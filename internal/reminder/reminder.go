// Package reminder is the in-process expiry reminder job.
package reminder

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"certifire/internal/certgen"
	"certifire/internal/store"
	"certifire/internal/webhook"
)

const (
	interval    = time.Hour
	maxWait     = 60 * time.Second
	defaultWait = 5 * time.Second
	shownHosts  = 5
)

// Mark returns the dedupe mark for a cert at now: "7","5","3","2","1" at those
// days left, "expired:YYYY-MM-DD" (UTC) once expired, and false otherwise.
func Mark(notAfter, now time.Time) (string, bool) {
	if !notAfter.After(now) {
		return "expired:" + now.UTC().Format("2006-01-02"), true
	}
	switch d := certgen.DaysLeft(notAfter, now); d {
	case 7, 5, 3, 2, 1:
		return strconv.Itoa(d), true
	}
	return "", false
}

// Compose builds the reminder text for a candidate.
func Compose(c store.Candidate, now time.Time) webhook.Message {
	var title string
	expired := !c.NotAfter.After(now)
	if expired {
		switch n := -certgen.DaysLeft(c.NotAfter, now) - 1; {
		case n <= 0:
			title = fmt.Sprintf("Certificate `%s` expired today", c.Name)
		default:
			title = fmt.Sprintf("Certificate `%s` expired %d %s ago", c.Name, n, plural(n))
		}
	} else {
		n := certgen.DaysLeft(c.NotAfter, now)
		title = fmt.Sprintf("Certificate `%s` expires in %d %s", c.Name, n, plural(n))
	}
	hosts := c.Hosts
	more := 0
	if len(hosts) > shownHosts {
		hosts, more = hosts[:shownHosts], len(hosts)-shownHosts
	}
	quoted := make([]string, len(hosts))
	for i, h := range hosts {
		quoted[i] = "`" + h + "`"
	}
	list := strings.Join(quoted, ", ")
	if more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}
	verb := "expires"
	if expired {
		verb = "expired"
	}
	return webhook.Message{
		Title:   title,
		Text:    fmt.Sprintf("Hosts: %s. It %s on %s (UTC).", list, verb, c.NotAfter.UTC().Format("2006-01-02")),
		Expired: expired,
	}
}

func plural(n int) string {
	if n == 1 {
		return "day"
	}
	return "days"
}

// Job sends reminders.
type Job struct {
	Store  *store.Store
	Sender *webhook.Sender
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration)
}

// New returns a Job using the wall clock.
func New(st *store.Store, sender *webhook.Sender) *Job {
	return &Job{Store: st, Sender: sender, Now: time.Now, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Run ticks once now and then hourly until ctx ends. The hourly tick plus the
// dedupe table gives one reminder per day without missing a day over restarts.
func (j *Job) Run(ctx context.Context) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := j.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Error("reminder tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type target struct {
	user int64
	kind string
}

// Tick sends every reminder that is due and not yet sent.
func (j *Job) Tick(ctx context.Context) error {
	now := j.Now()
	cands, err := j.Store.ReminderCandidates(ctx, now)
	if err != nil {
		return err
	}
	skip := map[target]bool{} // webhooks that failed this tick
	for _, c := range cands {
		if ctx.Err() != nil {
			return nil
		}
		tg := target{c.UserID, c.Kind}
		mark, ok := Mark(c.NotAfter, now)
		if !ok || skip[tg] {
			continue
		}
		claimed, err := j.Store.ClaimReminder(ctx, c.CertID, c.Kind, mark)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if !j.deliver(ctx, c, mark, now) {
			skip[tg] = true
		}
	}
	return nil
}

// deliver sends one claimed reminder; it reports whether the webhook is still usable.
func (j *Job) deliver(ctx context.Context, c store.Candidate, mark string, now time.Time) bool {
	kind := webhook.Kind(c.Kind)
	msg := Compose(c, now)
	res := j.Sender.Send(ctx, kind, c.URL, msg)
	if res.Outcome == webhook.RateLimited && res.RetryAfter <= maxWait && ctx.Err() == nil {
		wait := res.RetryAfter
		if wait == 0 {
			wait = defaultWait
		}
		j.Sleep(ctx, wait)
		res = j.Sender.Send(ctx, kind, c.URL, msg)
	}
	bg := context.WithoutCancel(ctx)
	switch {
	case res.OK():
		slog.Info("reminder sent", "kind", c.Kind, "mark", mark, "cert", c.CertID)
		return true
	case res.Outcome == webhook.Broken:
		// Keep the claim: a permanent failure must not be retried.
		slog.Warn("webhook broken", "kind", c.Kind, "target", webhook.Redact(c.URL), "reason", res.Message)
		if err := j.Store.MarkWebhookBroken(bg, c.UserID, c.Kind, res.Message); err != nil {
			slog.Error("mark webhook broken", "err", err)
		}
		return false
	}
	// Rate limited beyond the cap, 5xx or network error: retry next tick.
	slog.Warn("reminder delayed", "kind", c.Kind, "target", webhook.Redact(c.URL), "outcome", res.Outcome)
	if err := j.Store.UnclaimReminder(bg, c.CertID, c.Kind, mark); err != nil {
		slog.Error("release reminder claim", "err", err)
	}
	return false
}
