// Package web serves the gofbpSRVLS browser interface and its JSON API. There
// is no login: the server is meant to listen on a local address only.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"gofbpsrvls/pkg/discover"
	"gofbpsrvls/pkg/trace"
	"gofbpsrvls/pkg/webui"

	"github.com/555nase89/goFBP/pkg/service"

	"github.com/nats-io/nats.go"
)

// flushTimeout limits the wait for the NATS server to confirm a publish.
const flushTimeout = 2 * time.Second

// historyTimeout limits the wait for the Log histories; all Services are
// asked at once.
const historyTimeout = time.Second

// Server is the HTTP handler of gofbpSRVLS.
type Server struct {
	nc     *nats.Conn
	hub    *trace.Hub
	wait   time.Duration
	logger *slog.Logger
	mux    *http.ServeMux
}

// New returns a Server using nc for discovery, switching and tracing. wait
// is how long a discovery collects replies (discover.DefaultWait if 0).
func New(nc *nats.Conn, wait time.Duration, logger *slog.Logger) *Server {
	if wait == 0 {
		wait = discover.DefaultWait
	}
	s := &Server{nc: nc, hub: trace.NewHub(nc), wait: wait, logger: logger, mux: http.NewServeMux()}
	s.mux.Handle("GET /", http.FileServerFS(webui.Files()))
	s.mux.HandleFunc("GET /api/networks", s.networks)
	s.mux.HandleFunc("POST /api/networks/observe", s.observe)
	s.mux.HandleFunc("GET /api/networks/events", s.events)
	s.mux.HandleFunc("GET /api/networks/logs", s.logs)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// networks answers with all goFBP Networks found on NATS.
func (s *Server) networks(w http.ResponseWriter, r *http.Request) {
	networks, err := discover.List(r.Context(), s.nc, s.wait)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(networks)
}

// ObserveRequest is the body of POST /api/networks/observe.
type ObserveRequest struct {
	Base string `json:"base"`
	On   bool   `json:"on"`
}

// observe switches observing on or off for all Services of a Network. Only
// base subjects of Networks that are running can be switched, so the API
// cannot publish on arbitrary subjects.
func (s *Server) observe(w http.ResponseWriter, r *http.Request) {
	var req ObserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.known(r.Context(), req.Base); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	data, _ := json.Marshal(req.On)
	err := s.nc.Publish(service.ObserveSubject(req.Base), data)
	if err == nil {
		// FlushWithContext needs a deadline.
		ctx, cancel := context.WithTimeout(r.Context(), flushTimeout)
		err = s.nc.FlushWithContext(ctx)
		cancel()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.logger.Info("gofbpsrvls: observing switched", "base", req.Base, "on", req.On)
	w.WriteHeader(http.StatusNoContent)
}

// events streams the Events of one Network as Server-Sent Events, each
// carrying the JSON of a service.EventData, until the browser goes away.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	if _, err := s.known(r.Context(), base); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	events, stop, err := s.hub.Watch(service.TraceSubject(base))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer stop()
	stream(w, r, base, nil, events, nil)
}

// logs streams the log records of all Services of one Network as
// Server-Sent Events, each carrying the JSON of a service.LogData: first
// the Log history of every Service, then the live records, until the
// browser goes away.
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	base := r.URL.Query().Get("base")
	n, err := s.known(r.Context(), base)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// Subscribe before asking for the history, so no record falls in between.
	records, stop, err := s.hub.Watch(service.LogSubject(base, "*"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer stop()
	histories := make([][]service.LogData, len(n.Services))
	ctx, cancel := context.WithTimeout(r.Context(), historyTimeout)
	var wg sync.WaitGroup
	for i, svc := range n.Services {
		wg.Go(func() {
			msg, err := s.nc.RequestWithContext(ctx, service.LogHistorySubject(base, svc.ID), nil)
			if err == nil {
				err = json.Unmarshal(msg.Data, &histories[i])
			}
			if err != nil {
				// E.g. a Service built with an older goFBP: live records only.
				histories[i] = nil
				s.logger.Debug("gofbpsrvls: no log history", "base", base, "service", svc.ID, "error", err)
			}
		})
	}
	wg.Wait()
	cancel()
	history := slices.Concat(histories...)
	slices.SortStableFunc(history, func(a, b service.LogData) int { return a.Time.Compare(b.Time) })
	seen := map[string]uint64{} // highest Seq sent per Service
	first := make([][]byte, 0, len(history))
	for _, d := range history {
		seen[d.Service] = max(seen[d.Service], d.Seq)
		data, _ := json.Marshal(d)
		first = append(first, data)
	}
	// Records from the history may come again live, or from the Hub's own
	// recent records.
	keep := func(data []byte) bool {
		var d struct {
			Service string `json:"service"`
			Seq     uint64 `json:"seq"`
		}
		if json.Unmarshal(data, &d) != nil || d.Seq <= seen[d.Service] {
			return false
		}
		seen[d.Service] = d.Seq
		return true
	}
	stream(w, r, base, first, records, keep)
}

// stream writes first and then every message from msgs for which keep (if
// not nil) returns true as Server-Sent Events, until the browser goes away.
func stream(w http.ResponseWriter, r *http.Request, base string, first [][]byte, msgs <-chan []byte, keep func([]byte) bool) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	// A comment line makes the browser see the stream as open at once.
	fmt.Fprint(w, ": watching "+base+"\n\n")
	for _, m := range first {
		fmt.Fprintf(w, "data: %s\n\n", m)
	}
	if rc.Flush() != nil {
		return
	}
	write := func(m []byte) {
		if keep == nil || keep(m) {
			fmt.Fprintf(w, "data: %s\n\n", m)
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-msgs:
			write(m)
			// Send what is already queued in one write.
			for more := true; more; {
				select {
				case m := <-msgs:
					write(m)
				default:
					more = false
				}
			}
			if rc.Flush() != nil {
				return
			}
		}
	}
}

// known returns the running Network with base subject base, or an error if
// there is none.
func (s *Server) known(ctx context.Context, base string) (discover.Network, error) {
	if base == "" {
		return discover.Network{}, fmt.Errorf("base is required")
	}
	networks, err := discover.List(ctx, s.nc, s.wait)
	if err != nil {
		return discover.Network{}, err
	}
	i := slices.IndexFunc(networks, func(n discover.Network) bool { return n.Base == base })
	if i < 0 {
		return discover.Network{}, fmt.Errorf("no running network with base %q", base)
	}
	return networks[i], nil
}
