package reminder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"certifire/internal/crypto"
	"certifire/internal/store"
	"certifire/internal/storetest"
	"certifire/internal/webhook"
)

var now = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func mustCfg(t *testing.T, env map[string]string) Config {
	t.Helper()
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMark(t *testing.T) {
	utc := mustCfg(t, nil)
	manila := mustCfg(t, map[string]string{"REMINDER_TZ": "Asia/Manila"})
	for _, tt := range []struct {
		name     string
		cfg      Config
		notAfter string
		now      string
		days     int
		want     string
	}{
		{"8 days", utc, "2026-06-18T13:00:00Z", "2026-06-10T12:00:00Z", 8, ""},
		{"7 days", utc, "2026-06-17T13:00:00Z", "2026-06-10T12:00:00Z", 7, "7"},
		{"7 days, expiry earlier in day than now", utc, "2026-06-17T01:00:00Z", "2026-06-10T23:00:00Z", 7, "7"},
		{"6 days", utc, "2026-06-16T13:00:00Z", "2026-06-10T12:00:00Z", 6, ""},
		{"5 days", utc, "2026-06-15T13:00:00Z", "2026-06-10T12:00:00Z", 5, "5"},
		{"4 days", utc, "2026-06-14T13:00:00Z", "2026-06-10T12:00:00Z", 4, ""},
		{"3 days", utc, "2026-06-13T13:00:00Z", "2026-06-10T12:00:00Z", 3, "3"},
		{"2 days (not a default step)", utc, "2026-06-12T13:00:00Z", "2026-06-10T12:00:00Z", 2, ""},
		{"1 day", utc, "2026-06-11T13:00:00Z", "2026-06-10T12:00:00Z", 1, "1"},
		{"day 0 before not_after", utc, "2026-06-10T13:00:00Z", "2026-06-10T12:00:00Z", 0, "0"},
		{"day 0 after not_after", utc, "2026-06-10T11:00:00Z", "2026-06-10T12:00:00Z", 0, "0"},
		{"day 0 last second", utc, "2026-06-10T00:00:00Z", "2026-06-10T23:59:59Z", 0, "0"},
		{"expired day+1", utc, "2026-06-10T11:00:00Z", "2026-06-11T00:00:00Z", -1, "expired:2026-06-11"},
		{"expired day+2", utc, "2026-06-10T11:00:00Z", "2026-06-12T09:00:00Z", -2, "expired:2026-06-12"},
		// not_after is Nov 8 16:30Z = Nov 9 00:30 in Manila (UTC+8).
		{"UTC: 0 days on Nov 8", utc, "2026-11-08T16:30:00Z", "2026-11-08T10:00:00Z", 0, "0"},
		{"Manila: 1 day on Nov 8 UTC morning (Nov 8 18:00 local)", manila, "2026-11-08T16:30:00Z", "2026-11-08T10:00:00Z", 1, "1"},
		{"Manila: day 0 at 00:00 local", manila, "2026-11-08T16:30:00Z", "2026-11-08T16:00:00Z", 0, "0"},
		{"UTC: expired already at 00:00Z Nov 9", utc, "2026-11-08T16:30:00Z", "2026-11-09T00:00:00Z", -1, "expired:2026-11-09"},
		{"Manila: still day 0 at 00:00Z Nov 9 (08:00 local)", manila, "2026-11-08T16:30:00Z", "2026-11-09T00:00:00Z", 0, "0"},
		{"Manila: expired mark uses the local date", manila, "2026-11-08T16:30:00Z", "2026-11-09T16:00:00Z", -1, "expired:2026-11-10"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			na, now := at(t, tt.notAfter), at(t, tt.now)
			if d := daysLeft(na, now, tt.cfg.Loc); d != tt.days {
				t.Errorf("daysLeft %d, want %d", d, tt.days)
			}
			got, ok := Mark(na, now, tt.cfg)
			if got != tt.want || ok != (tt.want != "") {
				t.Errorf("got %q %v, want %q", got, ok, tt.want)
			}
		})
	}
}

