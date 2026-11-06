// Package storeweb serves the browser interface of gofbpstore: the same
// pages as gofbpsrvls, with login, Events and log records read from the
// store, and an admin area for users. Writing actions go through the
// Networks of package users.
package storeweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"gofbpsrvls/pkg/discover"
	"gofbpsrvls/pkg/flow"
	"gofbpsrvls/pkg/store"
	"gofbpsrvls/pkg/users"
	"gofbpsrvls/pkg/webui"

	"github.com/555nase89/goFBP/pkg/fbp"
	"github.com/555nase89/goFBP/pkg/service"

	"github.com/nats-io/nats.go"
)

// CookieName is the name of the session cookie.
const CookieName = "gofbpstore_session"

// Defaults of a Server.
const (
	// PageSize is how many rows a stream starts with and an older page has.
	PageSize = 500
	// PollInterval is how often a stream looks for new rows in the store.
	PollInterval = time.Second
	natsTimeout  = 2 * time.Second
)

// Config holds what a Server needs.
type Config struct {
	NC        *nats.Conn
	Store     *store.Store
	Login     *flow.Runner // Network users.Login
	Useradmin *flow.Runner // Network users.Useradmin
	Logger    *slog.Logger
	// CookieSecure marks the session cookie Secure; set it when the server
	// is reached via HTTPS.
	CookieSecure bool
	// Wait is how long a discovery collects replies (discover.DefaultWait if 0).
	Wait time.Duration
}

// Server is the HTTP handler of gofbpstore.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

type ctxKey struct{}

