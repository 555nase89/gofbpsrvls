package trace

import (
	"fmt"
	"testing"
	"time"

	"github.com/555nase89/goFBP/pkg/service"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

const timeout = 2 * time.Second

func connect(t *testing.T) *nats.Conn {
	t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(timeout) {
		t.Fatal("nats server not ready")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func next(t *testing.T, ch <-chan []byte) string {
	t.Helper()
	select {
	case e := <-ch:
		return string(e)
	case <-time.After(timeout):
		t.Fatal("no event")
		return ""
	}
}

func publish(t *testing.T, nc *nats.Conn, base string, events ...string) {
	t.Helper()
	for _, e := range events {
		if err := nc.Publish(service.TraceSubject(base), []byte(e)); err != nil {
			t.Fatal(err)
		}
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestHubHistoryAndFanOut(t *testing.T) {
	nc := connect(t)
	h := NewHub(nc)
	h.history = 3
	first, stop1, err := h.Watch(service.TraceSubject("fbp.a.b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	publish(t, nc, "fbp.a.b", "1", "2", "3", "4")
	publish(t, nc, "fbp.other.net", "x")
	for _, want := range []string{"1", "2", "3", "4"} {
		if got := next(t, first); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	}
	// A new watcher gets the last 3 Events first, oldest first.
	second, stop2, err := h.Watch(service.TraceSubject("fbp.a.b"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2", "3", "4"} {
		if got := next(t, second); got != want {
			t.Fatalf("history: got %s, want %s", got, want)
		}
	}
	publish(t, nc, "fbp.a.b", "5")
	if next(t, first) != "5" || next(t, second) != "5" {
		t.Fatal("event 5 not fanned out")
	}
	stop1()
	stop2()
	if _, ok := <-first; ok {
		t.Fatal("channel open after stop")
	}
	h.Close()
	if len(h.subjects) != 0 {
		t.Fatalf("subscription kept: %v", h.subjects)
	}
}

// Without watchers a subject keeps receiving for linger, so a watcher
// coming back gets what was published in between.
func TestHubLinger(t *testing.T) {
	nc := connect(t)
	h := NewHub(nc)
	h.linger = 200 * time.Millisecond
	subj := service.TraceSubject("fbp.a.b")
	_, stop, err := h.Watch(subj)
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	stop()
	publish(t, nc, "fbp.a.b", "1")
	ch, stop, err := h.Watch(subj)
	if err != nil {
		t.Fatal(err)
	}
	if got := next(t, ch); got != "1" {
		t.Fatalf("after coming back: got %s", got)
	}
	stop()
	// The watch in between restarted the linger; after it the subject ends.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.subjects)
		h.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscription kept after linger")
		}
		time.Sleep(20 * time.Millisecond)
	}
	publish(t, nc, "fbp.a.b", "2")
	ch, stop, err = h.Watch(subj)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if len(ch) != 0 {
		t.Fatalf("%d messages after linger, want none", len(ch))
	}
}

func TestHubSlowWatcher(t *testing.T) {
	nc := connect(t)
	h := NewHub(nc)
	h.buffer = 2
	ch, stop, err := h.Watch(service.TraceSubject("fbp.a.b"))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		publish(t, nc, "fbp.a.b", fmt.Sprint(i))
	}
	time.Sleep(50 * time.Millisecond)
	if len(ch) != 2 {
		t.Fatalf("buffered %d, want 2", len(ch))
	}
}
