package reminder

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTZ    = "UTC"
	defaultTime  = "00:00"
	defaultSteps = "7,5,3,1,0"
	maxStep      = 365
)

// Config is the reminder schedule, read once from REMINDER_* variables.
type Config struct {
	TZ        string         // REMINDER_TZ name, shown in messages
	Loc       *time.Location // REMINDER_TZ
	Hour, Min int            // REMINDER_TIME in Loc
	Steps     []int          // REMINDER_STEPS, days before expiry, sorted descending
	LocalCron bool           // REMINDER_LOCAL_CRON: run the in-app scheduler
}

// LoadConfig reads and validates the REMINDER_* variables through getenv
// (os.Getenv, or a map in tests). Unset or empty variables take their
// defaults, which are valid.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{TZ: cmp.Or(getenv("REMINDER_TZ"), defaultTZ)}
	var err error
	if cfg.Loc, err = time.LoadLocation(cfg.TZ); err != nil {
		return Config{}, fmt.Errorf("REMINDER_TZ: unknown time zone %q", cfg.TZ)
	}
	at, err := time.Parse("15:04", cmp.Or(getenv("REMINDER_TIME"), defaultTime))
	if err != nil {
		return Config{}, fmt.Errorf("REMINDER_TIME: want HH:MM (24 hour), got %q", getenv("REMINDER_TIME"))
	}
	cfg.Hour, cfg.Min = at.Hour(), at.Minute()
	if cfg.Steps, err = parseSteps(cmp.Or(getenv("REMINDER_STEPS"), defaultSteps)); err != nil {
		return Config{}, fmt.Errorf("REMINDER_STEPS: %w", err)
	}
	cfg.LocalCron = true
	if v := getenv("REMINDER_LOCAL_CRON"); v != "" {
		if cfg.LocalCron, err = strconv.ParseBool(v); err != nil {
			return Config{}, fmt.Errorf("REMINDER_LOCAL_CRON: want true or false, got %q", v)
		}
	}
	return cfg, nil
}

// parseSteps parses a comma-separated list of distinct integers 0-365 and
// returns it sorted descending.
func parseSteps(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not a whole number", part)
		}
		if n < 0 || n > maxStep {
			return nil, fmt.Errorf("%d is outside 0-%d", n, maxStep)
		}
		if slices.Contains(out, n) {
			return nil, fmt.Errorf("%d is listed twice", n)
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b int) int { return b - a })
	return out, nil
}

// Window is how many days ahead of now the store must look for candidates: a
// cert whose expiry date is the largest step away can expire up to a day and
// a bit later than that, so max(step) + 2.
func (c Config) Window() int { return c.Steps[0] + 2 }
