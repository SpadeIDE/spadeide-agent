// Package ratelimit provides a per-key token bucket with exponential lockout
// after consecutive failures (used for /pair and the loopback control API).
package ratelimit

import (
	"log"
	"sync"
	"time"
)

type Config struct {
	Rate       float64       // tokens per second
	Burst      float64       // max tokens
	FailLimit  int           // consecutive failures before lockout
	LockoutMin time.Duration // first lockout duration
	LockoutMax time.Duration // cap
}

type Limiter struct {
	cfg Config
	mu  sync.Mutex
	m   map[string]*bucket
}

type bucket struct {
	tokens     float64
	last       time.Time
	fails      int
	lockedUntil time.Time
	lockouts   int
}

func New(cfg Config) *Limiter {
	if cfg.Rate <= 0 {
		cfg.Rate = 2
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 5
	}
	if cfg.FailLimit <= 0 {
		cfg.FailLimit = 10
	}
	if cfg.LockoutMin <= 0 {
		cfg.LockoutMin = time.Second
	}
	if cfg.LockoutMax <= 0 {
		cfg.LockoutMax = 5 * time.Minute
	}
	return &Limiter{cfg: cfg, m: make(map[string]*bucket)}
}

// Allow returns false when the key is locked out or the bucket is empty.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(key)
	now := time.Now()
	if now.Before(b.lockedUntil) {
		return false
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.cfg.Rate
	if b.tokens > l.cfg.Burst {
		b.tokens = l.cfg.Burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Failure records an auth/pairing failure and may engage lockout.
func (l *Limiter) Failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(key)
	b.fails++
	if b.fails < l.cfg.FailLimit {
		return
	}
	b.lockouts++
	d := l.cfg.LockoutMin
	for i := 1; i < b.lockouts; i++ {
		d *= 2
		if d >= l.cfg.LockoutMax {
			d = l.cfg.LockoutMax
			break
		}
	}
	b.lockedUntil = time.Now().Add(d)
	b.fails = 0
	log.Printf("ratelimit: lockout key=%s for %s (strike %d)", key, d, b.lockouts)
}

// Success resets the consecutive-failure counter (not the lockout history).
func (l *Limiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(key)
	b.fails = 0
}

func (l *Limiter) get(key string) *bucket {
	if key == "" {
		key = "unknown"
	}
	b, ok := l.m[key]
	if !ok {
		b = &bucket{tokens: l.cfg.Burst, last: time.Now()}
		l.m[key] = b
	}
	return b
}
