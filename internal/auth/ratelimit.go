package auth

import (
	"sync"
	"time"
)

const (
	loginRateLimit  = 5
	loginRateWindow = time.Minute
)

type loginLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (l *loginLimiter) allow(user string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	cutoff := now.Add(-loginRateWindow)
	kept := l.hits[user][:0]
	for _, t := range l.hits[user] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= loginRateLimit {
		l.hits[user] = kept
		return false
	}
	l.hits[user] = append(kept, now)
	return true
}
