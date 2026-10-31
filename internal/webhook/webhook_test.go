package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	token      = strings.Repeat("aB3_-", 14) // 70 chars
	discordURL = "https://discord.com/api/webhooks/123456789012345678/" + token
	teamsURL   = "https://prod-00.westeurope.environment.api.powerplatform.com:443/powerautomate/automations/direct/workflows/0123456789abcdef0123456789abcdef/triggers/manual/paths/invoke?api-version=1&sp=%2Ftriggers%2Fmanual%2Frun&sv=1.0&sig=FAKESIG"
)

func TestValidate(t *testing.T) {
	for name, tt := range map[string]struct {
		kind Kind
		url  string
		want string // normalised URL when valid; "" when invalid
	}{
		"discord":              {Discord, discordURL, discordURL},
		"discord v10":          {Discord, strings.Replace(discordURL, "/api/", "/api/v10/", 1), strings.Replace(discordURL, "/api/", "/api/v10/", 1)},
		"discord trimmed":      {Discord, "  " + discordURL + " ", discordURL},
		"discord port 443":     {Discord, strings.Replace(discordURL, "discord.com", "discord.com:443", 1), discordURL},
		"discord uppercase":    {Discord, strings.Replace(discordURL, "discord.com", "DISCORD.com", 1), discordURL},
		"discordapp rewritten": {Discord, strings.Replace(discordURL, "discord.com", "discordapp.com", 1), discordURL},
		"ptb rewritten":        {Discord, strings.Replace(discordURL, "discord.com", "ptb.discord.com", 1), discordURL},
		"canary rewritten":     {Discord, strings.Replace(discordURL, "discord.com", "canary.discord.com", 1), discordURL},
		"thread":               {Discord, discordURL + "?thread_id=123", discordURL + "?thread_id=123"},
		"teams":                {Teams, teamsURL, strings.Replace(teamsURL, ":443", "", 1)},

		"http":              {Discord, strings.Replace(discordURL, "https", "http", 1), ""},
		"look-alike host":   {Discord, strings.Replace(discordURL, "discord.com", "discord.com.evil.test", 1), ""},
		"look-alike prefix": {Discord, strings.Replace(discordURL, "discord.com", "evildiscord.com", 1), ""},
		"userinfo":          {Discord, strings.Replace(discordURL, "://", "://user:pw@", 1), ""},
		"port 8443":         {Discord, strings.Replace(discordURL, "discord.com", "discord.com:8443", 1), ""},
		"extra path":        {Discord, discordURL + "/extra", ""},
		"short token":       {Discord, "https://discord.com/api/webhooks/123456789012345678/abc", ""},
		"short id":          {Discord, "https://discord.com/api/webhooks/123/" + token, ""},
		"unexpected query":  {Discord, discordURL + "?wait=true", ""},
		"bad thread":        {Discord, discordURL + "?thread_id=abc", ""},
		"double thread":     {Discord, discordURL + "?thread_id=1&thread_id=2", ""},
		"fragment":          {Discord, discordURL + "#x", ""},
		"empty fragment":    {Discord, discordURL + "#", ""},
		"ip literal":        {Discord, "https://192.0.2.1/api/webhooks/123456789012345678/" + token, ""},
		"ipv6 literal":      {Discord, "https://[2001:db8::1]/api/webhooks/123456789012345678/" + token, ""},
		"empty":             {Discord, "", ""},
		"too long":          {Discord, discordURL + "?thread_id=" + strings.Repeat("1", 2100), ""},
		"opaque":            {Discord, "https:discord.com/x", ""},
		"teams no sig":      {Teams, strings.Replace(teamsURL, "sig=FAKESIG", "x=1", 1), ""},
		"teams empty sig":   {Teams, strings.Replace(teamsURL, "sig=FAKESIG", "sig=", 1), ""},
		"teams bare suffix": {Teams, "https://environment.api.powerplatform.com/powerautomate/x?sig=a", ""},
		"teams dot suffix":  {Teams, "https://.environment.api.powerplatform.com/powerautomate/x?sig=a", ""},
		"teams look-alike":  {Teams, "https://x.environment.api.powerplatform.com.evil.test/powerautomate/x?sig=a", ""},
		"teams wrong path":  {Teams, "https://x.environment.api.powerplatform.com/other/x?sig=a", ""},
		"teams discord url": {Teams, discordURL, ""},
		"discord teams url": {Discord, teamsURL, ""},
		"office connector":  {Teams, "https://contoso.webhook.office.com/webhookb2/abc/IncomingWebhook/def/ghi", ""},
		"outlook office":    {Teams, "https://outlook.office.com/webhook/abc", ""},
		"logic azure":       {Teams, "https://prod-1.westeurope.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?sig=x", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Validate(tt.kind, tt.url)
			if tt.want == "" {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("want ValidationError, got %q %v", got, err)
				}
				if strings.Contains(ve.Msg, token) || strings.Contains(ve.Msg, "FAKESIG") {
					t.Errorf("error leaks the secret: %s", ve.Msg)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %q (%v), want %q", got, err, tt.want)
			}
		})
	}
	_, err := Validate(Teams, "https://contoso.webhook.office.com/x")
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Errorf("retired hint missing: %v", err)
	}
}

func TestMaskAndRedact(t *testing.T) {
	m := Mask(discordURL)
	if m != "https://discord.com/…"+discordURL[len(discordURL)-4:] || strings.Contains(m, "webhooks") {
		t.Errorf("mask %q", m)
	}
	if r := Redact(teamsURL); strings.Contains(r, "FAKESIG") || strings.Contains(r, "powerautomate") || !strings.HasSuffix(r, "/…") {
		t.Errorf("redact %q", r)
	}
}

