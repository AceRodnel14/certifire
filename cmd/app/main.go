// Command app serves the CertiFire API and the embedded web UI. Plain HTTP
// only: a reverse proxy terminates TLS.
package main

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"certifire/internal/reminder"
	"certifire/internal/server"
	"certifire/internal/store"
)

// static is the embedded web UI; nil for a backend-only build (web.go sets it).
var static fs.FS

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "reset-password":
			os.Exit(resetPassword(os.Args[2:]))
		}
	}
	if err := run(); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := server.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := server.New(cfg, static)
	srv := &http.Server{Addr: ":" + cfg.Port, Handler: app, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "base_url", cfg.BaseURL)
		errc <- srv.ListenAndServe()
	}()

	// The server is up before the database: /healthz answers while we retry.
	dbDone := make(chan *store.Store, 1)
	go func() { dbDone <- connect(ctx, cfg.DatabaseURL, app) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if st := <-dbDone; st != nil { // jobs stop with ctx; wait for the pool owner
		st.Close()
	}
	return nil
}

// connect retries the database with backoff, then attaches it to the server
// and starts the background jobs. It returns the store (nil if ctx ended first)
// after ctx is cancelled.
func connect(ctx context.Context, url string, app *server.Server) *store.Store {
	wait := time.Second
	for {
		var st *store.Store
		err := errors.New("DATABASE_URL is not set")
		if url != "" {
			octx, cancel := context.WithTimeout(ctx, 10*time.Second)
			st, err = store.Open(octx, url)
			cancel()
		}
		if err == nil {
			slog.Info("database ready")
			app.SetStore(st)
			go app.ReapSessions(ctx)
			job := reminder.New(st, app.Sender())
			job.Run(ctx) // blocks until ctx ends
			return st
		}
		slog.Warn("database not ready", "err", err, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

// healthcheck backs the image's HEALTHCHECK.
func healthcheck() int {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + cmp.Or(os.Getenv("PORT"), "8080") + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
