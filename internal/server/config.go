package server

import (
	"cmp"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
)

// Config comes only from environment variables (documented in .env.example).
type Config struct {
	Port        string
	BaseURL     string
	DatabaseURL string
	LogLevel    slog.Level
	// BackupAllIPs are the direct callers allowed to POST /api/backup-all
	// (masked prefixes); empty disables the endpoint.
	BackupAllIPs []netip.Prefix
}

// SecureCookie reports whether session cookies get the Secure flag.
func (c Config) SecureCookie() bool { return strings.HasPrefix(c.BaseURL, "https://") }

// LoadConfig reads the config through getenv (os.Getenv, or a map in tests).
// DATABASE_URL may be empty: the server still starts and reports /readyz 503.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:        cmp.Or(getenv("PORT"), "8080"),
		BaseURL:     getenv("BASE_URL"),
		DatabaseURL: getenv("DATABASE_URL"),
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:" + cfg.Port
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(cmp.Or(getenv("LOG_LEVEL"), "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	ips, err := parsePrefixes(getenv("BACKUP_ALL_ALLOWED_IPS"))
	if err != nil {
		return Config{}, fmt.Errorf("BACKUP_ALL_ALLOWED_IPS: %w", err)
	}
	cfg.BackupAllIPs = ips
	return cfg, nil
}

// parsePrefixes parses a comma-separated list of IPs and CIDRs; a bare IP
// becomes /32 or /128. IPv4-mapped IPv6 entries are stored as IPv4.
func parsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", part)
			}
			p = p.Masked()
			if p.Addr().Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP or CIDR %q", part)
		}
		a = a.Unmap().WithZone("")
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
