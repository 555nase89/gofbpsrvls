package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/555nase89/goFBP/pkg/service"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Characters with a meaning in an SQLite URI must reach the file system as
// they are.
func TestOpenSpecialPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a?b#c%41")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x?y.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.InsertUser(context.Background(), "anna", "h", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not at %s: %v", path, err)
	}
}

func TestEventsPaging(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	for i := range 5 {
		e := service.EventData{Service: "s1", Kind: "Sent", Time: now.Add(time.Duration(i) * time.Second), RunID: "r",
			PacketID: uint64(i + 1), Network: "n", Port: "In", Data: json.RawMessage(`"x"`)}
		if err := s.InsertEvent(ctx, "fbp.a.n", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertEvent(ctx, "fbp.other.n", service.EventData{Service: "s2", Kind: "Sent", Time: now}); err != nil {
		t.Fatal(err)
	}
	last, err := s.Events(ctx, Page{Base: "fbp.a.n", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 2 || last[0].PacketID != 4 || last[1].PacketID != 5 || string(last[1].Data) != `"x"` ||
		!last[1].Time.Equal(now.Add(4*time.Second)) {
		t.Fatalf("newest: %+v", last)
	}
	older, _ := s.Events(ctx, Page{Base: "fbp.a.n", Before: last[0].ID, Limit: 10})
	if len(older) != 3 || older[0].PacketID != 1 {
		t.Fatalf("older: %+v", older)
	}
	after, _ := s.Events(ctx, Page{Base: "fbp.a.n", After: older[2].ID})
	if len(after) != 2 || after[0].ID != last[0].ID {
		t.Fatalf("after: %+v", after)
	}
	bases, _ := s.Bases(ctx)
	if len(bases) != 2 {
		t.Fatalf("bases %v", bases)
	}
	n, err := s.PruneEvents(ctx, now.Add(2*time.Second))
	if err != nil || n != 3 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func TestLogsNoDuplicates(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	d := service.LogData{Service: "s1", Seq: 1, Time: time.Now(), Level: "INFO", Message: "hi", Attrs: map[string]any{"n": 1.0}}
	for range 2 {
		if err := s.InsertLog(ctx, "fbp.a.n", d); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := s.Logs(ctx, Page{Base: "fbp.a.n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Message != "hi" || logs[0].Attrs["n"] != 1.0 || logs[0].Base != "fbp.a.n" {
		t.Fatalf("got %+v", logs)
	}
}

func TestUsersAndSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.InsertUser(ctx, "anna", "h1", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertUser(ctx, "anna", "h2", RoleViewer); err == nil {
		t.Fatal("duplicate name accepted")
	}
	// A second admin, so anna can be demoted.
	if err := s.InsertUser(ctx, "bert", "h4", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	u, err := s.User(ctx, "anna")
	if err != nil || u.PwHash != "h1" || u.Role != RoleAdmin {
		t.Fatalf("got %+v, %v", u, err)
	}
	if err := s.InsertSession(ctx, "tok", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if su, err := s.SessionUser(ctx, "tok"); err != nil || su.Name != "anna" {
		t.Fatalf("session: %+v, %v", su, err)
	}
	// A role change keeps the session, a new password ends it.
	if err := s.UpdateUser(ctx, "anna", "", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "tok"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUser(ctx, "anna", "h3", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "tok"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session after password change: %v", err)
	}
	// SetPassword ends the sessions too, but keeps the role.
	s.InsertSession(ctx, "tok2", u.ID, time.Now().Add(time.Hour))
	if err := s.SetPassword(ctx, "anna", "h5"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "tok2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session after SetPassword: %v", err)
	}
	if u, _ := s.User(ctx, "anna"); u.PwHash != "h5" || u.Role != RoleViewer {
		t.Fatalf("after SetPassword: %+v", u)
	}
	if err := s.SetPassword(ctx, "nobody", "h6"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPassword of unknown user: %v", err)
	}
	s.InsertSession(ctx, "old", u.ID, time.Now().Add(-time.Minute))
	if _, err := s.SessionUser(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session accepted")
	}
	if n, _ := s.PruneSessions(ctx, time.Now()); n != 1 {
		t.Fatalf("pruned %d sessions", n)
	}
	if err := s.DeleteUser(ctx, "anna"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "anna"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestLastAdmin(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	s.InsertUser(ctx, "anna", "h", RoleAdmin)
	s.InsertUser(ctx, "vera", "h", RoleViewer)
	if err := s.UpdateUser(ctx, "anna", "", RoleViewer); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
	if err := s.DeleteUser(ctx, "anna"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete last admin: %v", err)
	}
	if err := s.UpdateUser(ctx, "nobody", "", RoleViewer); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}
	// Viewers and the last admin keeping its role are not affected.
	if err := s.UpdateUser(ctx, "anna", "h2", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "vera"); err != nil {
		t.Fatal(err)
	}

	// Two admins demoted at the same time: one change must fail.
	s.InsertUser(ctx, "bert", "h", RoleAdmin)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"anna", "bert"} {
		wg.Go(func() { errs[i] = s.UpdateUser(ctx, name, "", RoleViewer) })
	}
	wg.Wait()
	if n, _ := s.CountAdmins(ctx); n != 1 {
		t.Fatalf("%d admins left, errors %v", n, errs)
	}
}

// After pruning has emptied a table, new rows must get higher IDs than the
// deleted ones, or a stream asking for id > last would miss them.
func TestIDsNotReused(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	s.InsertEvent(ctx, "fbp.a.n", service.EventData{Service: "s", Kind: "Sent", Time: old})
	s.InsertLog(ctx, "fbp.a.n", service.LogData{Service: "s", Seq: 1, Time: old})
	events, _ := s.Events(ctx, Page{Base: "fbp.a.n"})
	logs, _ := s.Logs(ctx, Page{Base: "fbp.a.n"})
	s.PruneEvents(ctx, time.Now())
	s.PruneLogs(ctx, time.Now())
	s.InsertEvent(ctx, "fbp.a.n", service.EventData{Service: "s", Kind: "Sent", Time: time.Now()})
	s.InsertLog(ctx, "fbp.a.n", service.LogData{Service: "s", Seq: 2, Time: time.Now()})
	if e, _ := s.Events(ctx, Page{Base: "fbp.a.n", After: events[0].ID}); len(e) != 1 {
		t.Fatalf("event after prune not found: %+v", e)
	}
	if l, _ := s.Logs(ctx, Page{Base: "fbp.a.n", After: logs[0].ID}); len(l) != 1 {
		t.Fatalf("log after prune not found: %+v", l)
	}
}

// A database created before AUTOINCREMENT is rebuilt by Open, keeping its
// rows, IDs and indexes.
func TestMigrateAutoincrement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.ReplaceAll(eventsSchema+logsSchema, " AUTOINCREMENT", "")
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (id, base, service, kind, time, run_id, packet_id, parent_id, network, node, port, copy, error)
		VALUES (7, 'fbp.a.n', 's', 'Sent', 1, 'r', 0, 0, 'n', '', '', 0, '');
		INSERT INTO logs (id, base, service, seq, time, level, message) VALUES (9, 'fbp.a.n', 's', 1, 1, 'INFO', 'hi')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, table := range []string{"events", "logs"} {
		var def string
		s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = ?`, table).Scan(&def)
		if !strings.Contains(def, "AUTOINCREMENT") {
			t.Errorf("%s not migrated: %s", table, def)
		}
	}
	var indexes int
	s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('events_base', 'events_time', 'logs_base', 'logs_time')`).Scan(&indexes)
	if indexes != 4 {
		t.Errorf("%d of 4 indexes", indexes)
	}
	events, _ := s.Events(ctx, Page{Base: "fbp.a.n"})
	logs, _ := s.Logs(ctx, Page{Base: "fbp.a.n"})
	if len(events) != 1 || events[0].ID != 7 || len(logs) != 1 || logs[0].ID != 9 || logs[0].Message != "hi" {
		t.Fatalf("rows lost: %+v %+v", events, logs)
	}
	// The log UNIQUE (service, seq) is kept.
	s.InsertLog(ctx, "fbp.a.n", service.LogData{Service: "s", Seq: 1, Time: time.Now()})
	if logs, _ := s.Logs(ctx, Page{Base: "fbp.a.n"}); len(logs) != 1 {
		t.Fatalf("duplicate log after migration: %+v", logs)
	}
}
