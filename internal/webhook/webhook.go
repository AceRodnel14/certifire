// Package webhook validates, masks and sends Discord and Microsoft Teams
// (Workflows) webhook messages. The whole webhook URL is a secret: it is never
// logged and never appears in an error returned from this package.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind is a webhook provider.
type Kind string

// Supported providers.
const (
	Discord Kind = "discord"
	Teams   Kind = "teams"
)

// ParseKind returns the Kind for s, or false.
func ParseKind(s string) (Kind, bool) {
	k := Kind(s)
	return k, k == Discord || k == Teams
}

// Outcome values returned by Send (and by the test endpoint).
const (
	Delivered   = "delivered"
	Accepted    = "accepted"
	Broken      = "broken"
	RateLimited = "rate_limited"
	Error       = "error"
)

const maxURLLen = 2048

var (
	discordPath = regexp.MustCompile(`^/api(/v\d+)?/webhooks/\d{17,20}/[A-Za-z0-9_-]{60,100}$`)
	digitsRe    = regexp.MustCompile(`^\d+$`)
)

const teamsSuffix = ".environment.api.powerplatform.com"

// ValidationError is a client error with a message safe to show (no URL parts).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func bad(msg string) error { return &ValidationError{msg} }

// Validate checks raw for kind and returns the normalised URL to store.
func Validate(kind Kind, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", bad("Webhook URL is required.")
	}
	if len(raw) > maxURLLen {
		return "", bad("Webhook URL is too long.")
	}
	if strings.Contains(raw, "#") {
		return "", bad("Webhook URL must not contain a fragment.")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return "", bad("Not a valid URL.")
	}
	if u.Scheme != "https" {
		return "", bad("Webhook URL must use https.")
	}
	if u.User != nil {
		return "", bad("Webhook URL must not contain credentials.")
	}
	if p := u.Port(); p != "" && p != "443" {
		return "", bad("Webhook URL must use port 443.")
	}
	host := strings.ToLower(u.Hostname())
	switch kind {
	case Discord:
		return validateDiscord(u, host)
	case Teams:
		return validateTeams(u, host)
	}
	return "", bad("Unknown webhook kind.")
}

func validateDiscord(u *url.URL, host string) (string, error) {
	switch host {
	case "discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com":
	default:
		return "", bad("Not a Discord webhook host (expected discord.com).")
	}
	if !discordPath.MatchString(u.EscapedPath()) {
		return "", bad("Not a Discord webhook path (expected /api/webhooks/<id>/<token>).")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", bad("Not a valid URL.")
	}
	for k, v := range q {
		if k != "thread_id" || len(v) != 1 || !digitsRe.MatchString(v[0]) {
			return "", bad("Unexpected query parameter in the Discord URL (only thread_id=<digits> is allowed).")
		}
	}
	out := url.URL{Scheme: "https", Host: "discord.com", Path: u.Path, RawQuery: u.RawQuery}
	return out.String(), nil
}

func validateTeams(u *url.URL, host string) (string, error) {
	if host == "outlook.office.com" || strings.HasSuffix(host, ".webhook.office.com") || strings.HasSuffix(host, ".logic.azure.com") {
		return "", bad("This is a retired URL. Re-copy it from the Teams Workflows app ('Send webhook alerts to a channel', 'Anyone' trigger).")
	}
	if !strings.HasSuffix(host, teamsSuffix) || len(host) == len(teamsSuffix) {
		return "", bad("Not a Teams Workflows host (expected *.environment.api.powerplatform.com).")
	}
	if !strings.HasPrefix(u.Path, "/powerautomate/") {
		return "", bad("Not a Teams Workflows path (expected /powerautomate/...).")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || q.Get("sig") == "" {
		return "", bad("The workflow URL has no sig parameter; copy the full URL.")
	}
	out := url.URL{Scheme: "https", Host: host, Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}
	return out.String(), nil
}

// Mask is the display form: scheme://host/… plus the last 4 characters.
func Mask(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) < 4 {
		return "…"
	}
	return u.Scheme + "://" + u.Host + "/…" + raw[len(raw)-4:]
}

// Redact is the log form: scheme://host/… only.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "…"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// Message is provider-neutral text.
type Message struct {
	Title, Text string
	Expired     bool // colours the Discord embed red instead of orange
}

func discordPayload(m Message) any {
	color := 15105570 // orange
	if m.Expired {
		color = 15548997 // red
	}
	return map[string]any{
		"username":         "CertiFire",
		"allowed_mentions": map[string]any{"parse": []string{}},
		"embeds":           []any{map[string]any{"title": m.Title, "description": m.Text, "color": color}},
	}
}

