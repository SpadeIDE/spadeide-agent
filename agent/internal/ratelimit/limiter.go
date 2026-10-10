// Package ratelimit provides a per-key token bucket with exponential lockout
// after consecutive failures (used for /pair and the loopback control API).
package ratelimit

import (
	"log"
	"sync"
	"time"
)

type Config struct {
	Rate        float64       // tokens per second
	Burst       float64       // max tokens
	FailLimit   int           // consecutive failures before lockout
	LockoutMin  time.Duration // first lockout duration
	LockoutMax  time.Duration // cap
	StrikeDecay time.Duration // after this idle, lockouts resets to 0
	MaxKeys     int           // hard cap on map size (0 = 10_000)
}

type Limiter struct {
	cfg        Config
	mu         sync.Mutex
	m          map[string]*bucket
	ops        int
	sweepEvery int
}

type bucket struct {
	tokens      float64
	last        time.Time
	fails       int
	lockedUntil time.Time
	lockouts    int
	lastTouch   time.Time
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
	if cfg.StrikeDecay <= 0 {
		cfg.StrikeDecay = cfg.LockoutMax
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = 10_000
	}
	return &Limiter{cfg: cfg, m: make(map[string]*bucket), sweepEvery: 64}
}

// Allow returns false when the key is locked out or the bucket is empty.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maybeSweepLocked()
	b := l.get(key)
	now := time.Now()
	b.lastTouch = now
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
	now := time.Now()
	if l.cfg.StrikeDecay > 0 && b.lockouts > 0 && now.Sub(b.lastTouch) > l.cfg.StrikeDecay {
		b.lockouts = 0
	}
	b.lastTouch = now
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
	b.lockedUntil = now.Add(d)
	b.fails = 0
	log.Printf("ratelimit: lockout key=%s for %s (strike %d)", key, d, b.lockouts)
}

// Success resets the consecutive-failure counter (not the lockout history).
func (l *Limiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(key)
	b.fails = 0
	b.lastTouch = time.Now()
}

func (l *Limiter) get(key string) *bucket {
	if key == "" {
		key = "unknown"
	}
	b, ok := l.m[key]
	if !ok {
		if len(l.m) >= l.cfg.MaxKeys {
			l.evictOldestLocked()
		}
		b = &bucket{tokens: l.cfg.Burst, last: time.Now(), lastTouch: time.Now()}
		l.m[key] = b
	}
	return b
}

func (l *Limiter) maybeSweepLocked() {
	l.ops++
	if l.ops < l.sweepEvery {
		return
	}
	l.ops = 0
	cutoff := time.Now().Add(-l.cfg.LockoutMax)
	for k, b := range l.m {
		if b.lastTouch.Before(cutoff) && time.Now().After(b.lockedUntil) {
			delete(l.m, k)
		}
	}
}

func (l *Limiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	first := true
	for k, b := range l.m {
		if first || b.lastTouch.Before(oldest) {
			oldestKey = k
			oldest = b.lastTouch
			first = false
		}
	}
	if oldestKey != "" {
		delete(l.m, oldestKey)
	}
}
