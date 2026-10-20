package service

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/karthikponna/tryboxi/internal/network/policy"
	"github.com/karthikponna/tryboxi/internal/scheduler"
	"github.com/karthikponna/tryboxi/internal/store/db"
)

// poolVM is a VM restored from a golden snapshot and left paused, ready to
// be claimed by the next create. It owns a pre-assigned sandbox id/dir.
type poolVM struct {
	box     *Box
	tplID   string
	snapID  string
	vcpu    int
	memMB   int
	diskMB  int
	created time.Time
	m       *Manager
}

func (p *poolVM) discard() {
	p.box.killVM()
	p.m.removeData(p.box.row.DirPath)
}

type pool struct {
	m        *Manager
	mu       sync.Mutex
	vms      []*poolVM
	demand   map[string]time.Time // template id -> last create
	rate     map[string]float64   // EWMA creates per minute
	filling  map[string]bool
	disabled bool
}

func newPool(m *Manager) *pool {
	return &pool{m: m, demand: map[string]time.Time{}, rate: map[string]float64{}, filling: map[string]bool{}, disabled: !m.cfg.WarmPool.Enabled}
}

func (p *pool) noteDemand(tplID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	last, ok := p.demand[tplID]
	p.demand[tplID] = time.Now()
	inst := 1.0
	if ok {
		if gap := time.Since(last).Minutes(); gap > 0 {
			inst = 1 / gap
		}
	}
	p.rate[tplID] = 0.7*p.rate[tplID] + 0.3*inst
}

// claim hands out a matching pool VM, if any.
func (p *pool) claim(tplID string, vcpu, memMB, diskMB int) *poolVM {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, v := range p.vms {
		if v.tplID == tplID && v.vcpu == vcpu && v.memMB == memMB && v.diskMB == diskMB {
			p.vms = append(p.vms[:i], p.vms[i+1:]...)
			last, ok := p.demand[tplID]
			p.demand[tplID] = time.Now()
			inst := 1.0
			if ok {
				if gap := time.Since(last).Minutes(); gap > 0 {
					inst = 1 / gap
				}
			}
			p.rate[tplID] = 0.7*p.rate[tplID] + 0.3*inst
			return v
		}
	}
	return nil
}

// adoptPoolVM moves a pool VM into b: same id-less runtime, b's policy.
func (m *Manager) adoptPoolVM(ctx context.Context, b *Box, pv *poolVM) error {
	pb := pv.box
	pb.mu.Lock()
	vm, nw, gc, res := pb.vm, pb.net, pb.gc, pb.res
	pb.vm, pb.net, pb.gc, pb.res = nil, nil, nil, nil
	pb.mu.Unlock()
	if vm == nil {
		return errState("pool VM gone")
	}
	// The pool VM's files live in its own dir; move them under b's dir.
	if err := os.Rename(pb.row.DirPath, b.row.DirPath); err != nil {
		_ = os.RemoveAll(b.row.DirPath)
		if err := os.Rename(pb.row.DirPath, b.row.DirPath); err != nil {
			_ = vm.Kill()
			nw.Close()
			res.Release()
			return err
		}
	}
	nw.SetPolicy(b.policy())
	b.mu.Lock()
	// b already holds its own reservation; drop the pool's.
	b.vm, b.net, b.gc = vm, nw, gc
	b.mu.Unlock()
	res.Release()
	if err := vm.Resume(ctx); err != nil {
		return err
	}
	if err := m.waitGuest(ctx, vm, gc, 10*time.Second); err != nil {
		return err
	}
	return gc.Refresh(ctx)
}

