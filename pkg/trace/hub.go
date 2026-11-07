// Package trace receives what goFBP Services publish on a subject, e.g. the
// Events on the Trace subject or the log records on the Log subjects of a
// Network, and hands it to any number of watchers, e.g. the browsers showing
// that Network.
package trace

import (
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Defaults of a Hub.
const (
	// DefaultHistory is how many messages per subject a new watcher gets first.
	DefaultHistory = 500
	// DefaultBuffer is the channel size per watcher. A watcher that falls
	// further behind loses messages instead of blocking the others.
	DefaultBuffer = 256
	// DefaultLinger is how long a subject stays subscribed after its last
	// watcher has gone, so a browser coming back (reload, switching between
	// Networks) still finds the recent messages, including those that came
	// in between.
	DefaultLinger = 10 * time.Minute
)

// Hub holds one NATS subscription per watched subject, from the first
// watcher of a subject until linger after the last one has gone. Messages
// are passed on as raw data, e.g. the JSON of service.EventData.
type Hub struct {
	nc      *nats.Conn
	history int
	buffer  int
	linger  time.Duration

	mu       sync.Mutex
	subjects map[string]*subject
}

type subject struct {
	sub      *nats.Subscription
	watchers map[chan []byte]struct{}
	recent   [][]byte // ring of the last messages, oldest at next once full
	next     int
	idle     *time.Timer // ends the subscription while there is no watcher
}

// NewHub returns a Hub receiving messages via nc.
func NewHub(nc *nats.Conn) *Hub {
	return &Hub{nc: nc, history: DefaultHistory, buffer: DefaultBuffer, linger: DefaultLinger, subjects: map[string]*subject{}}
}

// Watch returns a channel with the recent and all further messages on
// subj, which may contain wildcards, e.g. service.TraceSubject(base). stop
// ends the watch and closes the channel; it must be called exactly once.
func (h *Hub) Watch(subj string) (msgs <-chan []byte, stop func(), err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bs := h.subjects[subj]
	if bs == nil {
		bs = &subject{watchers: map[chan []byte]struct{}{}}
		bs.sub, err = h.nc.Subscribe(subj, func(msg *nats.Msg) { h.receive(bs, msg.Data) })
		if err != nil {
			return nil, nil, err
		}
		h.subjects[subj] = bs
	}
	if bs.idle != nil {
		bs.idle.Stop()
		bs.idle = nil
	}
	recent := bs.ordered()
	ch := make(chan []byte, max(h.buffer, len(recent)))
	for _, e := range recent {
		ch <- e
	}
	bs.watchers[ch] = struct{}{}
	return ch, func() { h.unwatch(subj, bs, ch) }, nil
}

func (h *Hub) unwatch(subj string, bs *subject, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(bs.watchers, ch)
	close(ch)
	if len(bs.watchers) == 0 {
		bs.idle = time.AfterFunc(h.linger, func() { h.expire(subj, bs) })
	}
}

// expire ends the subscription of bs unless a watcher came back meanwhile.
func (h *Hub) expire(subj string, bs *subject) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(bs.watchers) > 0 || h.subjects[subj] != bs {
		return
	}
	bs.sub.Unsubscribe()
	delete(h.subjects, subj)
}

// Close ends all subscriptions. Watchers must have stopped before.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for subj, bs := range h.subjects {
		if bs.idle != nil {
			bs.idle.Stop()
		}
		bs.sub.Unsubscribe()
		delete(h.subjects, subj)
	}
}

// receive stores a message and passes it to every watcher of bs that has
// room for it.
func (h *Hub) receive(bs *subject, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(bs.recent) < h.history {
		bs.recent = append(bs.recent, data)
	} else {
		bs.recent[bs.next] = data
		bs.next = (bs.next + 1) % h.history
	}
	for ch := range bs.watchers {
		select {
		case ch <- data:
		default:
		}
	}
}

// ordered returns the recent messages, oldest first.
func (bs *subject) ordered() [][]byte {
	return append(append([][]byte{}, bs.recent[bs.next:]...), bs.recent[:bs.next]...)
}