func TestMarkDST(t *testing.T) {
	// New York springs forward on 2026-03-08: Mar 7 12:00 local to Mar 9 12:00
	// local is 2 calendar days even though it is 47 hours.
	cfg := mustCfg(t, map[string]string{"REMINDER_TZ": "America/New_York", "REMINDER_STEPS": "2"})
	na := time.Date(2026, 3, 9, 12, 0, 0, 0, cfg.Loc)
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, cfg.Loc)
	if m, ok := Mark(na, now, cfg); !ok || m != "2" {
		t.Errorf("got %q %v", m, ok)
	}
}

func TestCompose(t *testing.T) {
	utc := mustCfg(t, nil)
	c := store.Candidate{Name: "web", Hosts: []string{"a", "b", "c", "d", "e", "f", "g"}, NotAfter: now.Add(3*24*time.Hour + time.Hour)}
	m := Compose(c, now, utc)
	if m.Title != "Certificate `web` expires in 3 days" || m.Expired ||
		!strings.Contains(m.Text, "`a`, `b`, `c`, `d`, `e` and 2 more") || !strings.Contains(m.Text, "2026-06-13 (UTC)") {
		t.Errorf("%+v", m)
	}
	c.NotAfter = now.Add(25 * time.Hour)
	if m := Compose(c, now, utc); m.Title != "Certificate `web` expires in 1 day" {
		t.Errorf("%q", m.Title)
	}
	c.NotAfter = now.Add(-49 * time.Hour)
	if m := Compose(c, now, utc); m.Title != "Certificate `web` expired 2 days ago" || !m.Expired {
		t.Errorf("%+v", m)
	}
	c.NotAfter = now.Add(-time.Hour)
	if m := Compose(c, now, utc); m.Title != "Certificate `web` expired today" || !m.Expired {
		t.Errorf("%q", m.Title)
	}
	c.NotAfter = now.Add(time.Hour)
	if m := Compose(c, now, utc); m.Title != "Certificate `web` expires today" || m.Expired {
		t.Errorf("%+v", m)
	}
	// Dates are shown in the configured zone: Nov 8 16:30Z is Nov 9 in Manila.
	manila := mustCfg(t, map[string]string{"REMINDER_TZ": "Asia/Manila"})
	c.NotAfter = at(t, "2026-11-08T16:30:00Z")
	m = Compose(c, at(t, "2026-11-08T10:00:00Z"), manila)
	if m.Title != "Certificate `web` expires in 1 day" || !strings.Contains(m.Text, "2026-11-09 (Asia/Manila)") {
		t.Errorf("%+v", m)
	}
}

