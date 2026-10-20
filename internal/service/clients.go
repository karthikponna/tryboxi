package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/karthikponna/tryboxi/internal/ids"
	"github.com/karthikponna/tryboxi/internal/store/db"
)

// EnsureClient returns the client row for name, creating it on first use.
func (m *Manager) EnsureClient(ctx context.Context, name string, uid int) Caller {
	c, err := m.st.R.GetClientByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		id := ids.New(ids.Client)
		p := db.InsertClientParams{ID: id, Name: name, CreatedAt: time.Now().UnixMilli()}
		if uid >= 0 {
			p.Uid = sql.NullInt64{Int64: int64(uid), Valid: true}
		}
		if err := m.st.W.InsertClient(ctx, p); err == nil {
			return Caller{ClientID: id}
		}
		c, err = m.st.R.GetClientByName(ctx, name)
	}
	if err != nil {
		return Caller{ClientID: name}
	}
	return Caller{ClientID: c.ID, Priority: int(c.Priority)}
}
