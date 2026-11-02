package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"certifire/internal/reminder"
	"certifire/internal/store"
	"certifire/internal/webhook"
)

// remind implements `app remind`: one reminder tick, then exit. For a
// scheduler (a Kubernetes CronJob) when the in-app one cannot be relied on.
// It exits non-zero on a config, database or tick error, or when any send was
// delayed (5xx, network, 429 over the cap), so the Job's retry runs the same
// day; sent reminders are not repeated. A permanent 4xx marks the webhook
// broken and still exits 0.
func remind(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: app remind")
		return 2
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cmp.Or(os.Getenv("LOG_LEVEL"), "info"))); err != nil {
		fmt.Fprintln(os.Stderr, "error: LOG_LEVEL:", err)
		return 1
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	rcfg, err := reminder.LoadConfig(os.Getenv)
	if err != nil {
		slog.Error("remind", "err", err)
		return 1
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("remind", "err", "DATABASE_URL is not set")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runRemind(ctx, dbURL, rcfg, webhook.NewSender())
}

func runRemind(ctx context.Context, dbURL string, rcfg reminder.Config, sender *webhook.Sender) int {
	octx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := store.Open(octx, dbURL) // no retry loop: fail fast, the Job retries
	cancel()
	if err != nil {
		slog.Error("remind: cannot reach the database", "err", err)
		return 1
	}
	defer st.Close()
	return exitCode(ctx, reminder.New(st, sender, rcfg))
}

// exitCode runs one tick and maps its result to the process exit code.
func exitCode(ctx context.Context, job *reminder.Job) int {
	rep, err := job.Tick(ctx)
	switch {
	case err != nil:
		slog.Error("remind: tick failed", "err", err)
		return 1
	case ctx.Err() != nil:
		slog.Error("remind: interrupted")
		return 1
	case rep.Delayed > 0:
		slog.Error("remind: sends delayed, retry later", "delayed", rep.Delayed)
		return 1
	}
	return 0
}