func TestDue(t *testing.T) {
	manila := mustCfg(t, map[string]string{"REMINDER_TZ": "Asia/Manila", "REMINDER_TIME": "09:30"})
	utc := mustCfg(t, nil)
	for _, tt := range []struct {
		name string
		cfg  Config
		now  string
		want bool
	}{
		{"default 00:00 is always due", utc, "2026-06-10T00:00:00Z", true},
		{"default late in the day", utc, "2026-06-10T23:59:00Z", true},
		{"Manila before 09:30 (01:29Z)", manila, "2026-06-10T01:29:59Z", false},
		{"Manila at 09:30 (01:30Z)", manila, "2026-06-10T01:30:00Z", true},
		{"Manila after", manila, "2026-06-10T15:00:00Z", true},
		{"Manila just after local midnight", manila, "2026-06-09T16:00:00Z", false},
		{"Manila late evening local", manila, "2026-06-10T15:59:00Z", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := due(at(t, tt.now), tt.cfg); got != tt.want {
				t.Errorf("got %v", got)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg := mustCfg(t, nil)
		if cfg.TZ != "UTC" || cfg.Hour != 0 || cfg.Min != 0 || !cfg.LocalCron || fmt.Sprint(cfg.Steps) != "[7 5 3 1 0]" || cfg.Window() != 9 {
			t.Errorf("%+v", cfg)
		}
	})
	t.Run("custom", func(t *testing.T) {
		cfg := mustCfg(t, map[string]string{"REMINDER_TZ": "Asia/Manila", "REMINDER_TIME": "08:05",
			"REMINDER_STEPS": " 1, 14,0 ,3", "REMINDER_LOCAL_CRON": "false"})
		if cfg.TZ != "Asia/Manila" || cfg.Hour != 8 || cfg.Min != 5 || cfg.LocalCron || fmt.Sprint(cfg.Steps) != "[14 3 1 0]" || cfg.Window() != 16 {
			t.Errorf("%+v", cfg)
		}
	})
	for name, env := range map[string]map[string]string{
		"bad tz":           {"REMINDER_TZ": "Mars/Base"},
		"bad time":         {"REMINDER_TIME": "25:00"},
		"time no colon":    {"REMINDER_TIME": "0900"},
		"steps text":       {"REMINDER_STEPS": "7,x"},
		"steps negative":   {"REMINDER_STEPS": "7,-1"},
		"steps too big":    {"REMINDER_STEPS": "366"},
		"steps duplicate":  {"REMINDER_STEPS": "7,3,7"},
		"steps empty item": {"REMINDER_STEPS": "7,,1"},
		"steps blank":      {"REMINDER_STEPS": " "},
		"cron bad":         {"REMINDER_LOCAL_CRON": "maybe"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(func(k string) string { return env[k] })
			if err == nil || !strings.Contains(err.Error(), "REMINDER_") {
				t.Errorf("err %v", err)
			}
		})
	}
	if cfg := mustCfg(t, map[string]string{"REMINDER_STEPS": "365"}); cfg.Steps[0] != 365 {
		t.Error("365 is valid")
	}
}

type harness struct {
	st     *store.Store
	job    *Job
	mu     sync.Mutex
	calls  []string // "<kind-path>" per request
	status map[string]int
	clock  time.Time
	slept  []time.Duration
	userID int64
}

