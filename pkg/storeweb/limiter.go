package storeweb

import (
	"sync"
	"time"
)

// limiter counts the password attempts per user name. Every attempt counts
// when it starts, so parallel requests cannot pass the limit together; a
// successful one forgets the attempts of that name.
type limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	tries     map[string][]time.Time // starts within window, oldest first
	lastSweep time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, now: time.Now, tries: map[string][]time.Time{}}
}

// take records an attempt for name. If max attempts already started within
// window, it records nothing and returns how long to wait instead.
func (l *limiter) take(name string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) > l.window {
		for k, ts := range l.tries {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.window {
				delete(l.tries, k)
			}
		}
		l.lastSweep = now
	}
	ts := l.tries[name]
	for len(ts) > 0 && now.Sub(ts[0]) >= l.window {
		ts = ts[1:]
	}
	if len(ts) >= l.max {
		l.tries[name] = ts
		return ts[0].Add(l.window).Sub(now)
	}
	l.tries[name] = append(ts, now)
	return 0
}

// reset forgets the attempts of name.
func (l *limiter) reset(name string) {
	l.mu.Lock()
	delete(l.tries, name)
	l.mu.Unlock()
}
