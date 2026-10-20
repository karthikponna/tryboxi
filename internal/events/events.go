// Package events records daemon events in SQLite and fans them out live to
// SSE subscribers. The row id doubles as the SSE event id.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/karthikponna/tryboxi/internal/store"
	"github.com/karthikponna/tryboxi/internal/store/db"
)

// Event is one recorded event.
type Event struct {
	ID          int64           `json:"id"`
	Ts          int64           `json:"ts"`
	SandboxID   string          `json:"sandbox_id,omitempty"`
	ExecutionID string          `json:"execution_id,omitempty"`
	Type        string          `json:"type"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// Bus persists and broadcasts events.
type Bus struct {
	st   *store.Store
	log  *slog.Logger
	mu   sync.Mutex
	subs map[chan Event]string // channel -> sandbox filter ("" = all)
}

func New(st *store.Store, log *slog.Logger) *Bus {
	return &Bus{st: st, log: log, subs: map[chan Event]string{}}
}

// Emit records an event. data may be nil or any JSON-marshalable value.
func (b *Bus) Emit(sandboxID, execID, typ string, data any) {
	raw := json.RawMessage("{}")
	if data != nil {
		if bs, err := json.Marshal(data); err == nil {
			raw = bs
		}
	}
	ev := Event{Ts: time.Now().UnixMilli(), SandboxID: sandboxID, ExecutionID: execID, Type: typ, Data: raw}
	id, err := b.st.W.InsertEvent(context.Background(), db.InsertEventParams{
		Ts: ev.Ts, SandboxID: store.NS(sandboxID), ExecutionID: store.NS(execID), Type: typ, Data: string(raw),
	})
	if err != nil {
		b.log.Warn("event insert failed", "type", typ, "err", err)
	}
	ev.ID = id
	b.log.Debug("event", "type", typ, "sandbox", sandboxID, "exec", execID, "data", string(raw))
	b.mu.Lock()
	for ch, filter := range b.subs {
		if filter != "" && filter != sandboxID {
			continue
		}
		select {
		case ch <- ev:
		default:
		}
	}
	b.mu.Unlock()
}

// Subscribe returns past events after `after` plus a live channel.
func (b *Bus) Subscribe(ctx context.Context, after int64, sandboxID string) ([]Event, chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = sandboxID
	b.mu.Unlock()
	var past []Event
	rows, err := b.st.R.ListEventsAfter(ctx, db.ListEventsAfterParams{After: after, SandboxID: store.NS(sandboxID), Limit: 1000})
	if err == nil {
		for _, r := range rows {
			past = append(past, fromRow(r))
		}
	}
	return past, ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func fromRow(r db.Event) Event {
	return Event{ID: r.ID, Ts: r.Ts, SandboxID: r.SandboxID.String, ExecutionID: r.ExecutionID.String, Type: r.Type, Data: json.RawMessage(r.Data)}
}
