package storeweb

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gofbpsrvls/pkg/flow"
	"gofbpsrvls/pkg/store"
	"gofbpsrvls/pkg/users"

	"github.com/555nase89/goFBP/examples/text"
	"github.com/555nase89/goFBP/pkg/fbp"
	"github.com/555nase89/goFBP/pkg/service"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

const timeout = 3 * time.Second

var quiet = slog.New(slog.DiscardHandler)

type env struct {
	nc *nats.Conn
	st *store.Store
	ts *httptest.Server
}

func setup(t *testing.T) env {
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
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	start := func(net *fbp.Network, err error) *flow.Runner {
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
	useradmin := start(users.Useradmin(st))
	for name, role := range map[string]string{"admin": store.RoleAdmin, "viewer": store.RoleViewer} {
		outport, p, err := useradmin.Call(context.Background(), fbp.In,
			users.UserInput{Action: users.ActionCreate, Name: name, Password: "0123456789", Role: role})
		if err != nil || outport != fbp.Success {
			t.Fatalf("create %s: %s %v", name, p.Data, err)
		}
	}
	ts := httptest.NewServer(New(Config{NC: nc, Store: st, Login: start(users.Login(st)), Useradmin: useradmin,
		Logger: quiet, Wait: 200 * time.Millisecond}))
	t.Cleanup(ts.Close)
	return env{nc: nc, st: st, ts: ts}
}

// client returns an HTTP client logged in as name, or not logged in if name
// is empty.
func (e env) client(t *testing.T, name string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if name != "" {
		if code := e.post(t, c, "/api/login", users.LoginInput{Name: name, Password: "0123456789"}); code != http.StatusOK {
			t.Fatalf("login %s: %d", name, code)
		}
	}
	return c
}

func (e env) post(t *testing.T, c *http.Client, path string, v any) int {
	t.Helper()
	body, _ := json.Marshal(v)
	resp, err := c.Post(e.ts.URL+path, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (e env) get(t *testing.T, c *http.Client, path string, v any) int {
	t.Helper()
	resp, err := c.Get(e.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestAuth(t *testing.T) {
	e := setup(t)
	anon, viewer, admin := e.client(t, ""), e.client(t, "viewer"), e.client(t, "admin")
	if code := e.get(t, anon, "/api/networks", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code := e.get(t, anon, "/", nil); code != http.StatusOK {
		t.Fatalf("static: %d", code)
	}
	if code := e.post(t, anon, "/api/login", users.LoginInput{Name: "admin", Password: "wrong-password"}); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", code)
	}
	var me store.User
	if code := e.get(t, viewer, "/api/me", &me); code != http.StatusOK || me.Name != "viewer" || me.Role != store.RoleViewer {
		t.Fatalf("me: %d %+v", code, me)
	}
	if code := e.get(t, viewer, "/api/users", nil); code != http.StatusForbidden {
		t.Fatalf("viewer lists users: %d", code)
	}
	if code := e.post(t, viewer, "/api/networks/observe", ObserveRequest{Base: "fbp.a.n", On: true}); code != http.StatusForbidden {
		t.Fatalf("viewer switches trace: %d", code)
	}
	// A form post cannot reach the API.
	resp, _ := admin.Post(e.ts.URL+"/api/users", "application/x-www-form-urlencoded", strings.NewReader("action=delete&name=viewer"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", resp.StatusCode)
	}
	if code := e.post(t, admin, "/api/users", users.UserInput{Action: users.ActionCreate, Name: "x", Password: "short", Role: store.RoleViewer}); code != http.StatusBadRequest {
		t.Fatalf("invalid user: %d", code)
	}
	if code := e.post(t, admin, "/api/users", users.UserInput{Action: users.ActionDelete, Name: "viewer"}); code != http.StatusOK {
		t.Fatalf("delete viewer: %d", code)
	}
	var list []store.User
	if code := e.get(t, admin, "/api/users", &list); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("users: %d %+v", code, list)
	}
	// The deleted user's session is gone.
	if code := e.get(t, viewer, "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("deleted user: %d", code)
	}
	e.post(t, admin, "/api/logout", struct{}{})
	if code := e.get(t, admin, "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", code)
	}
}

func TestNetworksAndStreams(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	net, err := text.Pipeline()
	if err != nil {
		t.Fatal(err)
	}
	s, err := service.Start(ctx, e.nc, text.PackagePath, net, service.WithLogger(quiet))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop() })
	for i := range 3 {
		e.st.InsertLog(ctx, s.Base(), service.LogData{Service: s.ID(), Seq: uint64(i + 1), Time: time.Now(), Level: "INFO", Message: "m"})
	}
	e.st.InsertEvent(ctx, "fbp.old.net", service.EventData{Service: "gone", Kind: "Sent", Time: time.Now()})
	admin := e.client(t, "admin")

	var list []Network
	if code := e.get(t, admin, "/api/networks", &list); code != http.StatusOK || len(list) != 2 {
		t.Fatalf("networks: %d %+v", code, list)
	}
	if list[0].Base != s.Base() || !list[0].Running || len(list[0].Services) != 1 ||
		list[1].Base != "fbp.old.net" || list[1].Running || list[1].Name != "net" {
		t.Fatalf("networks %+v", list)
	}
	if code := e.post(t, admin, "/api/networks/observe", ObserveRequest{Base: "fbp.old.net", On: true}); code != http.StatusNotFound {
		t.Fatalf("observe stopped network: %d", code)
	}
	if code := e.post(t, admin, "/api/networks/observe", ObserveRequest{Base: s.Base(), On: true}); code != http.StatusNoContent {
		t.Fatalf("observe: %d", code)
	}

	// Stream: the stored rows, then a new one.
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, e.ts.URL+"/api/networks/logs?base="+s.Base(), nil)
	resp, err := admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewReader(resp.Body)
	next := func() store.Log {
		t.Helper()
		for {
			line, err := lines.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v", err)
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var l store.Log
				if err := json.Unmarshal([]byte(data), &l); err != nil {
					t.Fatal(err)
				}
				return l
			}
		}
	}
	var last store.Log
	for i := range 3 {
		if last = next(); last.Seq != uint64(i+1) {
			t.Fatalf("stored row %d: %+v", i, last)
		}
	}
	e.st.InsertLog(ctx, s.Base(), service.LogData{Service: s.ID(), Seq: 4, Time: time.Now(), Level: "WARN", Message: "new"})
	if l := next(); l.Seq != 4 || l.Message != "new" || l.ID <= last.ID {
		t.Fatalf("new row %+v", l)
	}

	var older []store.Log
	if code := e.get(t, admin, "/api/networks/logs?base="+s.Base()+"&before="+jsonID(last.ID), &older); code != http.StatusOK || len(older) != 2 {
		t.Fatalf("older: %d %+v", code, older)
	}
}

func jsonID(id int64) string { b, _ := json.Marshal(id); return string(b) }
