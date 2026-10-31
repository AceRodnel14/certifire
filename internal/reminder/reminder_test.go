package reminder

import (
	"context"
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

func TestMark(t *testing.T) {
	for name, tt := range map[string]struct {
		off  time.Duration
		want string
	}{
		"8 days":         {8*24*time.Hour + time.Hour, ""},
		"7 days":         {7*24*time.Hour + time.Hour, "7"},
		"exactly 7":      {7 * 24 * time.Hour, "7"},
		"6 days":         {6*24*time.Hour + time.Hour, ""},
		"5 days":         {5*24*time.Hour + time.Hour, "5"},
		"4 days":         {4*24*time.Hour + time.Hour, ""},
		"3 days":         {3*24*time.Hour + time.Hour, "3"},
		"2 days":         {2*24*time.Hour + time.Hour, "2"},
		"1 day":          {24*time.Hour + time.Hour, "1"},
		"0 days left":    {12 * time.Hour, ""},
		"just expired":   {0, "expired:2026-06-10"},
		"expired 3 days": {-3 * 24 * time.Hour, "expired:2026-06-10"},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := Mark(now.Add(tt.off), now)
			if got != tt.want || ok != (tt.want != "") {
				t.Errorf("got %q %v", got, ok)
			}
		})
	}
	// Expired marks roll over with the UTC date.
	m1, _ := Mark(now.Add(-time.Hour), now)
	m2, _ := Mark(now.Add(-time.Hour), now.Add(24*time.Hour))
	if m1 == m2 {
		t.Error("expired mark must change each UTC day")
	}
}

func TestCompose(t *testing.T) {
	c := store.Candidate{Name: "web", Hosts: []string{"a", "b", "c", "d", "e", "f", "g"}, NotAfter: now.Add(3*24*time.Hour + time.Hour)}
	m := Compose(c, now)
	if m.Title != "Certificate `web` expires in 3 days" || m.Expired ||
		!strings.Contains(m.Text, "`a`, `b`, `c`, `d`, `e` and 2 more") || !strings.Contains(m.Text, "2026-06-13") {
		t.Errorf("%+v", m)
	}
	c.NotAfter = now.Add(25 * time.Hour)
	if m := Compose(c, now); m.Title != "Certificate `web` expires in 1 day" {
		t.Errorf("%q", m.Title)
	}
	c.NotAfter = now.Add(-49 * time.Hour)
	if m := Compose(c, now); m.Title != "Certificate `web` expired 2 days ago" || !m.Expired {
		t.Errorf("%+v", m)
	}
	c.NotAfter = now.Add(-time.Hour)
	if m := Compose(c, now); m.Title != "Certificate `web` expired today" {
		t.Errorf("%q", m.Title)
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
	h.job = &Job{Store: h.st, Sender: sender, Now: func() time.Time { return h.clock },
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
	if err := h.job.Tick(context.Background()); err != nil {
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
	h.job = &Job{Store: h.st, Sender: h.job.Sender, Now: h.job.Now, Sleep: h.job.Sleep}
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
	// Day offsets 0..8 -> days left 7,6,5,4,3,2,1,0(not expired),expired: marks 7,5,3,2,1 + expired.
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
	old := h.addCert(t, "old", 2*24*time.Hour+time.Hour)
	del := h.addCert(t, "del", 2*24*time.Hour+time.Hour)
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
	h.addCert(t, "b", 2*24*time.Hour+time.Hour)

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
	h3.addCert(t, "b", 2*24*time.Hour+time.Hour)
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
	_ = h.job.Tick(ctx) // must not panic or leave a claim; next tick still sends
	if got := h.tick(t); len(got) != 1 {
		t.Errorf("after cancelled tick: %v", got)
	}
}
