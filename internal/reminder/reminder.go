// Package reminder is the in-process expiry reminder job.
package reminder

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"certifire/internal/store"
	"certifire/internal/webhook"
)

const (
	interval    = time.Hour
	maxWait     = 60 * time.Second
	defaultWait = 5 * time.Second
	shownHosts  = 5
)

// dateOf is the calendar date of t in loc as a UTC midnight, so subtracting
// two of them counts calendar days whatever DST did in between.
func dateOf(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// daysLeft is the number of calendar days in cfg.Loc from now's date to the
// expiry date: 0 on the whole expiry date, negative after it.
func daysLeft(notAfter, now time.Time, loc *time.Location) int {
	return int(dateOf(notAfter, loc).Sub(dateOf(now, loc)) / (24 * time.Hour))
}

// Mark returns the dedupe mark for a cert at now: the step value ("7", "0")
// when the days left match a configured step (mark "0" covers the whole expiry
// date), "expired:<today in cfg.Loc>" once today is after the expiry date, and
// false otherwise.
func Mark(notAfter, now time.Time, cfg Config) (string, bool) {
	d := daysLeft(notAfter, now, cfg.Loc)
	if d < 0 {
		return "expired:" + now.In(cfg.Loc).Format("2006-01-02"), true
	}
	if slices.Contains(cfg.Steps, d) {
		return strconv.Itoa(d), true
	}
	return "", false
}

// due reports whether the day's reminders may go out: now is at or after
// today's REMINDER_TIME in cfg.Loc.
func due(now time.Time, cfg Config) bool {
	l := now.In(cfg.Loc)
	return !l.Before(time.Date(l.Year(), l.Month(), l.Day(), cfg.Hour, cfg.Min, 0, 0, cfg.Loc))
}

// Compose builds the reminder text for a candidate. Dates are shown in cfg.Loc.
func Compose(c store.Candidate, now time.Time, cfg Config) webhook.Message {
	var title string
	expired := !c.NotAfter.After(now)
	switch d := daysLeft(c.NotAfter, now, cfg.Loc); {
	case d < 0:
		title = fmt.Sprintf("Certificate `%s` expired %d %s ago", c.Name, -d, plural(-d))
	case d == 0 && expired:
		title = fmt.Sprintf("Certificate `%s` expired today", c.Name)
	case d == 0:
		title = fmt.Sprintf("Certificate `%s` expires today", c.Name)
	default:
		title = fmt.Sprintf("Certificate `%s` expires in %d %s", c.Name, d, plural(d))
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
		Text:    fmt.Sprintf("Hosts: %s. It %s on %s (%s).", list, verb, c.NotAfter.In(cfg.Loc).Format("2006-01-02"), cfg.TZ),
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
	Cfg    Config
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration)
}

// New returns a Job using the wall clock.
func New(st *store.Store, sender *webhook.Sender, cfg Config) *Job {
	return &Job{Store: st, Sender: sender, Cfg: cfg, Now: time.Now, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// nextWake is when Run should next look: the hourly retry tick, or the next
// REMINDER_TIME in cfg.Loc if that comes sooner. Without the second bound a
// REMINDER_TIME late in the evening could fall between two hourly ticks with
// less than an hour of the day left, and the day's step would be lost.
func nextWake(now time.Time, cfg Config) time.Time {
	l := now.In(cfg.Loc)
	at := time.Date(l.Year(), l.Month(), l.Day(), cfg.Hour, cfg.Min, 0, 0, cfg.Loc)
	if !at.After(now) {
		at = time.Date(l.Year(), l.Month(), l.Day()+1, cfg.Hour, cfg.Min, 0, 0, cfg.Loc)
	}
	return minTime(now.Add(interval), at)
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// Run ticks at startup and then at every wake (hourly, or REMINDER_TIME if
// sooner; see nextWake) until ctx ends. A tick only sends once due(now) holds,
// so a day's reminders wait for REMINDER_TIME; later ticks that day retry
// failed sends and catch up. The dedupe table keeps it to one reminder per
// step, and a step missed for a whole day is not backfilled.
func (j *Job) Run(ctx context.Context) {
	for ctx.Err() == nil {
		now := j.Now()
		if due(now, j.Cfg) {
			if _, err := j.Tick(ctx); err != nil && ctx.Err() == nil {
				slog.Error("reminder tick failed", "err", err)
			}
		}
		j.Sleep(ctx, nextWake(now, j.Cfg).Sub(now))
	}
}

type target struct {
	user int64
	kind string
}

// Report counts what one Tick did.
type Report struct {
	Sent    int // delivered or accepted
	Delayed int // 5xx, network error or 429 over the cap: retry on a later tick
	Broken  int // permanent 4xx: webhook marked broken, no retry
	Skipped int // already sent
}

// Tick sends every reminder that is due and not yet sent. It does not check
// REMINDER_TIME: Run does, and `app remind` is started by a schedule.
func (j *Job) Tick(ctx context.Context) (Report, error) {
	var rep Report
	now := j.Now()
	cands, err := j.Store.ReminderCandidates(ctx, now, j.Cfg.Window())
	if err != nil {
		return rep, err
	}
	skip := map[target]bool{} // webhooks that failed this tick
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		tg := target{c.UserID, c.Kind}
		mark, ok := Mark(c.NotAfter, now, j.Cfg)
		if !ok {
			continue
		}
		if skip[tg] { // webhook failed earlier in this tick
			continue
		}
		claimed, err := j.Store.ClaimReminder(ctx, c.CertID, c.Kind, mark)
		if err != nil {
			return rep, err
		}
		if !claimed {
			rep.Skipped++
			continue
		}
		switch j.deliver(ctx, c, mark, now) {
		case sent:
			rep.Sent++
		case delayed:
			rep.Delayed++
			skip[tg] = true
		case broken:
			rep.Broken++
			skip[tg] = true
		}
	}
	slog.Info("reminder tick", "sent", rep.Sent, "delayed", rep.Delayed, "broken", rep.Broken, "skipped", rep.Skipped)
	return rep, nil
}

type outcome int

const (
	sent outcome = iota
	delayed
	broken
)

// deliver sends one claimed reminder.
func (j *Job) deliver(ctx context.Context, c store.Candidate, mark string, now time.Time) outcome {
	kind := webhook.Kind(c.Kind)
	msg := Compose(c, now, j.Cfg)
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
		return sent
	case res.Outcome == webhook.Broken:
		// Keep the claim: a permanent failure must not be retried.
		slog.Warn("webhook broken", "kind", c.Kind, "target", webhook.Redact(c.URL), "reason", res.Message)
		if err := j.Store.MarkWebhookBroken(bg, c.UserID, c.Kind, res.Message); err != nil {
			slog.Error("mark webhook broken", "err", err)
		}
		return broken
	}
	// Rate limited beyond the cap, 5xx or network error: retry next tick.
	slog.Warn("reminder delayed", "kind", c.Kind, "target", webhook.Redact(c.URL), "outcome", res.Outcome)
	if err := j.Store.UnclaimReminder(bg, c.CertID, c.Kind, mark); err != nil {
		slog.Error("release reminder claim", "err", err)
	}
	return delayed
}
