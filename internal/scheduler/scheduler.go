// Package scheduler admits sandboxes against vCPU, memory, and count
// budgets. One goroutine owns the ledger and the wait queue; callers talk
// to it over channels.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"
)

// Budget is the total capacity.
type Budget struct {
	VCPU         float64 // already multiplied by overcommit
	MemMB        int
	MaxSandboxes int
	MaxQueue     int
}

// Request asks for a reservation.
type Request struct {
	ClientID string
	Priority int // higher first
	VCPU     int
	MemMB    int
	// Pool reservations are evicted (via the Reclaimer) when real requests
	// need room, and never queue.
	Pool bool
}

// Pressure is the host memory pressure level.
type Pressure int

const (
	PressureNormal Pressure = iota
	PressureWarn
	PressureCritical
)

// ErrCapacity is returned when a request can't be admitted in time.
type ErrCapacity struct {
	RetryAfter time.Duration
	Reason     string
}

func (e *ErrCapacity) Error() string {
	return fmt.Sprintf("capacity exhausted: %s (retry in %s)", e.Reason, e.RetryAfter.Round(time.Second))
}

// IsCapacity reports whether err is a capacity error.
func IsCapacity(err error) (*ErrCapacity, bool) {
	var c *ErrCapacity
	ok := errors.As(err, &c)
	return c, ok
}

// Reservation holds resources until Release.
type Reservation struct {
	ID       uint64
	ClientID string
	VCPU     int
	MemMB    int
	Pool     bool
	s        *Scheduler
	released atomic.Bool
}

// Release returns resources to the ledger. Safe to call more than once.
func (r *Reservation) Release() {
	if r == nil || !r.released.CompareAndSwap(false, true) {
		return
	}
	r.s.cmds <- func() { r.s.release(r) }
}

// Convert turns a pool reservation into a regular one owned by clientID.
func (r *Reservation) Convert(clientID string) {
	done := make(chan struct{})
	r.s.cmds <- func() {
		if r.Pool {
			r.Pool = false
			r.s.poolCount--
		}
		r.ClientID = clientID
		r.s.active[clientID]++
		close(done)
	}
	<-done
}

// Reclaimer frees pool reservations; it returns how many it released.
type Reclaimer func(need int) int

type waiter struct {
	req   Request
	at    time.Time
	ready chan *Reservation
	gone  bool
	seq   uint64
}

// Capacity is a point-in-time view of the ledger.
type Capacity struct {
	VCPUTotal  float64 `json:"vcpu_total"`
	VCPUFree   float64 `json:"vcpu_free"`
	MemTotalMB int     `json:"mem_total_mb"`
	MemFreeMB  int     `json:"mem_free_mb"`
	Sandboxes  int     `json:"sandboxes"`
	MaxSandbox int     `json:"max_sandboxes"`
	PoolVMs    int     `json:"pool_vms"`
	QueueLen   int     `json:"queue_len"`
	Pressure   string  `json:"memory_pressure"`
}

// Scheduler is the admission controller.
type Scheduler struct {
	budget    Budget
	cmds      chan func()
	usedVCPU  float64
	usedMem   int
	count     int
	poolCount int
	queue     []*waiter
	active    map[string]int // live reservations per client
	nextID    uint64
	pressure  func() Pressure
	reclaim   Reclaimer
}

// New starts the scheduler goroutine; it stops when ctx is done.
func New(ctx context.Context, b Budget, pressure func() Pressure) *Scheduler {
	if b.MaxQueue <= 0 {
		b.MaxQueue = 32
	}
	if pressure == nil {
		pressure = func() Pressure { return PressureNormal }
	}
	s := &Scheduler{budget: b, cmds: make(chan func(), 64), active: map[string]int{}, pressure: pressure}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case fn := <-s.cmds:
				fn()
			}
		}
	}()
	return s
}

// SetReclaimer installs the warm-pool eviction hook.
func (s *Scheduler) SetReclaimer(r Reclaimer) {
	done := make(chan struct{})
	s.cmds <- func() { s.reclaim = r; close(done) }
	<-done
}

func (s *Scheduler) fits(req Request) bool {
	return s.usedVCPU+float64(req.VCPU) <= s.budget.VCPU+1e-9 &&
		s.usedMem+req.MemMB <= s.budget.MemMB &&
		s.count+1 <= s.budget.MaxSandboxes
}

func (s *Scheduler) grant(req Request) *Reservation {
	s.nextID++
	s.usedVCPU += float64(req.VCPU)
	s.usedMem += req.MemMB
	s.count++
	if req.Pool {
		s.poolCount++
	} else {
		s.active[req.ClientID]++
	}
	return &Reservation{ID: s.nextID, ClientID: req.ClientID, VCPU: req.VCPU, MemMB: req.MemMB, Pool: req.Pool, s: s}
}