// tick refills and shrinks the pool.
func (p *pool) tick() {
	if p.disabled {
		return
	}
	m := p.m
	cfg := m.cfg.WarmPool
	p.mu.Lock()
	// Shrink: drop VMs for templates with no recent demand, and old ones.
	var keep []*poolVM
	var drop []*poolVM
	for _, v := range p.vms {
		if time.Since(p.demand[v.tplID]) > time.Duration(cfg.IdleShrinkS)*time.Second && time.Since(v.created) > time.Minute {
			drop = append(drop, v)
		} else {
			keep = append(keep, v)
		}
	}
	p.vms = keep
	p.mu.Unlock()
	for _, v := range drop {
		v.discard()
		v.box.res.Release()
	}
	if m.sched.Pressure() != scheduler.PressureNormal {
		return
	}
	// Refill: base always (min_warm), plus templates used recently.
	base, err := m.st.R.GetReadyTemplateByName(m.ctx, baseTemplateName)
	if err != nil {
		return
	}
	targets := map[string]int{base.ID: cfg.MinWarm}
	p.mu.Lock()
	for tpl, last := range p.demand {
		if time.Since(last) < time.Duration(cfg.IdleShrinkS)*time.Second {
			want := cfg.MinWarm
			if p.rate[tpl] > 1 {
				want = min(cfg.MaxWarm, cfg.MinWarm+int(p.rate[tpl]))
			}
			if want > targets[tpl] {
				targets[tpl] = want
			}
		}
	}
	have := map[string]int{}
	for _, v := range p.vms {
		have[v.tplID]++
	}
	p.mu.Unlock()
	d := m.cfg.Defaults
	for tpl, want := range targets {
		if have[tpl] >= want {
			continue
		}
		p.mu.Lock()
		busy := p.filling[tpl]
		p.filling[tpl] = true
		p.mu.Unlock()
		if busy {
			continue
		}
		go func(tpl string) {
			defer func() {
				p.mu.Lock()
				p.filling[tpl] = false
				p.mu.Unlock()
			}()
			if err := p.fill(tpl, d.VCPU, d.MemMB, d.DiskMB); err != nil {
				m.log.Debug("pool fill skipped", "template", tpl, "err", err)
			}
		}(tpl)
	}
}

func (p *pool) fill(tplID string, vcpu, memMB, diskMB int) error {
	m := p.m
	snap, ok := m.findGolden(m.ctx, tplID, vcpu, memMB, diskMB)
	if !ok {
		return errNotFound("golden snapshot for template", tplID)
	}
	res, err := m.sched.Reserve(m.ctx, scheduler.Request{ClientID: "pool", VCPU: vcpu, MemMB: memMB, Pool: true}, 0)
	if err != nil {
		return err
	}
	b := m.builderBox(vcpu, memMB, diskMB, policy.Spec{Mode: policy.ModeNone}, "")
	b.res = res
	if err := m.restoreInto(m.ctx, b, snap); err != nil {
		m.discardBuilder(b)
		return err
	}
	b.mu.Lock()
	vm := b.vm
	b.mu.Unlock()
	if err := vm.Pause(m.ctx); err != nil {
		m.discardBuilder(b)
		return err
	}
	p.mu.Lock()
	p.vms = append(p.vms, &poolVM{box: b, tplID: tplID, snapID: snap.ID, vcpu: vcpu, memMB: memMB, diskMB: diskMB, created: time.Now(), m: m})
	p.mu.Unlock()
	m.bus.Emit("", "", "pool.filled", map[string]string{"template": tplID})
	return nil
}

// reclaim drops pool VMs to make room for real sandboxes.
func (p *pool) reclaim(need int) int {
	p.mu.Lock()
	n := min(need, len(p.vms))
	victims := append([]*poolVM(nil), p.vms[:n]...)
	p.vms = p.vms[n:]
	p.mu.Unlock()
	for _, v := range victims {
		v.discard()
	}
	return n
}

func (p *pool) drain() {
	p.mu.Lock()
	vms := p.vms
	p.vms = nil
	p.disabled = true
	p.mu.Unlock()
	for _, v := range vms {
		v.discard()
	}
}

func (p *pool) counts() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for _, v := range p.vms {
		out[v.tplID]++
	}
	return out
}

var _ = db.Template{}
