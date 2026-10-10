package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestAllowRefillsOverTime(t *testing.T) {
	t.Parallel()
	l := New(Config{
		Rate:      100,
		Burst:     1,
		FailLimit: 100,
	})
	if !l.Allow("k") {
		t.Fatal("first Allow should succeed")
	}
	if l.Allow("k") {
		t.Fatal("second Allow should fail on empty bucket")
	}
	time.Sleep(20 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("Allow should succeed after refill")
	}
}

func TestLockoutLadder(t *testing.T) {
	t.Parallel()
	l := New(Config{
		Rate:       100,
		Burst:      10,
		FailLimit:  1,
		LockoutMin: 25 * time.Millisecond,
		LockoutMax: 200 * time.Millisecond,
	})

	l.Failure("ip")
	if l.Allow("ip") {
		t.Fatal("expected lockout after first failure (FailLimit=1)")
	}
	time.Sleep(35 * time.Millisecond)
	if !l.Allow("ip") {
		t.Fatal("expected lockout to expire")
	}

	l.Failure("ip")
	if l.Allow("ip") {
		t.Fatal("expected second lockout")
	}
	// Second strike doubles min lockout (~50ms); should still be locked at 30ms.
	time.Sleep(30 * time.Millisecond)
	if l.Allow("ip") {
		t.Fatal("expected longer second lockout")
	}
	time.Sleep(35 * time.Millisecond)
	if !l.Allow("ip") {
		t.Fatal("expected second lockout to expire")
	}
}

func TestStrikeDecayResetsLockoutHistory(t *testing.T) {
	t.Parallel()
	const (
		lockMin   = 15 * time.Millisecond
		strikeDec = 40 * time.Millisecond
	)
	l := New(Config{
		Rate:        100,
		Burst:       10,
		FailLimit:   2,
		LockoutMin:  lockMin,
		LockoutMax:  200 * time.Millisecond,
		StrikeDecay: strikeDec,
	})

	l.Failure("k")
	l.Failure("k")
	if l.Allow("k") {
		t.Fatal("expected lockout after two failures")
	}
	// Wait out lockout, then idle without touching the key so StrikeDecay applies.
	time.Sleep(lockMin + 10*time.Millisecond)
	time.Sleep(strikeDec + 15*time.Millisecond)

	l.Failure("k")
	l.Failure("k")
	if l.Allow("k") {
		t.Fatal("expected lockout after failures post-decay")
	}
	// First strike duration again (~15ms), not doubled (~30ms).
	time.Sleep(20 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("expected first-strike lockout duration after decay")
	}
}

func TestSweepDropsIdleKeys(t *testing.T) {
	t.Parallel()
	const idle = 35 * time.Millisecond
	l := New(Config{
		Rate:       0.001,
		Burst:      1,
		FailLimit:  100,
		LockoutMax: 30 * time.Millisecond,
		MaxKeys:    10_000,
	})

	if !l.Allow("stale") {
		t.Fatal("first Allow on stale key")
	}
	if l.Allow("stale") {
		t.Fatal("bucket should be empty")
	}
	time.Sleep(idle)

	for i := 0; i < 65; i++ {
		l.Allow(fmt.Sprintf("sweep-%d", i))
	}
	if !l.Allow("stale") {
		t.Fatal("expected fresh bucket after sweep removed stale key")
	}
}

func TestSuccessClearsConsecutiveFailures(t *testing.T) {
	t.Parallel()
	l := New(Config{
		Rate:       100,
		Burst:      10,
		FailLimit:  3,
		LockoutMin: time.Second,
	})
	l.Failure("k")
	l.Failure("k")
	l.Success("k")
	l.Failure("k")
	if !l.Allow("k") {
		t.Fatal("Success should reset fails so one failure does not lock out")
	}
}
