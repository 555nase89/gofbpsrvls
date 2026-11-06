// Command gofbpsrvls serves a local web interface that lists the goFBP Services
// connected to NATS, switches observing per Network on and off and shows
// the traced Events live. There is no login; it listens on 127.0.0.1 unless
// configured otherwise.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gofbpsrvls/pkg/web"

	"github.com/555nase89/goFBP/pkg/natscon"
)

type config struct {
	NatsCon natscon.Config `json:"natscon"`
	// Listen is the HTTP address, default 127.0.0.1:8080.
	Listen string `json:"listen"`
}

func main() {
	path := flag.String("config", "config.json", "path to the config file")
	listen := flag.String("listen", "", "HTTP address, overrides listen in the config file")
	flag.Parse()
	if err := run(*path, *listen); err != nil {
		fmt.Fprintln(os.Stderr, "gofbpsrvls:", err)
		os.Exit(1)
	}
}

func run(path, listen string) error {
	cfg := config{Listen: "127.0.0.1:8080"}
	if err := natscon.ReadFile(path, &cfg); err != nil {
		return err
	}
	if listen != "" {
		cfg.Listen = listen
	}
	nc, err := natscon.Connect(cfg.NatsCon, "gofbpsrvls")
	if err != nil {
		return err
	}
	defer nc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Request contexts end with ctx, so open event streams do not hold up
	// the shutdown.
	srv := &http.Server{Addr: cfg.Listen, Handler: web.New(nc, 0, slog.Default()),
		BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("gofbpsrvls: listening", "url", "http://"+cfg.Listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