// rewrite sends every request to the stub server, keeping the original URL out of reach.
type rewrite struct{ to *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func stubSender(t *testing.T, h http.HandlerFunc) *Sender {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	s := NewSender()
	s.Client.Transport = rewrite{u}
	return s
}

func TestSendClassification(t *testing.T) {
	for name, tt := range map[string]struct {
		kind    Kind
		url     string
		status  int
		header  http.Header
		body    string
		outcome string
		retry   time.Duration
	}{
		"discord 204":       {Discord, discordURL, 204, nil, "", Delivered, 0},
		"discord 200":       {Discord, discordURL, 200, nil, "{}", Delivered, 0},
		"teams 202":         {Teams, teamsURL, 202, nil, "", Accepted, 0},
		"429 header":        {Discord, discordURL, 429, http.Header{"Retry-After": {"3"}}, "", RateLimited, 3 * time.Second},
		"429 body":          {Discord, discordURL, 429, nil, `{"retry_after": 1.5}`, RateLimited, 1500 * time.Millisecond},
		"429 none":          {Discord, discordURL, 429, nil, "", RateLimited, 0},
		"500":               {Discord, discordURL, 500, nil, "", Error, 0},
		"503":               {Teams, teamsURL, 503, nil, "", Error, 0},
		"404 discord":       {Discord, discordURL, 404, nil, `{"code":10015}`, Broken, 0},
		"401 discord":       {Discord, discordURL, 401, nil, "", Broken, 0},
		"400":               {Discord, discordURL, 400, nil, "", Broken, 0},
		"403 teams":         {Teams, teamsURL, 403, nil, "", Broken, 0},
		"404 teams":         {Teams, teamsURL, 404, nil, "", Broken, 0},
		"redirect not used": {Discord, discordURL, 302, http.Header{"Location": {"https://evil.test/"}}, "", Broken, 0},
	} {
		t.Run(name, func(t *testing.T) {
			s := stubSender(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.header {
					w.Header()[k] = v
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			got := s.Send(context.Background(), tt.kind, tt.url, Message{Title: "t", Text: "x"})
			if got.Outcome != tt.outcome || got.RetryAfter != tt.retry {
				t.Errorf("got %+v", got)
			}
			if strings.Contains(got.Message, token) || strings.Contains(got.Message, "FAKESIG") {
				t.Errorf("message leaks the secret: %s", got.Message)
			}
			if got.Outcome == Broken && !strings.Contains(got.Message, " "+itoa(tt.status)+":") {
				t.Errorf("broken reason lacks status: %q", got.Message)
			}
		})
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestSendPayloads(t *testing.T) {
	var gotPath, gotQuery, gotCT string
	var got map[string]any
	s := stubSender(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotCT = r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	})
	msg := Message{Title: "Certificate `x` expires in 3 days", Text: "hosts"}

	if r := s.Send(context.Background(), Discord, discordURL, msg); r.Outcome != Delivered {
		t.Fatal(r)
	}
	if gotCT != "application/json" || !strings.HasSuffix(gotPath, token) {
		t.Errorf("request %q %q", gotCT, gotPath)
	}
	if got["username"] != "CertiFire" || got["allowed_mentions"].(map[string]any)["parse"].([]any) == nil {
		t.Errorf("discord payload %v", got)
	}
	embed := got["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != msg.Title || embed["description"] != "hosts" {
		t.Errorf("embed %v", embed)
	}

	got = nil
	if r := s.Send(context.Background(), Teams, teamsURL, msg); r.Outcome != Delivered && r.Outcome != Accepted {
		t.Fatal(r)
	}
	if !strings.Contains(gotQuery, "sig=FAKESIG") || got["type"] != "message" {
		t.Errorf("teams %q %v", gotQuery, got)
	}
	content := got["attachments"].([]any)[0].(map[string]any)["content"].(map[string]any)
	if content["version"] != "1.4" || len(content["body"].([]any)) != 2 {
		t.Errorf("card %v", content)
	}
}

func TestSendNeverFollowsRedirectsAndHidesURL(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer other.Close()
	s := stubSender(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) })
	if r := s.Send(context.Background(), Discord, discordURL, Message{}); r.Outcome != Broken || hit {
		t.Errorf("redirect followed or misclassified: %+v hit=%v", r, hit)
	}

	// Network failure: error text must not contain the URL.
	s = NewSender()
	s.Client.Transport = http.RoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: errors.New("boom")}
	}))
	r := s.Send(context.Background(), Discord, discordURL, Message{})
	if r.Outcome != Error || strings.Contains(r.Message, token) || strings.Contains(r.Message, "boom") {
		t.Errorf("network error result: %+v", r)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendRevalidates(t *testing.T) {
	called := false
	s := stubSender(t, func(http.ResponseWriter, *http.Request) { called = true })
	if r := s.Send(context.Background(), Discord, "https://evil.test/x", Message{}); r.Outcome != Broken || called {
		t.Errorf("unvalidated URL sent: %+v", r)
	}
}

func TestSendTimeout(t *testing.T) {
	release := make(chan struct{})
	s := stubSender(t, func(http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	s.Client.Timeout = 50 * time.Millisecond
	if r := s.Send(context.Background(), Discord, discordURL, Message{}); r.Outcome != Error || !strings.Contains(r.Message, "timed out") {
		t.Errorf("%+v", r)
	}
}

func TestParseKindAndResult(t *testing.T) {
	if _, ok := ParseKind("slack"); ok {
		t.Error("slack accepted")
	}
	if k, ok := ParseKind("teams"); !ok || k != Teams {
		t.Error("teams")
	}
	if !(Result{Outcome: Accepted}).OK() || (Result{Outcome: Broken}).Transient() || !(Result{Outcome: RateLimited}).Transient() {
		t.Error("result helpers")
	}
}
