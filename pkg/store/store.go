// Package store keeps the recorded Events and log records of goFBP Services
// and the users and sessions of gofbpstore in one SQLite file.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/555nase89/goFBP/pkg/service"

	_ "modernc.org/sqlite"
)

// Roles of a user.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// ErrNotFound is returned when a user or session does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrLastAdmin is returned when a change would leave no user with RoleAdmin.
var ErrLastAdmin = errors.New("the last admin cannot be removed or demoted")

// The IDs of events and logs use AUTOINCREMENT, so they are never reused,
// not even after pruning has emptied a table: streams ask for id > last.
const eventsSchema = `
CREATE TABLE IF NOT EXISTS events (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	base      TEXT NOT NULL,
	service   TEXT NOT NULL,
	kind      TEXT NOT NULL,
	time      INTEGER NOT NULL,
	run_id    TEXT NOT NULL,
	packet_id INTEGER NOT NULL,
	parent_id INTEGER NOT NULL,
	network   TEXT NOT NULL,
	node      TEXT NOT NULL,
	port      TEXT NOT NULL,
	copy      INTEGER NOT NULL,
	data      TEXT,
	error     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_base ON events (base, id);
CREATE INDEX IF NOT EXISTS events_time ON events (time);
`

const logsSchema = `
CREATE TABLE IF NOT EXISTS logs (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	base      TEXT NOT NULL,
	service   TEXT NOT NULL,
	seq       INTEGER NOT NULL,
	time      INTEGER NOT NULL,
	level     TEXT NOT NULL,
	message   TEXT NOT NULL,
	attrs     TEXT,
	UNIQUE (service, seq)
);
CREATE INDEX IF NOT EXISTS logs_base ON logs (base, id);
CREATE INDEX IF NOT EXISTS logs_time ON logs (time);
`

const usersSchema = `
CREATE TABLE IF NOT EXISTS users (
	id      INTEGER PRIMARY KEY,
	name    TEXT NOT NULL UNIQUE,
	pw_hash TEXT NOT NULL,
	role    TEXT NOT NULL,
	created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	expires    INTEGER NOT NULL
);
`

// Store is an open database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens or creates the database file at path.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(eventsSchema + logsSchema + usersSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}
	for table, schema := range map[string]string{"events": eventsSchema, "logs": logsSchema} {
		if err := autoincrement(db, table, schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: migrate %s: %w", table, err)
		}
	}
	return &Store{db: db}, nil
}

// autoincrement rebuilds table with schema if it was created before its IDs
// used AUTOINCREMENT, keeping all rows and IDs.
func autoincrement(db *sql.DB, table, schema string) error {
	var def string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&def); err != nil {
		return err
	}
	if strings.Contains(strings.ToUpper(def), "AUTOINCREMENT") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The indexes stay with the renamed table until it is dropped, so schema
	// runs again afterwards to create them on the new one.
	if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %[1]s RENAME TO %[1]s_old;
		%[2]s
		INSERT INTO %[1]s SELECT * FROM %[1]s_old;
		DROP TABLE %[1]s_old;
		%[2]s`, table, schema)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Event is a recorded Event of a Network.
type Event struct {
	ID   int64  `json:"id"`
	Base string `json:"base"`
	service.EventData
}

// Log is a recorded log record of a Service.
type Log struct {
	ID   int64  `json:"id"`
	Base string `json:"base"`
	service.LogData
}

// InsertEvent records e of the Network with base subject base.
func (s *Store) InsertEvent(ctx context.Context, base string, e service.EventData) error {
	var data any
	if len(e.Data) > 0 {
		data = string(e.Data)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO events
		(base, service, kind, time, run_id, packet_id, parent_id, network, node, port, copy, data, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		base, e.Service, e.Kind, e.Time.UnixNano(), e.RunID, int64(e.PacketID), int64(e.ParentID),
		e.Network, e.Node, e.Port, e.Copy, data, e.Error)
	return err
}

// InsertLog records d of a Service of the Network with base subject base. A
// record already stored (same Service and Seq) is ignored, so the Log
// history can be recorded again without duplicates.
func (s *Store) InsertLog(ctx context.Context, base string, d service.LogData) error {
	var attrs any
	if len(d.Attrs) > 0 {
		b, err := json.Marshal(d.Attrs)
		if err != nil {
			return err
		}
		attrs = string(b)
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO logs
		(base, service, seq, time, level, message, attrs) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		base, d.Service, int64(d.Seq), d.Time.UnixNano(), d.Level, d.Message, attrs)
	return err
}

// Page selects rows of one Network: with After > 0 the rows after that ID,
// oldest first; otherwise the Limit newest rows before Before (all if 0),
// also returned oldest first.
type Page struct {
	Base   string
	After  int64
	Before int64
	Limit  int
}

func (p Page) query(table, cols string) (string, []any) {
	limit := p.Limit
	if limit <= 0 {
		limit = 500
	}
	if p.After > 0 {
		return fmt.Sprintf(`SELECT %s FROM %s WHERE base = ? AND id > ? ORDER BY id LIMIT ?`, cols, table),
			[]any{p.Base, p.After, limit}
	}
	before := p.Before
	if before <= 0 {
		before = 1<<63 - 1
	}
	return fmt.Sprintf(`SELECT * FROM (SELECT %s FROM %s WHERE base = ? AND id < ? ORDER BY id DESC LIMIT ?) ORDER BY id`, cols, table),
		[]any{p.Base, before, limit}
}

// Events returns the recorded Events selected by p.
func (s *Store) Events(ctx context.Context, p Page) ([]Event, error) {
	q, args := p.query("events", "id, base, service, kind, time, run_id, packet_id, parent_id, network, node, port, copy, data, error")
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		var t, packet, parent int64
		var data sql.NullString
		if err := rows.Scan(&e.ID, &e.Base, &e.Service, &e.Kind, &t, &e.RunID, &packet, &parent,
			&e.Network, &e.Node, &e.Port, &e.Copy, &data, &e.EventData.Error); err != nil {
			return nil, err
		}
		e.Time = time.Unix(0, t)
		e.PacketID, e.ParentID = uint64(packet), uint64(parent)
		if data.Valid {
			e.Data = json.RawMessage(data.String)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// Logs returns the recorded log records selected by p.
func (s *Store) Logs(ctx context.Context, p Page) ([]Log, error) {
	q, args := p.query("logs", "id, base, service, seq, time, level, message, attrs")
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	logs := []Log{}
	for rows.Next() {
		var l Log
		var t, seq int64
		var attrs sql.NullString
		if err := rows.Scan(&l.ID, &l.Base, &l.Service, &seq, &t, &l.Level, &l.Message, &attrs); err != nil {
			return nil, err
		}
		l.Seq, l.Time = uint64(seq), time.Unix(0, t)
		if attrs.Valid {
			if err := json.Unmarshal([]byte(attrs.String), &l.Attrs); err != nil {
				return nil, err
			}
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// Bases returns the base subjects of all Networks with recorded rows.
func (s *Store) Bases(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT base FROM logs UNION SELECT base FROM events ORDER BY base`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bases []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		bases = append(bases, b)
	}
	return bases, rows.Err()
}

