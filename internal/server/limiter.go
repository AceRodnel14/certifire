package server

import (
	"sync"
	"time"
)

const (
	maxFails   = 5
	failWindow = 5 * time.Minute
)

// sem is a non-blocking counting semaphore bounding concurrent Argon2 work.
type sem chan struct{}

func (s sem) tryAcquire() bool {
	select {
	case s <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s sem) release() { <-s }

// limiter is a sliding-window failed-attempt limiter.
type limiter struct {
	mu     sync.Mutex
	fails  map[string][]time.Time
	max    int
	window time.Duration
	now    func() time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{fails: map[string][]time.Time{}, max: max, window: window, now: time.Now}
}

// take records an attempt unless key is already at its limit, as one atomic
// step, so concurrent guesses see each other before any slow work. It reports
// whether the attempt may proceed. Successful attempts call release.
func (l *limiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.prune(key)
	if len(cur) >= l.max {
		return false
	}
	l.fails[key] = append(cur, l.now())
	return true
}

// release undoes the most recent take.
func (l *limiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur := l.prune(key); len(cur) > 0 {
		cur = cur[:len(cur)-1]
		if len(cur) == 0 {
			delete(l.fails, key)
		} else {
			l.fails[key] = cur
		}
	}
}

// sweep drops keys whose attempts have all expired.
func (l *limiter) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.fails {
		l.prune(k)
	}
}

func (l *limiter) prune(key string) []time.Time {
	keep := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if l.now().Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = keep
	return keep
}