func (s *Scheduler) release(r *Reservation) {
	s.usedVCPU -= float64(r.VCPU)
	s.usedMem -= r.MemMB
	s.count--
	if r.Pool {
		s.poolCount--
	} else if s.active[r.ClientID]--; s.active[r.ClientID] <= 0 {
		delete(s.active, r.ClientID)
	}
	s.dispatch()
}

// dispatch admits queued waiters in (priority, fairness, age) order.
func (s *Scheduler) dispatch() {
	for {
		live := s.queue[:0]
		for _, w := range s.queue {
			if !w.gone {
				live = append(live, w)
			}
		}
		s.queue = live
		if len(s.queue) == 0 {
			return
		}
		sort.SliceStable(s.queue, func(i, j int) bool {
			a, b := s.queue[i], s.queue[j]
			if a.req.Priority != b.req.Priority {
				return a.req.Priority > b.req.Priority
			}
			if s.active[a.req.ClientID] != s.active[b.req.ClientID] {
				return s.active[a.req.ClientID] < s.active[b.req.ClientID]
			}
			return a.seq < b.seq
		})
		w := s.queue[0]
		if !s.fits(w.req) {
			if s.poolCount > 0 && s.reclaim != nil {
				r := s.reclaim
				// Reclaim runs outside the loop; released pool VMs call back
				// into release(), which re-dispatches.
				go r(1)
			}
			return
		}
		s.queue = s.queue[1:]
		w.ready <- s.grant(w.req)
	}
}

// Reserve blocks until resources are granted, ctx ends, or wait elapses.
func (s *Scheduler) Reserve(ctx context.Context, req Request, wait time.Duration) (*Reservation, error) {
	type result struct {
		r   *Reservation
		w   *waiter
		err error
	}
	resCh := make(chan result, 1)
	s.cmds <- func() {
		if s.pressure() == PressureCritical {
			resCh <- result{err: &ErrCapacity{RetryAfter: 30 * time.Second, Reason: "host memory pressure is critical"}}
			return
		}
		if float64(req.VCPU) > s.budget.VCPU || req.MemMB > s.budget.MemMB {
			resCh <- result{err: &ErrCapacity{RetryAfter: time.Hour, Reason: fmt.Sprintf("request (%d vCPU, %d MB) exceeds the host budget", req.VCPU, req.MemMB)}}
			return
		}
		if len(s.queue) == 0 && s.fits(req) {
			resCh <- result{r: s.grant(req)}
			return
		}
		if req.Pool {
			resCh <- result{err: &ErrCapacity{RetryAfter: time.Minute, Reason: "no room for warm pool"}}
			return
		}
		if s.poolCount > 0 && s.reclaim != nil {
			go s.reclaim(1)
		}
		if len(s.queue) >= s.budget.MaxQueue {
			resCh <- result{err: &ErrCapacity{RetryAfter: 20 * time.Second, Reason: "queue is full"}}
			return
		}
		s.nextID++
		w := &waiter{req: req, at: time.Now(), ready: make(chan *Reservation, 1), seq: s.nextID}
		s.queue = append(s.queue, w)
		resCh <- result{w: w}
	}
	res := <-resCh
	if res.err != nil || res.r != nil {
		return res.r, res.err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-res.w.ready:
		return r, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	// Withdraw; a grant may have raced in.
	done := make(chan *Reservation, 1)
	s.cmds <- func() {
		res.w.gone = true
		select {
		case r := <-res.w.ready:
			done <- r
		default:
			done <- nil
		}
	}
	if r := <-done; r != nil {
		return r, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, &ErrCapacity{RetryAfter: 20 * time.Second, Reason: "all sandboxes busy"}
}

// Capacity returns the current ledger.
func (s *Scheduler) Capacity() Capacity {
	ch := make(chan Capacity, 1)
	s.cmds <- func() {
		p := s.pressure()
		ch <- Capacity{
			VCPUTotal: s.budget.VCPU, VCPUFree: s.budget.VCPU - s.usedVCPU,
			MemTotalMB: s.budget.MemMB, MemFreeMB: s.budget.MemMB - s.usedMem,
			Sandboxes: s.count - s.poolCount, MaxSandbox: s.budget.MaxSandboxes,
			PoolVMs: s.poolCount, QueueLen: len(s.queue), Pressure: [...]string{"normal", "warn", "critical"}[p],
		}
	}
	return <-ch
}

// Pressure reports the current host memory pressure.
func (s *Scheduler) Pressure() Pressure { return s.pressure() }
