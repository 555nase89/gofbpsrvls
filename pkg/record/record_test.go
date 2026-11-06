package record

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"gofbpsrvls/pkg/flow"
	"gofbpsrvls/pkg/store"

	"github.com/555nase89/goFBP/examples/text"
	"github.com/555nase89/goFBP/pkg/fbp"
	"github.com/555nase89/goFBP/pkg/service"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

const timeout = 2 * time.Second

var quiet = slog.New(slog.DiscardHandler)

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

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func startRunner(t *testing.T, net *fbp.Network, err error) *flow.Runner {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	r, err := flow.Start(context.Background(), net, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	return r
}

func TestRecordNetwork(t *testing.T) {
	st := openStore(t)
	net, err := Record(st)
	r := startRunner(t, net, err)
	ctx := context.Background()

	in := EventInput{Base: "fbp.a.n", Event: service.EventData{Service: "s1", Kind: "Sent", Time: time.Now(), RunID: "r"}}
	if outport, _, err := r.Call(ctx, fbp.In, in); err != nil || outport != fbp.Success {
		t.Fatalf("event: %s, %v", outport, err)
	}
	log := LogInput{Base: "fbp.a.n", Log: service.LogData{Service: "s1", Seq: 1, Time: time.Now(), Level: "INFO", Message: "hi"}}
	for range 2 {
		if outport, _, err := r.Call(ctx, LogInport, log); err != nil || outport != fbp.Success {
			t.Fatalf("log: %s, %v", outport, err)
		}
	}
	outport, p, err := r.Call(ctx, fbp.In, EventInput{Base: "fbp.a.n"})
	if err != nil || outport != fbp.Error || flow.ErrorMessage(p) != "event without base, service or kind" {
		t.Fatalf("invalid event: %s %s, %v", outport, p.Data, err)
	}
	events, _ := st.Events(ctx, store.Page{Base: "fbp.a.n"})
	logs, _ := st.Logs(ctx, store.Page{Base: "fbp.a.n"})
	if len(events) != 1 || len(logs) != 1 {
		t.Fatalf("events %+v, logs %+v", events, logs)
	}
}

func TestPruneNetwork(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	st.InsertEvent(ctx, "fbp.a.n", service.EventData{Service: "s", Kind: "Sent", Time: old})
	st.InsertEvent(ctx, "fbp.a.n", service.EventData{Service: "s", Kind: "Sent", Time: time.Now()})
	st.InsertLog(ctx, "fbp.a.n", service.LogData{Service: "s", Seq: 1, Time: old, Level: "INFO"})
	net, err := Prune(st)
	r := startRunner(t, net, err)
	if err := r.Send(ctx, fbp.In, PruneInput{Before: time.Now().Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(timeout)
	for {
		events, _ := st.Events(ctx, store.Page{Base: "fbp.a.n"})
		logs, _ := st.Logs(ctx, store.Page{Base: "fbp.a.n"})
		if len(events) == 1 && len(logs) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("events %d, logs %d", len(events), len(logs))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for " + what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCollector(t *testing.T) {
	nc := connect(t)
	st := openStore(t)
	ctx := context.Background()
	net, err := text.Pipeline()
	if err != nil {
		t.Fatal(err)
	}
	s, err := service.Start(ctx, nc, text.PackagePath, net, service.WithLogger(quiet))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop() })
	base := s.Base()
	// Written before the Collector starts: only in the Log history.
	s.Logger().Info("before collector")

	recNet, err := Record(st)
	rec := startRunner(t, recNet, err)
	c := NewCollector(nc, rec, quiet)
	if err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.unsubscribe)
	logs := func() []store.Log { l, _ := st.Logs(ctx, store.Page{Base: base}); return l }
	waitFor(t, "log history", func() bool { return len(logs()) == 1 })

	// Live record; a second scan must not record the history again.
	s.Logger().Warn("live")
	if err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "live log", func() bool { return len(logs()) == 2 })

	// No Events while observing is off.
	if _, _, err := service.Call(ctx, nc, base, []byte(`"a"`)); err != nil {
		t.Fatal(err)
	}
	nc.Publish(service.ObserveSubject(base), []byte(`true`))
	nc.Flush()
	waitFor(t, "observing log", func() bool { return len(logs()) == 3 })
	if events, _ := st.Events(ctx, store.Page{Base: base}); len(events) != 0 {
		t.Fatalf("events recorded while not observing: %d", len(events))
	}
	if _, _, err := service.Call(ctx, nc, base, []byte(`"b"`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "events", func() bool {
		events, _ := st.Events(ctx, store.Page{Base: base})
		return slices.ContainsFunc(events, func(e store.Event) bool { return e.Kind == "Output" })
	})
	got := logs()
	if got[0].Message != "before collector" || got[1].Message != "live" || got[2].Message != "service: observing switched" {
		t.Fatalf("logs %+v", got)
	}
}

// A Service whose Log history request times out is asked again on the next
// Scan instead of losing its history.
func TestCollectorRetriesHistory(t *testing.T) {
	nc := connect(t)
	st := openStore(t)
	ctx := context.Background()
	defer func(d time.Duration) { historyTimeout = d }(historyTimeout)
	historyTimeout = 100 * time.Millisecond

	// A micro service that looks like a goFBP Service to discover.List.
	const base = "fbp.test.n"
	svc, err := micro.AddService(nc, micro.Config{Name: "n", Version: "1.0.0",
		Metadata: map[string]string{"base": base, "network": "n"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Stop() })
	id := svc.Info().ID
	history, _ := json.Marshal([]service.LogData{{Service: id, Seq: 1, Time: time.Now(), Level: "INFO", Message: "old"}})
	var requests atomic.Int32
	sub, err := nc.Subscribe(service.LogHistorySubject(base, id), func(msg *nats.Msg) {
		// The first request goes unanswered, as if the Service were busy.
		if requests.Add(1) > 1 {
			msg.Respond(history)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sub.Unsubscribe() })

	recNet, err := Record(st)
	rec := startRunner(t, recNet, err)
	c := NewCollector(nc, rec, quiet)
	t.Cleanup(c.unsubscribe)
	logs := func() []store.Log { l, _ := st.Logs(ctx, store.Page{Base: base}); return l }
	if err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(logs()) != 0 {
		t.Fatal("history recorded without answer")
	}
	if err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "log history on second scan", func() bool { return len(logs()) == 1 })
	// Recorded once: no third request.
	if err := c.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 2 {
		t.Fatalf("%d history requests, want 2", n)
	}
}
