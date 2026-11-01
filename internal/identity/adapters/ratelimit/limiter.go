// Package ratelimit is an in-memory implementation of ports.LoginLimiter.
// State lives in one process: with several API instances each would count
// separately, so swap it for a shared-store adapter before scaling out.
package ratelimit

import (
	"sync"
	"time"

	"github.com/edutrak/backend/internal/identity/ports"
)

// Rule limits failures per key inside a window.
type Rule struct {
	MaxFailures int
	Window      time.Duration
}

// Limiter applies a different Rule depending on the key prefix: keys starting
// with "ip:" use IP, every other key uses Account. Once a key reaches
// MaxFailures it is blocked until its window (counted from the first failure)
// ends; the window then restarts from scratch.
type Limiter struct {
	clock   ports.Clock
	account Rule
	ip      Rule

	mu      sync.Mutex
	entries map[string]*entry
	lastGC  time.Time
}

type entry struct {
	failures    int
	windowStart time.Time
	window      time.Duration
}

func New(clock ports.Clock, account, ip Rule) *Limiter {
	return &Limiter{clock: clock, account: account, ip: ip, entries: make(map[string]*entry)}
}

func (l *Limiter) rule(key string) Rule {
	if len(key) >= 3 && key[:3] == "ip:" {
		return l.ip
	}
	return l.account
}

func (l *Limiter) Allow(key string) (bool, time.Duration) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(now)

	e, ok := l.entries[key]
	if !ok {
		return true, 0
	}
	if !now.Before(e.windowStart.Add(e.window)) {
		delete(l.entries, key)
		return true, 0
	}
	if e.failures >= l.rule(key).MaxFailures {
		return false, e.windowStart.Add(e.window).Sub(now)
	}
	return true, 0
}

func (l *Limiter) Fail(key string) {
	now := l.clock.Now()
	rule := l.rule(key)
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok || !now.Before(e.windowStart.Add(e.window)) {
		e = &entry{windowStart: now, window: rule.Window}
		l.entries[key] = e
	}
	e.failures++
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// gc drops expired entries at most once a minute so the map cannot grow without bound.
// Callers hold l.mu.
func (l *Limiter) gc(now time.Time) {
	if now.Sub(l.lastGC) < time.Minute {
		return
	}
	l.lastGC = now
	for k, e := range l.entries {
		if !now.Before(e.windowStart.Add(e.window)) {
			delete(l.entries, k)
		}
	}
}

// Len returns the number of tracked keys (for tests and metrics).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
