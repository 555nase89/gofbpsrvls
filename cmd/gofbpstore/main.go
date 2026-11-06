// Command gofbpstore records the log records and traced Events of the goFBP
// Services on NATS in a SQLite file and serves them in the browser, with
// login and user administration.
//
//	gofbpstore -config config.json                     # run the server
//	gofbpstore -config config.json -adduser anna -role admin < password.txt
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gofbpsrvls/pkg/flow"
	"gofbpsrvls/pkg/record"
	"gofbpsrvls/pkg/store"
	"gofbpsrvls/pkg/storeweb"
	"gofbpsrvls/pkg/users"

	"github.com/555nase89/goFBP/pkg/fbp"
	"github.com/555nase89/goFBP/pkg/natscon"
)

type config struct {
	NatsCon natscon.Config `json:"natscon"`
	// Listen is the HTTP address, default 127.0.0.1:8090.
	Listen string `json:"listen"`
	// DB is the path of the SQLite file, default gofbpstore.db.
	DB string `json:"db"`
	// Retention is how long Events and log records are kept, default 720h.
	Retention    string `json:"retention"`
	CookieSecure bool   `json:"cookie_secure"`
}

func main() {
	path := flag.String("config", "config.json", "path to the config file")
	addUser := flag.String("adduser", "", "create this user with the password read from stdin, then exit")
	role := flag.String("role", store.RoleViewer, "role of -adduser: admin or viewer")
	flag.Parse()
	err := run(*path, *addUser, *role)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gofbpstore:", err)
		os.Exit(1)
	}
}

func run(path, addUser, role string) error {
	cfg := config{Listen: "127.0.0.1:8090", DB: "gofbpstore.db", Retention: "720h"}
	if err := natscon.ReadFile(path, &cfg); err != nil {
		return err
	}
	retention, err := time.ParseDuration(cfg.Retention)
	if err != nil {
		return fmt.Errorf("retention: %w", err)
	}
	st, err := store.Open(cfg.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.Default()
	useradmin, err := startRunner(ctx, logger)(users.Useradmin(st))
	if err != nil {
		return err
	}
	defer useradmin.Stop()
	if addUser != "" {
		return createUser(ctx, useradmin, addUser, role)
	}

	login, err := startRunner(ctx, logger)(users.Login(st))
	if err != nil {
		return err
	}
	defer login.Stop()
	rec, err := startRunner(ctx, logger)(record.Record(st))
	if err != nil {
		return err
	}
	defer rec.Stop()
	prune, err := startRunner(ctx, logger)(record.Prune(st))
	if err != nil {
		return err
	}
	defer prune.Stop()

	nc, err := natscon.Connect(cfg.NatsCon, "gofbpstore")
	if err != nil {
		return err
	}
	defer nc.Close()
	go record.NewCollector(nc, rec, logger).Run(ctx, record.DefaultInterval)
	go pruneEvery(ctx, prune, retention, time.Hour, logger)

	srv := &http.Server{Addr: cfg.Listen,
		Handler: storeweb.New(storeweb.Config{NC: nc, Store: st, Login: login, Useradmin: useradmin,
			Logger: logger, CookieSecure: cfg.CookieSecure}),
		// Request contexts end with ctx, so open streams do not hold up the shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	logger.Info("gofbpstore: listening", "url", "http://"+cfg.Listen, "db", cfg.DB)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// startRunner returns a function starting a flow.Runner for a Network
// constructor's results.
func startRunner(ctx context.Context, logger *slog.Logger) func(*fbp.Network, error) (*flow.Runner, error) {
	return func(net *fbp.Network, err error) (*flow.Runner, error) {
		if err != nil {
			return nil, err
		}
		return flow.Start(ctx, net, logger)
	}
}

// createUser runs the Network useradmin for -adduser.
func createUser(ctx context.Context, useradmin *flow.Runner, name, role string) error {
	fmt.Fprintf(os.Stderr, "password for %s: ", name)
	pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && pw == "" {
		return fmt.Errorf("read password: %w", err)
	}
	outport, p, err := useradmin.Call(ctx, fbp.In, users.UserInput{Action: users.ActionCreate, Name: name,
		Password: strings.TrimRight(pw, "\r\n"), Role: role})
	if err != nil {
		return err
	}
	if outport == fbp.Error {
		return errors.New(flow.ErrorMessage(p))
	}
	fmt.Fprintf(os.Stderr, "\nuser %s created with role %s\n", name, role)
	return nil
}

// pruneEvery sends the Network prune the retention limit at start and then
// every interval.
func pruneEvery(ctx context.Context, prune *flow.Runner, retention, interval time.Duration, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := prune.Send(ctx, fbp.In, record.PruneInput{Before: time.Now().Add(-retention)}); err != nil && ctx.Err() == nil {
			logger.Warn("gofbpstore: prune failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
