// Package record stores what goFBP Services publish on NATS: the Collector
// receives the Events and log records and sends them into the Network
// "record", whose Bricks write them to the store. The Network "prune"
// deletes old rows.
package record

import (
	"context"
	"errors"
	"time"

	"gofbpsrvls/pkg/store"

	"github.com/555nase89/goFBP/pkg/fbp"
	"github.com/555nase89/goFBP/pkg/service"
)

// EventInput is the Packet data of Inport In of the Network record: one
// Event of the Network with base subject Base.
type EventInput struct {
	Base  string            `json:"base"`
	Event service.EventData `json:"event"`
}

// LogInput is the Packet data of Inport Log of the Network record: one log
// record of a Service of the Network with base subject Base.
type LogInput struct {
	Base string          `json:"base"`
	Log  service.LogData `json:"log"`
}

// PruneInput is the Packet data of the Network prune: rows older than
// Before are deleted.
type PruneInput struct {
	Before time.Time `json:"before"`
}

// PruneResult is emitted by the prune Bricks.
type PruneResult struct {
	Table   string `json:"table"`
	Deleted int64  `json:"deleted"`
}

// CheckEvent passes an EventInput on if it names a Network, a Service and a
// kind.
func CheckEvent() *fbp.Brick {
	return &fbp.Brick{
		Name:        "CheckEvent",
		Description: "Passes an Event on if base, service and kind are set",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[EventInput](p)
			if err != nil {
				return err
			}
			if in.Base == "" || in.Event.Service == "" || in.Event.Kind == "" {
				return errors.New("event without base, service or kind")
			}
			return out.Emit(ctx, fbp.Success, p.Data)
		},
	}
}

// StoreEvent writes an EventInput to st. It runs one copy only, since
// SQLite has one writer.
func StoreEvent(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "StoreEvent",
		Description: "Writes an Event to the store",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[EventInput](p)
			if err != nil {
				return err
			}
			if err := st.InsertEvent(ctx, in.Base, in.Event); err != nil {
				return err
			}
			return out.Emit(ctx, fbp.Success, p.Data)
		},
	}
}

// CheckLog passes a LogInput on if it names a Network and a Service and has
// a sequence number.
func CheckLog() *fbp.Brick {
	return &fbp.Brick{
		Name:        "CheckLog",
		Description: "Passes a log record on if base, service and seq are set",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[LogInput](p)
			if err != nil {
				return err
			}
			if in.Base == "" || in.Log.Service == "" || in.Log.Seq == 0 {
				return errors.New("log record without base, service or seq")
			}
			return out.Emit(ctx, fbp.Success, p.Data)
		},
	}
}

// StoreLog writes a LogInput to st; a record already stored is ignored.
func StoreLog(st *store.Store) *fbp.Brick {
	return &fbp.Brick{
		Name:        "StoreLog",
		Description: "Writes a log record to the store, ignoring duplicates",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[LogInput](p)
			if err != nil {
				return err
			}
			if err := st.InsertLog(ctx, in.Base, in.Log); err != nil {
				return err
			}
			return out.Emit(ctx, fbp.Success, p.Data)
		},
	}
}

// pruneBrick builds a Brick deleting the rows of table older than Before
// with fn.
func pruneBrick(name, table string, fn func(context.Context, time.Time) (int64, error)) *fbp.Brick {
	return &fbp.Brick{
		Name:        name,
		Description: "Deletes the " + table + " older than before",
		Process: func(ctx context.Context, p fbp.Packet, out fbp.Emitter) error {
			in, err := fbp.DataAs[PruneInput](p)
			if err != nil {
				return err
			}
			if in.Before.IsZero() {
				return errors.New("prune without before")
			}
			n, err := fn(ctx, in.Before)
			if err != nil {
				return err
			}
			return fbp.EmitAs(ctx, out, fbp.Success, PruneResult{Table: table, Deleted: n})
		},
	}
}

// PruneEvents deletes the Events older than Before.
func PruneEvents(st *store.Store) *fbp.Brick {
	return pruneBrick("PruneEvents", "events", st.PruneEvents)
}

// PruneLogs deletes the log records older than Before.
func PruneLogs(st *store.Store) *fbp.Brick { return pruneBrick("PruneLogs", "logs", st.PruneLogs) }

// PruneSessions deletes the sessions that expired before Before; it is
// given the current time, not the retention limit.
func PruneSessions(st *store.Store) *fbp.Brick {
	return pruneBrick("PruneSessions", "sessions", func(ctx context.Context, _ time.Time) (int64, error) {
		return st.PruneSessions(ctx, time.Now())
	})
}