// PruneEvents deletes the Events older than before and returns how many.
func (s *Store) PruneEvents(ctx context.Context, before time.Time) (int64, error) {
	return s.exec(ctx, `DELETE FROM events WHERE time < ?`, before.UnixNano())
}

// PruneLogs deletes the log records older than before and returns how many.
func (s *Store) PruneLogs(ctx context.Context, before time.Time) (int64, error) {
	return s.exec(ctx, `DELETE FROM logs WHERE time < ?`, before.UnixNano())
}

// PruneSessions deletes the sessions expired before t and returns how many.
func (s *Store) PruneSessions(ctx context.Context, t time.Time) (int64, error) {
	return s.exec(ctx, `DELETE FROM sessions WHERE expires < ?`, t.UnixNano())
}

func (s *Store) exec(ctx context.Context, q string, args ...any) (int64, error) {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// User is a user of gofbpstore. PwHash is never sent to a browser.
type User struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Role    string    `json:"role"`
	Created time.Time `json:"created"`
	PwHash  string    `json:"-"`
}

// User returns the user called name.
func (s *Store) User(ctx context.Context, name string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT id, name, role, created, pw_hash FROM users WHERE name = ?`, name))
}

func (s *Store) scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created int64
	err := row.Scan(&u.ID, &u.Name, &u.Role, &created, &u.PwHash)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.Created = time.Unix(0, created)
	return u, err
}

// Users returns all users, sorted by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, role, created, pw_hash FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CountAdmins returns the number of users with RoleAdmin.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = ?`, RoleAdmin).Scan(&n)
	return n, err
}

// InsertUser creates a user.
func (s *Store) InsertUser(ctx context.Context, name, pwHash, role string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (name, pw_hash, role, created) VALUES (?, ?, ?, ?)`,
		name, pwHash, role, time.Now().UnixNano())
	return err
}

// keepAdmin is the condition of a statement that demotes or deletes a user:
// it holds unless that user is the last admin. Being part of the statement,
// the check is atomic with the change.
const keepAdmin = `(role != '` + RoleAdmin + `' OR (SELECT count(*) FROM users WHERE role = '` + RoleAdmin + `') > 1)`

// UpdateUser sets the role and, unless pwHash is empty, the password hash of
// the user called name. A new password ends all sessions of the user.
// Demoting the last admin fails with ErrLastAdmin.
func (s *Store) UpdateUser(ctx context.Context, name, pwHash, role string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, pw_hash = CASE WHEN ? = '' THEN pw_hash ELSE ? END
		WHERE name = ? AND (? = '`+RoleAdmin+`' OR `+keepAdmin+`)`,
		role, pwHash, pwHash, name, role)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return unchanged(ctx, tx, name)
	}
	if pwHash != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE name = ?)`, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteUser deletes the user called name and its sessions. Deleting the
// last admin fails with ErrLastAdmin.
func (s *Store) DeleteUser(ctx context.Context, name string) error {
	n, err := s.exec(ctx, `DELETE FROM users WHERE name = ? AND `+keepAdmin, name)
	if err == nil && n == 0 {
		err = unchanged(ctx, s.db, name)
	}
	return err
}

// unchanged tells why a change of the user called name affected no row:
// ErrNotFound if there is no such user, else ErrLastAdmin.
func unchanged(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE name = ?`, name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return ErrLastAdmin
}

// HashToken returns the form of a session token stored in the database, so
// a copy of the database file does not contain usable tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// InsertSession stores a session of the user with ID userID.
func (s *Store) InsertSession(ctx context.Context, token string, userID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires) VALUES (?, ?, ?)`,
		HashToken(token), userID, expires.UnixNano())
	return err
}

// SessionUser returns the user of an unexpired session.
func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT u.id, u.name, u.role, u.created, u.pw_hash
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ? AND s.expires > ?`,
		HashToken(token), time.Now().UnixNano()))
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	return err
}