const (
	dTok = "aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-"
	dURL = "https://discord.com/api/webhooks/123456789012345678/" + dTok
	tURL = "https://qa.environment.api.powerplatform.com/powerautomate/x?sig=FAKESIG"
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{st: storetest.New(t), status: map[string]int{"discord": 204, "teams": 202}, clock: now}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := "discord"
		if strings.HasPrefix(r.URL.Path, "/powerautomate") {
			kind = "teams"
		}
		h.mu.Lock()
		h.calls = append(h.calls, kind)
		code := h.status[kind]
		h.mu.Unlock()
		if code == 429 {
			w.Header().Set("Retry-After", "2")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(stub.Close)
	su, _ := url.Parse(stub.URL)
	sender := webhook.NewSender()
	sender.Client.Transport = rt{su}
	h.job = &Job{Store: h.st, Sender: sender, Cfg: mustCfg(t, nil), Now: func() time.Time { return h.clock },
		Sleep: func(_ context.Context, d time.Duration) { h.slept = append(h.slept, d) }}
	u := &store.User{Username: "u", AuthSalt: []byte{1}, AuthHash: []byte{1}, KEKSalt: []byte{1}, WrappedDEK: []byte{1}, EscrowDEK: []byte{1}}
	if err := h.st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	h.userID = u.ID
	return h
}

type rt struct{ to *url.URL }

func (r rt) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func (h *harness) addCert(t *testing.T, name string, off time.Duration) string {
	t.Helper()
	c := &store.Cert{ID: crypto.NewUUID(), UserID: h.userID, Name: name, Hosts: []string{name + ".test"}, Bits: 2048, Days: 10,
		Subject: []byte(`{}`), CNF: "c", CertPEM: "p", KeyEnc: []byte("k"), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(off)}
	if err := h.st.InsertCert(context.Background(), c, ""); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func (h *harness) tick(t *testing.T) []string {
	t.Helper()
	h.mu.Lock()
	h.calls = nil
	h.mu.Unlock()
	if _, err := h.job.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func (h *harness) hook(t *testing.T, kind, url string) {
	t.Helper()
	if _, err := h.st.SetWebhook(context.Background(), h.userID, kind, url); err != nil {
		t.Fatal(err)
	}
}

func eq(a []string, b ...string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestTickThresholdsAndDedupe(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	h.addCert(t, "c7", 7*24*time.Hour+time.Hour)
	h.addCert(t, "c6", 6*24*time.Hour+time.Hour) // no mark
	h.addCert(t, "far", 30*24*time.Hour)

	if got := h.tick(t); !eq(got, "discord") {
		t.Fatalf("first tick %v", got)
	}
	if got := h.tick(t); len(got) != 0 {
		t.Fatalf("second tick must not resend: %v", got)
	}
	// A "restart" is just a new Job on the same DB.
	h.job = &Job{Store: h.st, Sender: h.job.Sender, Cfg: h.job.Cfg, Now: h.job.Now, Sleep: h.job.Sleep}
	if got := h.tick(t); len(got) != 0 {
		t.Fatalf("after restart %v", got)
	}
	// Next day: c7 is at 6 days (no mark), c6 at 5 days.
	h.clock = now.Add(24 * time.Hour)
	if got := h.tick(t); !eq(got, "discord") {
		t.Fatalf("day 2: %v", got)
	}
}

func TestTickAllMarks(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	h.addCert(t, "c", 7*24*time.Hour+time.Hour)
	var sent int
	for day := range 9 {
		h.clock = now.Add(time.Duration(day) * 24 * time.Hour)
		sent += len(h.tick(t))
	}
	// Day offsets 0..8 -> days left 7..0 then expired: marks 7,5,3,1,0 + expired.
	if sent != 6 {
		t.Errorf("sent %d, want 6", sent)
	}
	// Expired again the same UTC day: no resend; next day: one more.
	if got := h.tick(t); len(got) != 0 {
		t.Errorf("same-day resend %v", got)
	}
	h.clock = h.clock.Add(24 * time.Hour)
	if got := h.tick(t); len(got) != 1 {
		t.Errorf("next-day expired reminder %v", got)
	}
}

func TestTickSkipsRenewedDeletedAndBroken(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	old := h.addCert(t, "old", 3*24*time.Hour+time.Hour)
	del := h.addCert(t, "del", 3*24*time.Hour+time.Hour)
	// Renew "old".
	nc := &store.Cert{ID: crypto.NewUUID(), UserID: h.userID, Name: "old", Hosts: []string{"o.test"}, Bits: 2048, Days: 800,
		Subject: []byte(`{}`), CNF: "c", CertPEM: "p", KeyEnc: []byte("k"), NotBefore: now, NotAfter: now.Add(800 * 24 * time.Hour)}
	if err := h.st.InsertCert(context.Background(), nc, old); err != nil {
		t.Fatal(err)
	}
	if err := h.st.DeleteCert(context.Background(), h.userID, del); err != nil {
		t.Fatal(err)
	}
	if got := h.tick(t); len(got) != 0 {
		t.Errorf("renewed/deleted got reminders: %v", got)
	}
	// Deleting the renewal resumes reminders for the old cert.
	if err := h.st.DeleteCert(context.Background(), h.userID, nc.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.tick(t); !eq(got, "discord") {
		t.Errorf("resume after renewal deleted: %v", got)
	}
	// Broken webhook: skipped.
	h.addCert(t, "next", 1*24*time.Hour+time.Hour)
	if err := h.st.MarkWebhookBroken(context.Background(), h.userID, "discord", "x"); err != nil {
		t.Fatal(err)
	}
	if got := h.tick(t); len(got) != 0 {
		t.Errorf("broken webhook used: %v", got)
	}
}

func TestTickBothKinds(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	h.hook(t, "teams", tURL)
	h.addCert(t, "c", 3*24*time.Hour+time.Hour)
	got := h.tick(t)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got := h.tick(t); len(got) != 0 {
		t.Errorf("resend %v", got)
	}
}

func TestTickFailureHandling(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	h.addCert(t, "a", 3*24*time.Hour+time.Hour)
	h.addCert(t, "b", 1*24*time.Hour+time.Hour)

	// 5xx: retried next tick, nothing lost; the webhook is skipped for the rest of this tick.
	h.status["discord"] = 503
	if got := h.tick(t); len(got) != 1 {
		t.Errorf("5xx tick sent %v", got)
	}
	h.status["discord"] = 204
	if got := h.tick(t); len(got) != 2 {
		t.Errorf("retry after 5xx: %v", got)
	}

	// 429: waits Retry-After once, retries once, then gives up and releases the claim.
	h2 := newHarness(t)
	h2.hook(t, "discord", dURL)
	h2.addCert(t, "a", 3*24*time.Hour+time.Hour)
	h2.status["discord"] = 429
	if got := h2.tick(t); len(got) != 2 || len(h2.slept) != 1 || h2.slept[0] != 2*time.Second {
		t.Errorf("429: calls %v slept %v", got, h2.slept)
	}
	h2.status["discord"] = 204
	if got := h2.tick(t); len(got) != 1 {
		t.Errorf("after 429 not retried: %v", got)
	}

	// Permanent 4xx: webhook broken, claim kept, no further sends.
	h3 := newHarness(t)
	h3.hook(t, "discord", dURL)
	h3.addCert(t, "a", 3*24*time.Hour+time.Hour)
	h3.addCert(t, "b", 1*24*time.Hour+time.Hour)
	h3.status["discord"] = 404
	if got := h3.tick(t); len(got) != 1 {
		t.Errorf("404 tick %v", got)
	}
	w, err := h3.st.GetWebhook(context.Background(), h3.userID, "discord")
	if err != nil || w.BrokenAt == nil || w.BrokenReason == nil || !strings.Contains(*w.BrokenReason, "Discord 404") || strings.Contains(*w.BrokenReason, dTok) {
		t.Errorf("broken state %+v %v", w, err)
	}
	h3.status["discord"] = 204
	if got := h3.tick(t); len(got) != 0 {
		t.Errorf("sent to broken webhook: %v", got)
	}
	// Re-saving the URL clears broken; the 404'd reminder stays claimed, the other resumes.
	h3.hook(t, "discord", dURL)
	if got := h3.tick(t); len(got) != 1 {
		t.Errorf("after re-save %v", got)
	}
}

func TestTickCancelled(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	h.addCert(t, "a", 3*24*time.Hour+time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h.job.Tick(ctx) // must not panic or leave a claim; next tick still sends
	if got := h.tick(t); len(got) != 1 {
		t.Errorf("after cancelled tick: %v", got)
	}
}

func TestRunGatedByDueAndRetries(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	h.addCert(t, "a", 3*24*time.Hour+time.Hour)
	h.job.Cfg = mustCfg(t, map[string]string{"REMINDER_TIME": "15:00"})
	// 12:00Z is before 15:00Z: Run's startup tick must stay quiet, then
	// stop when ctx ends.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.job.Run(ctx)
	h.mu.Lock()
	n := len(h.calls)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("sent before REMINDER_TIME: %d", n)
	}
	// After REMINDER_TIME the same job sends; a failed send is retried by the next tick.
	h.clock = now.Add(4 * time.Hour)
	h.status["discord"] = 503
	rep, err := h.job.Tick(context.Background())
	if err != nil || rep.Delayed != 1 || rep.Sent != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	h.status["discord"] = 204
	h.clock = h.clock.Add(time.Hour)
	rep, _ = h.job.Tick(context.Background())
	if rep.Sent != 1 {
		t.Errorf("retry: %+v", rep)
	}
	rep, _ = h.job.Tick(context.Background())
	if rep.Sent != 0 || rep.Skipped != 1 {
		t.Errorf("rerun: %+v", rep)
	}
}

func TestNextWake(t *testing.T) {
	late := mustCfg(t, map[string]string{"REMINDER_TIME": "23:30"})
	manila := mustCfg(t, map[string]string{"REMINDER_TZ": "Asia/Manila", "REMINDER_TIME": "09:30"})
	ny := mustCfg(t, map[string]string{"REMINDER_TZ": "America/New_York", "REMINDER_TIME": "02:30"})
	for _, tt := range []struct {
		name string
		cfg  Config
		now  string
		want string
	}{
		{"23:15 wakes at 23:30, not 00:15", late, "2026-06-10T23:15:00Z", "2026-06-10T23:30:00Z"},
		{"23:30 exactly: next is the hourly tick", late, "2026-06-10T23:30:00Z", "2026-06-11T00:30:00Z"},
		{"22:45 hourly tick is sooner", late, "2026-06-10T22:45:00Z", "2026-06-10T23:30:00Z"},
		{"22:20 hourly tick is sooner", late, "2026-06-10T22:20:00Z", "2026-06-10T23:20:00Z"},
		{"23:59 hourly tick", late, "2026-06-10T23:59:00Z", "2026-06-11T00:59:00Z"},
		{"midnight default 00:00 at 23:30 wakes at midnight", mustCfg(t, nil), "2026-06-10T23:30:00Z", "2026-06-11T00:00:00Z"},
		{"midnight default at 00:00 is hourly", mustCfg(t, nil), "2026-06-11T00:00:00Z", "2026-06-11T01:00:00Z"},
		{"Manila 09:30 = 01:30Z; at 01:00Z wakes 01:30Z", manila, "2026-06-10T01:00:00Z", "2026-06-10T01:30:00Z"},
		// New York springs forward 2026-03-08 02:00 -> 03:00, so 02:30 does not
		// exist that day; Go picks an adjacent instant, so only the bound is checked.
		{"DST gap evening before", ny, "2026-03-08T06:10:00Z", ""},
		{"DST gap day before", ny, "2026-03-07T07:40:00Z", ""},
		{"DST fall back (2026-11-01)", ny, "2026-11-01T05:40:00Z", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := at(t, tt.now)
			got := nextWake(now, tt.cfg)
			if want := tt.want; want != "" && !got.Equal(at(t, want)) {
				t.Errorf("got %s, want %s", got.UTC().Format(time.RFC3339), tt.want)
			}
			if !got.After(now) || got.Sub(now) > time.Hour {
				t.Errorf("wake %s is not within (now, now+1h]", got)
			}
		})
	}
}

func TestRunSendsLateStepSameDay(t *testing.T) {
	h := newHarness(t)
	h.hook(t, "discord", dURL)
	// Expires 2026-06-13 13:00Z: Jun 10 is 3 days left, a step.
	h.addCert(t, "a", 3*24*time.Hour+time.Hour)
	h.job.Cfg = mustCfg(t, map[string]string{"REMINDER_TIME": "23:30"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.clock = at(t, "2026-06-10T22:20:00Z")
	var wakes []time.Time
	h.job.Sleep = func(_ context.Context, d time.Duration) {
		h.clock = h.clock.Add(d)
		wakes = append(wakes, h.clock)
		if h.clock.Day() != 10 {
			cancel()
		}
	}
	h.job.Run(ctx)
	h.mu.Lock()
	n := len(h.calls)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("sent %d, want 1; wakes %v", n, wakes)
	}
	// Wakes: 22:20 -> 23:20 -> 23:30 (REMINDER_TIME, sooner than 00:20) -> 00:30.
	if len(wakes) != 3 || wakes[1].Format("15:04") != "23:30" {
		t.Errorf("wakes %v", wakes)
	}
}
