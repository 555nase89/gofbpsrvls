package record

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"gofbpsrvls/pkg/discover"
	"gofbpsrvls/pkg/flow"

	"github.com/555nase89/goFBP/pkg/fbp"
	"github.com/555nase89/goFBP/pkg/service"

	"github.com/nats-io/nats.go"
)

// DefaultInterval is how often the Collector looks for new Networks and
// Services.
const DefaultInterval = 10 * time.Second

// historyTimeout limits the wait for the Log history of one Service; a var
// so tests can shorten it.
var historyTimeout = 2 * time.Second

// forgetAfter is how long a Network or Service may be missing from the
// discovery before the Collector forgets it: it unsubscribes from a
// Network's subjects, and would ask a Service that comes back for its Log
// history again (the store keeps no duplicates). Longer than a few scans, so
// a discovery missing a busy Service once changes nothing; a var so tests
// can shorten it.
var forgetAfter = 5 * time.Minute

// Collector finds the goFBP Networks on NATS, subscribes to their Trace and
// Log subjects and sends everything into a Runner of the Network record. It
// also fetches the Log history of every Service it sees for the first time,
// so records written before the Collector started are kept as well. It does
// not switch observing on: Events arrive only while someone traces.
type Collector struct {
	nc     *nats.Conn
	rec    *flow.Runner
	logger *slog.Logger

	mu       sync.Mutex
	networks map[string]*watched  // by base subject
	services map[string]time.Time // Service IDs whose history was recorded, with when last discovered
}

// watched holds the subscriptions of a Network and when it was last discovered.
type watched struct {
	subs []*nats.Subscription
	seen time.Time
}

// NewCollector returns a Collector sending into rec.
func NewCollector(nc *nats.Conn, rec *flow.Runner, logger *slog.Logger) *Collector {
	return &Collector{nc: nc, rec: rec, logger: logger, networks: map[string]*watched{}, services: map[string]time.Time{}}
}

// Run looks for new Networks and Services every interval until ctx is done,
// then unsubscribes.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	defer c.unsubscribe()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := c.Scan(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("record: discovery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Scan runs one discovery: it subscribes to the Networks not yet watched and
// records the Log history of the Services whose history is not recorded yet.
// A Service whose history request failed is asked again on the next Scan.
// Networks and Services missing for forgetAfter are forgotten.
func (c *Collector) Scan(ctx context.Context) error {
	networks, err := discover.List(ctx, c.nc, discover.DefaultWait)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, n := range networks {
		if err := c.watch(ctx, n.Base, now); err != nil {
			c.logger.Warn("record: subscribe failed", "base", n.Base, "error", err)
			continue
		}
		for _, s := range n.Services {
			c.mu.Lock()
			_, seen := c.services[s.ID]
			if seen {
				c.services[s.ID] = now
			}
			c.mu.Unlock()
			if !seen && c.history(ctx, n.Base, s.ID) {
				c.mu.Lock()
				c.services[s.ID] = now
				c.mu.Unlock()
			}
		}
	}
	c.forget(now.Add(-forgetAfter))
	return nil
}

// forget unsubscribes from the Networks and forgets the Services last
// discovered before t.
func (c *Collector) forget(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for base, w := range c.networks {
		if w.seen.Before(t) {
			for _, s := range w.subs {
				s.Unsubscribe()
			}
			delete(c.networks, base)
			c.logger.Info("record: network gone", "base", base)
		}
	}
	for id, seen := range c.services {
		if seen.Before(t) {
			delete(c.services, id)
		}
	}
}

// watch subscribes to the Trace and Log subjects of base once and notes now
// as when it was discovered. The handlers block while the Network record is
// busy, so NATS buffers the messages.
func (c *Collector) watch(ctx context.Context, base string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := c.networks[base]; w != nil {
		w.seen = now
		return nil
	}
	trace, err := c.nc.Subscribe(service.TraceSubject(base), func(msg *nats.Msg) {
		c.send(ctx, fbp.In, base, "event", msg.Data)
	})
	if err != nil {
		return err
	}
	logs, err := c.nc.Subscribe(service.LogSubject(base, "*"), func(msg *nats.Msg) {
		c.send(ctx, LogInport, base, "log", msg.Data)
	})
	if err != nil {
		trace.Unsubscribe()
		return err
	}
	c.networks[base] = &watched{subs: []*nats.Subscription{trace, logs}, seen: now}
	c.logger.Info("record: watching network", "base", base)
	return nil
}

// history records the Log history of one Service and reports whether it is
// done. A Service without one (older goFBP, no responders) or with an
// invalid one is done as well; a timeout or other error is not, so the next
// Scan asks again.
func (c *Collector) history(ctx context.Context, base, id string) bool {
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	msg, err := c.nc.RequestWithContext(ctx, service.LogHistorySubject(base, id), nil)
	if errors.Is(err, nats.ErrNoResponders) {
		c.logger.Debug("record: no log history", "base", base, "service", id)
		return true
	}
	if err != nil {
		c.logger.Warn("record: log history failed, retrying on next scan", "base", base, "service", id, "error", err)
		return false
	}
	var records []json.RawMessage
	if err := json.Unmarshal(msg.Data, &records); err != nil {
		c.logger.Warn("record: invalid log history", "base", base, "service", id, "error", err)
		return true
	}
	for _, r := range records {
		c.send(ctx, LogInport, base, "log", r)
	}
	return true
}

// send wraps data, the JSON of a service.EventData or service.LogData, as
// EventInput or LogInput (field key) and sends it to inport.
func (c *Collector) send(ctx context.Context, inport, base, key string, data []byte) {
	in, err := json.Marshal(map[string]any{"base": base, key: json.RawMessage(data)})
	if err == nil {
		err = c.rec.SendData(ctx, inport, in)
	}
	if err != nil && ctx.Err() == nil {
		c.logger.Warn("record: message lost", "base", base, "inport", inport, "error", err)
	}
}

func (c *Collector) unsubscribe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for base, w := range c.networks {
		for _, s := range w.subs {
			s.Unsubscribe()
		}
		delete(c.networks, base)
	}
}
