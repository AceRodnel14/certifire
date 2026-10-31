// Package certgen validates certificate requests and generates self-signed
// certificates with the openssl CLI.
package certgen

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Limits and defaults from the spec.
const (
	MaxHosts = 100
	MaxDays  = 825
)

// Subject holds the optional distinguished-name fields. Empty means omitted.
type Subject struct {
	C  string `json:"C,omitempty"`
	ST string `json:"ST,omitempty"`
	L  string `json:"L,omitempty"`
	O  string `json:"O,omitempty"`
	OU string `json:"OU,omitempty"`
}

// Request is the untrusted input.
type Request struct {
	Name    string
	Hosts   []string
	Bits    int
	Days    int
	Subject Subject
}

// Host is one validated subjectAltName entry.
type Host struct {
	Value string // normalised
	IP    bool
	// SingleLabelWildcard marks *.<single-label> (e.g. *.homelab): accepted,
	// but Chrome rejects it, so the UI warns.
	SingleLabelWildcard bool
}

// Spec is a validated request: safe to render into a .cnf and pass to openssl.
type Spec struct {
	Name    string
	Hosts   []Host
	Bits    int
	Days    int
	Subject Subject
}

// HostValues returns the normalised hosts in order.
func (s Spec) HostValues() []string {
	out := make([]string, len(s.Hosts))
	for i, h := range s.Hosts {
		out[i] = h.Value
	}
	return out
}

// ValidationError is a client error with a message safe to show.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &ValidationError{fmt.Sprintf(format, a...)} }

var (
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	fieldRe = regexp.MustCompile(`^[A-Za-z0-9 .,'()&/+-]{1,64}$`)
	countRe = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// Validate checks r strictly and returns the normalised Spec.
func Validate(r Request) (Spec, error) {
	if !nameRe.MatchString(r.Name) {
		return Spec{}, bad("Name must be 1-64 characters: letters, digits, dot, dash or underscore, starting with a letter or digit.")
	}
	hosts, err := validateHosts(r.Hosts)
	if err != nil {
		return Spec{}, err
	}
	if len(hosts[0].Value) > 64 {
		return Spec{}, bad("The first host becomes the certificate CN and must be at most 64 characters; put a shorter host first.")
	}
	if r.Bits != 2048 && r.Bits != 3072 && r.Bits != 4096 {
		return Spec{}, bad("RSA size must be 2048, 3072 or 4096.")
	}
	if r.Days < 1 || r.Days > MaxDays {
		return Spec{}, bad("Validity must be 1 to %d days.", MaxDays)
	}
	subj, err := validateSubject(r.Subject)
	if err != nil {
		return Spec{}, err
	}
	return Spec{Name: r.Name, Hosts: hosts, Bits: r.Bits, Days: r.Days, Subject: subj}, nil
}

func validateHosts(in []string) ([]Host, error) {
	if len(in) == 0 {
		return nil, bad("At least one host is required.")
	}
	if len(in) > MaxHosts {
		return nil, bad("At most %d hosts are allowed.", MaxHosts)
	}
	seen := map[string]bool{}
	var out []Host
	for _, raw := range in {
		h, err := parseHost(strings.ToLower(strings.TrimSpace(raw)))
		if err != nil {
			return nil, err
		}
		if seen[h.Value] {
			continue
		}
		seen[h.Value] = true
		out = append(out, h)
	}
	return out, nil
}

func parseHost(s string) (Host, error) {
	if s == "" {
		return Host{}, bad("Host must not be empty.")
	}
	if len(s) > 253 {
		return Host{}, bad("Host %q is longer than 253 characters.", clip(s))
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		if addr.Zone() != "" {
			return Host{}, bad("IP address %q must not have a zone.", clip(s))
		}
		return Host{Value: addr.Unmap().String(), IP: true}, nil
	}
	labels := strings.Split(s, ".")
	h := Host{Value: s}
	if labels[0] == "*" {
		if len(labels) < 2 {
			return Host{}, bad("A bare %q is not allowed; use *.<domain>.", "*")
		}
		labels = labels[1:]
		h.SingleLabelWildcard = len(labels) == 1
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return Host{}, bad("Host %q is not a valid DNS name, wildcard (*.domain) or IP address.", clip(s))
		}
	}
	return h, nil
}

// clip keeps error messages short when echoing untrusted input.
func clip(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

func validateSubject(s Subject) (Subject, error) {
	var out Subject
	s.C = strings.TrimSpace(s.C)
	if s.C != "" {
		if !countRe.MatchString(s.C) {
			return out, bad("Country (C) must be exactly 2 letters.")
		}
		out.C = strings.ToUpper(s.C)
	}
	for _, f := range []struct {
		label string
		in    string
		out   *string
	}{{"State (ST)", s.ST, &out.ST}, {"Locality (L)", s.L, &out.L}, {"Organization (O)", s.O, &out.O}, {"Unit (OU)", s.OU, &out.OU}} {
		v := strings.TrimSpace(f.in)
		if v == "" {
			continue
		}
		if !fieldRe.MatchString(v) {
			return out, bad("%s must be 1-64 characters from letters, digits, space and . , ' ( ) & / + -", f.label)
		}
		*f.out = v
	}
	return out, nil
}

// DaysLeft is floor((notAfter - now) / 24h); negative once expired.
// The server and the UI use the same rule.
func DaysLeft(notAfter, now time.Time) int {
	const day = 24 * time.Hour
	d := notAfter.Sub(now)
	n := int(d / day)
	if d%day < 0 {
		n--
	}
	return n
}