func teamsPayload(m Message) any {
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"contentUrl":  nil,
			"content": map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body": []any{
					map[string]any{"type": "TextBlock", "text": m.Title, "weight": "Bolder", "size": "Medium", "wrap": true},
					map[string]any{"type": "TextBlock", "text": m.Text, "wrap": true},
				},
			},
		}},
	}
}

// Result is the classified response of one send.
type Result struct {
	Outcome    string
	Message    string
	RetryAfter time.Duration // set for RateLimited when known
}

// Transient reports whether the send should be retried later (not a broken URL).
func (r Result) Transient() bool { return r.Outcome == RateLimited || r.Outcome == Error }

// OK reports whether the provider accepted the message.
func (r Result) OK() bool { return r.Outcome == Delivered || r.Outcome == Accepted }

// Sender posts messages through one shared client.
type Sender struct{ Client *http.Client }

// NewSender returns a Sender with a 10 s timeout that never follows redirects.
func NewSender() *Sender {
	return &Sender{Client: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Send validates rawURL again, posts m and classifies the response. The URL is
// never part of the returned message.
func (s *Sender) Send(ctx context.Context, kind Kind, rawURL string, m Message) Result {
	norm, err := Validate(kind, rawURL)
	if err != nil {
		return Result{Outcome: Broken, Message: "Stored URL is no longer valid: " + err.Error()}
	}
	payload := discordPayload(m)
	if kind == Teams {
		payload = teamsPayload(m)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{Outcome: Error, Message: "Could not build the message."}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, norm, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: Error, Message: "Could not build the request."}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CertiFire")
	resp, err := s.Client.Do(req)
	if err != nil {
		// url.Error would carry the full URL; report only the cause class.
		return Result{Outcome: Error, Message: "Network error: " + netCause(ctx, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return classify(kind, resp, data)
}

func netCause(ctx context.Context, err error) string {
	var ue interface{ Timeout() bool }
	switch {
	case ctx.Err() != nil:
		return "cancelled"
	case errors.As(err, &ue) && ue.Timeout():
		return "timed out"
	}
	return "could not connect"
}

func label(kind Kind) string {
	if kind == Teams {
		return "Teams"
	}
	return "Discord"
}

func classify(kind Kind, resp *http.Response, body []byte) Result {
	name, code := label(kind), resp.StatusCode
	switch {
	case code >= 200 && code < 300:
		if kind == Teams {
			return Result{Outcome: Accepted, Message: "Accepted by Teams Workflows (HTTP " + strconv.Itoa(code) + "). Teams gives no delivery confirmation: if nothing appears in the channel within a minute, check the workflow's run history."}
		}
		return Result{Outcome: Delivered, Message: "Delivered."}
	case code == http.StatusTooManyRequests:
		d := retryAfter(resp.Header.Get("Retry-After"), body)
		msg := name + " rate limit reached."
		if d > 0 {
			msg = fmt.Sprintf("%s rate limit reached; retry in %d s.", name, int(d.Seconds()+0.999))
		}
		return Result{Outcome: RateLimited, Message: msg, RetryAfter: d}
	case code >= 300 && code < 400:
		// Redirects are never followed; a moved webhook URL will not recover.
		return Result{Outcome: Broken, Message: fmt.Sprintf("%s %d: URL moved, re-copy it", name, code)}
	case code >= 500:
		return Result{Outcome: Error, Message: fmt.Sprintf("%s server error %d; will retry later.", name, code)}
	case code >= 400:
		return Result{Outcome: Broken, Message: fmt.Sprintf("%s %d: %s", name, code, reason(kind, code))}
	}
	return Result{Outcome: Error, Message: fmt.Sprintf("%s answered with unexpected status %d.", name, code)}
}

func reason(kind Kind, code int) string {
	switch {
	case kind == Discord && (code == 401 || code == 404):
		return "webhook deleted or URL wrong"
	case kind == Discord && code == 403:
		return "webhook not allowed"
	case kind == Teams && code == 404:
		return "flow deleted or turned off"
	case kind == Teams && (code == 401 || code == 403):
		return "URL signature invalid or the flow requires sign-in (use the 'Anyone' webhook template)"
	}
	return "request rejected"
}

// retryAfter reads the Retry-After header (seconds) or Discord's retry_after body field.
func retryAfter(header string, body []byte) time.Duration {
	if f, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && f > 0 {
		return time.Duration(f * float64(time.Second))
	}
	var b struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &b) == nil && b.RetryAfter > 0 {
		return time.Duration(b.RetryAfter * float64(time.Second))
	}
	return 0
}