// New returns a Server.
func New(cfg Config) *Server {
	if cfg.Wait == 0 {
		cfg.Wait = discover.DefaultWait
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.mux.Handle("GET /", http.FileServerFS(webui.Files()))
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/logout", s.logout)
	s.mux.HandleFunc("GET /api/me", s.auth("", s.me))
	s.mux.HandleFunc("GET /api/networks", s.auth("", s.networks))
	s.mux.HandleFunc("POST /api/networks/observe", s.auth(store.RoleAdmin, s.observe))
	s.mux.HandleFunc("GET /api/networks/events", s.auth("", s.events))
	s.mux.HandleFunc("GET /api/networks/logs", s.auth("", s.logs))
	s.mux.HandleFunc("GET /api/users", s.auth(store.RoleAdmin, s.listUsers))
	s.mux.HandleFunc("POST /api/users", s.auth(store.RoleAdmin, s.changeUser))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// POSTs must be JSON: a form of another site cannot send it, which
	// together with SameSite=Strict protects against CSRF.
	if r.Method == http.MethodPost {
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

// auth lets only requests with a valid session through, and with role set
// only users of that role.
func (s *Server) auth(role string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		u, err := s.cfg.Store.SessionUser(r.Context(), c.Value)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if role != "" && u.Role != role {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func user(r *http.Request) store.User { u, _ := r.Context().Value(ctxKey{}).(store.User); return u }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// login runs the Network login and sets the session cookie.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in users.LoginInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	outport, p, err := s.cfg.Login.Call(r.Context(), fbp.In, in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if outport == fbp.Error {
		msg := flow.ErrorMessage(p)
		s.cfg.Logger.Info("gofbpstore: login failed", "name", in.Name, "error", msg)
		if msg == users.ErrLogin.Error() {
			http.Error(w, msg, http.StatusUnauthorized)
		} else {
			http.Error(w, "login failed", http.StatusInternalServerError)
		}
		return
	}
	res, err := fbp.DataAs[users.LoginResult](p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: res.Token, Path: "/", Expires: res.Expires,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	s.cfg.Logger.Info("gofbpstore: login", "name", res.User.Name)
	writeJSON(w, res.User)
}

// logout ends the session, if any, and clears the cookie.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		s.cfg.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) { writeJSON(w, user(r)) }

// Network is a Network as listed by GET /api/networks. Running is false for
// a Network with recorded rows but no running Service.
type Network struct {
	discover.Network
	Running bool `json:"running"`
}

// networks lists the running Networks and those only found in the store.
func (s *Server) networks(w http.ResponseWriter, r *http.Request) {
	running, err := discover.List(r.Context(), s.cfg.NC, s.cfg.Wait)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	bases, err := s.cfg.Store.Bases(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list := make([]Network, 0, len(running)+len(bases))
	for _, n := range running {
		list = append(list, Network{Network: n, Running: true})
	}
	for _, b := range bases {
		if slices.ContainsFunc(running, func(n discover.Network) bool { return n.Base == b }) {
			continue
		}
		name := b[strings.LastIndex(b, ".")+1:]
		list = append(list, Network{Network: discover.Network{Base: b, Name: name,
			Inports: []string{}, Outports: []string{}, Services: []discover.Service{}}})
	}
	slices.SortFunc(list, func(a, b Network) int { return strings.Compare(a.Base, b.Base) })
	writeJSON(w, list)
}

// ObserveRequest is the body of POST /api/networks/observe.
type ObserveRequest struct {
	Base string `json:"base"`
	On   bool   `json:"on"`
}

// observe switches observing for all Services of a running Network.
func (s *Server) observe(w http.ResponseWriter, r *http.Request) {
	var req ObserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	running, err := discover.List(r.Context(), s.cfg.NC, s.cfg.Wait)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !slices.ContainsFunc(running, func(n discover.Network) bool { return n.Base == req.Base }) {
		http.Error(w, fmt.Sprintf("no running network with base %q", req.Base), http.StatusNotFound)
		return
	}
	data, _ := json.Marshal(req.On)
	err = s.cfg.NC.Publish(service.ObserveSubject(req.Base), data)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), natsTimeout)
		err = s.cfg.NC.FlushWithContext(ctx)
		cancel()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.cfg.Logger.Info("gofbpstore: observing switched", "base", req.Base, "on", req.On, "by", user(r).Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	serveRows(s, w, r, s.cfg.Store.Events, func(e store.Event) int64 { return e.ID })
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	serveRows(s, w, r, s.cfg.Store.Logs, func(l store.Log) int64 { return l.ID })
}

// serveRows answers ?base=…&before=<id> with an older page as JSON and
// ?base=… alone with a Server-Sent Events stream: the newest PageSize rows,
// then every new row, until the browser goes away.
func serveRows[T any](s *Server, w http.ResponseWriter, r *http.Request,
	query func(context.Context, store.Page) ([]T, error), id func(T) int64) {
	base := r.URL.Query().Get("base")
	if base == "" {
		http.Error(w, "base is required", http.StatusBadRequest)
		return
	}
	if b := r.URL.Query().Get("before"); b != "" {
		before, err := strconv.ParseInt(b, 10, 64)
		if err != nil || before <= 0 {
			http.Error(w, "invalid before", http.StatusBadRequest)
			return
		}
		rows, err := query(r.Context(), store.Page{Base: base, Before: before, Limit: PageSize})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, rows)
		return
	}
	rows, err := query(r.Context(), store.Page{Base: base, Limit: PageSize})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": watching "+base+"\n\n")
	var last int64
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		for _, row := range rows {
			data, _ := json.Marshal(row)
			fmt.Fprintf(w, "data: %s\n\n", data)
			last = id(row)
		}
		if rc.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
		if rows, err = query(r.Context(), store.Page{Base: base, After: last, Limit: PageSize}); err != nil {
			if r.Context().Err() == nil {
				s.cfg.Logger.Warn("gofbpstore: stream query failed", "base", base, "error", err)
			}
			return
		}
	}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.cfg.Store.Users(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

// changeUser runs the Network useradmin with the UserInput of the body.
func (s *Server) changeUser(w http.ResponseWriter, r *http.Request) {
	var in users.UserInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	in.Hash = ""
	outport, p, err := s.cfg.Useradmin.Call(r.Context(), fbp.In, in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if outport == fbp.Error {
		http.Error(w, flow.ErrorMessage(p), http.StatusBadRequest)
		return
	}
	s.cfg.Logger.Info("gofbpstore: user changed", "action", in.Action, "name", in.Name, "role", in.Role, "by", user(r).Name)
	w.Header().Set("Content-Type", "application/json")
	w.Write(p.Data)
}
