package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gofbpsrvls/pkg/discover"

	"github.com/555nase89/goFBP/examples/text"
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

// startPipeline starts n Services of the text pipeline and returns the base
// subject.
func startPipeline(t *testing.T, nc *nats.Conn, n int) string {
	t.Helper()
	net, err := text.Pipeline()
	if err != nil {
		t.Fatal(err)
	}
	var base string
	for range n {
		s, err := service.Start(context.Background(), nc, text.PackagePath, net)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Stop() })
		base = s.Base()
	}
	return base
}

func newServer(t *testing.T) (*nats.Conn, *httptest.Server) {
	t.Helper()
	nc := connect(t)
	ts := httptest.NewServer(New(nc, 200*time.Millisecond, slog.New(slog.DiscardHandler)))
	t.Cleanup(ts.Close)
	return nc, ts
}

func getNetworks(t *testing.T, ts *httptest.Server) []discover.Network {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/networks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var networks []discover.Network
	if err := json.NewDecoder(resp.Body).Decode(&networks); err != nil {
		t.Fatal(err)
	}
	return networks
}

func setObserve(t *testing.T, ts *httptest.Server, base string, on bool) int {
	t.Helper()
	body, _ := json.Marshal(ObserveRequest{Base: base, On: on})
	resp, err := http.Post(ts.URL+"/api/networks/observe", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestNetworksAndObserve(t *testing.T) {
	nc, ts := newServer(t)
	base := startPipeline(t, nc, 2)

	networks := getNetworks(t, ts)
	if len(networks) != 1 {
		t.Fatalf("got %+v", networks)
	}
	n := networks[0]
	if n.Base != base || n.Name != "pipeline" || n.Package != text.PackagePath || len(n.Services) != 2 ||
		n.Inports[0] != "In" || !strings.Contains(strings.Join(n.Outports, ","), text.Empty) {
		t.Fatalf("got %+v", n)
	}
	for _, s := range n.Services {
		if s.Observing {
			t.Fatalf("observing after start: %+v", s)
		}
	}

	if code := setObserve(t, ts, base, true); code != http.StatusNoContent {
		t.Fatalf("status %d", code)
	}
	for _, s := range getNetworks(t, ts)[0].Services {
		if !s.Observing {
			t.Fatalf("not observing: %+v", s)
		}
	}
	setObserve(t, ts, base, false)
	for _, s := range getNetworks(t, ts)[0].Services {
		if s.Observing {
			t.Fatalf("still observing: %+v", s)
		}
	}

	if code := setObserve(t, ts, "fbp.unknown.net", true); code != http.StatusNotFound {
		t.Fatalf("unknown base: status %d", code)
	}
}

func TestEvents(t *testing.T) {
	nc, ts := newServer(t)
	base := startPipeline(t, nc, 1)
	setObserve(t, ts, base, true)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/networks/events?base="+base, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := bufio.NewReader(resp.Body)
	// The stream is open once its first comment arrived.
	if _, err := lines.ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	if _, _, err := service.Call(ctx, nc, base, []byte(`"  hello "`)); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for !kinds["Output"] {
		line, err := lines.ReadString('\n')
		if err == io.EOF || ctx.Err() != nil {
			t.Fatalf("stream ended, kinds %v", kinds)
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var e service.EventData
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatal(err)
		}
		if e.Network == "" || e.RunID == "" {
			t.Fatalf("event %+v", e)
		}
		kinds[e.Kind] = true
	}
}

// sseData reads the next "data:" line of a Server-Sent Events stream.
func sseData(t *testing.T, ctx context.Context, lines *bufio.Reader) string {
	t.Helper()
	for {
		line, err := lines.ReadString('\n')
		if err != nil || ctx.Err() != nil {
			t.Fatalf("stream ended: %v", err)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			return strings.TrimSpace(data)
		}
	}
}

func TestLogs(t *testing.T) {
	nc, ts := newServer(t)
	net, err := text.Pipeline()
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.DiscardHandler)
	var svcs []*service.Service
	for range 2 {
		s, err := service.Start(context.Background(), nc, text.PackagePath, net, service.WithLogger(quiet))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Stop() })
		svcs = append(svcs, s)
	}
	base := svcs[0].Base()
	svcs[0].Logger().Info("first", "n", 1)
	svcs[1].Logger().Warn("second")
	svcs[0].Logger().Debug("not published")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/networks/logs?base="+base, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	lines := bufio.NewReader(resp.Body)
	next := func() service.LogData {
		t.Helper()
		var d service.LogData
		if err := json.Unmarshal([]byte(sseData(t, ctx, lines)), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	// The history of both Services, by time.
	if d := next(); d.Message != "first" || d.Service != svcs[0].ID() || d.Level != "INFO" || d.Attrs["n"] != 1.0 {
		t.Fatalf("got %+v", d)
	}
	if d := next(); d.Message != "second" || d.Service != svcs[1].ID() || d.Level != "WARN" {
		t.Fatalf("got %+v", d)
	}
	// Then live records, without the history again.
	svcs[1].Logger().Info("live")
	if d := next(); d.Message != "live" || d.Service != svcs[1].ID() {
		t.Fatalf("got %+v", d)
	}
}

func TestEventsUnknownBase(t *testing.T) {
	_, ts := newServer(t)
	resp, err := http.Get(ts.URL + "/api/networks/events?base=fbp.unknown.net")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestStatic(t *testing.T) {
	_, ts := newServer(t)
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
	}
}
