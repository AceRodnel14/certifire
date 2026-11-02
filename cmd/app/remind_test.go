package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"certifire/internal/crypto"
	"certifire/internal/reminder"
	"certifire/internal/store"
	"certifire/internal/storetest"
	"certifire/internal/webhook"
)

type rewrite struct{ to *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestExitCode(t *testing.T) {
	const hook = "https://discord.com/api/webhooks/123456789012345678/aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-"
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		status int
		want   int
	}{
		{"delivered", 204, 0},
		{"5xx is delayed", 503, 1},
		{"429 over the cap is delayed", 429, 1},
		{"permanent 4xx is a broken webhook, not a failure", 404, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := storetest.New(t)
			ctx := context.Background()
			u := &store.User{Username: "u", AuthSalt: []byte{1}, AuthHash: []byte{1}, KEKSalt: []byte{1}, WrappedDEK: []byte{1}, EscrowDEK: []byte{1}}
			if err := st.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SetWebhook(ctx, u.ID, "discord", hook); err != nil {
				t.Fatal(err)
			}
			c := &store.Cert{ID: crypto.NewUUID(), UserID: u.ID, Name: "c", Hosts: []string{"h"}, Bits: 2048, Days: 10, Subject: []byte(`{}`),
				CNF: "c", CertPEM: "p", KeyEnc: []byte("k"), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(3*24*time.Hour + time.Hour)}
			if err := st.InsertCert(ctx, c, ""); err != nil {
				t.Fatal(err)
			}
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status == 429 {
					w.Header().Set("Retry-After", "600") // over the cap: no in-tick retry
				}
				w.WriteHeader(tt.status)
			}))
			defer stub.Close()
			su, _ := url.Parse(stub.URL)
			sender := webhook.NewSender()
			sender.Client.Transport = rewrite{su}
			cfg, err := reminder.LoadConfig(func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			job := reminder.New(st, sender, cfg)
			job.Now = func() time.Time { return now }
			if got := exitCode(ctx, job); got != tt.want {
				t.Errorf("exit %d, want %d", got, tt.want)
			}
			// A second run after a success sends nothing and exits 0.
			if tt.want == 0 {
				if got := exitCode(ctx, job); got != 0 {
					t.Errorf("rerun exit %d", got)
				}
			}
		})
	}
}
