package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newLimiter() (*Limiter, *clock) {
	c := &clock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	return New(c, Rule{MaxFailures: 3, Window: 10 * time.Minute}, Rule{MaxFailures: 5, Window: 10 * time.Minute}), c
}

func TestBlocksAfterMaxFailures(t *testing.T) {
	l, _ := newLimiter()
	key := "acct:20240001|192.0.2.1"
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow(key); !ok {
			t.Fatalf("blocked after %d failures, limit is 3", i)
		}
		l.Fail(key)
	}
	ok, wait := l.Allow(key)
	if ok || wait <= 0 || wait > 10*time.Minute {
		t.Fatalf("ok=%v wait=%v, want blocked with 0 < wait <= 10m", ok, wait)
	}
}

// Production defaults: 5 failures per (account, IP) and 150 per IP, 15 minutes.
func campusLimiter() (*Limiter, *clock) {
	c := &clock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	return New(c, Rule{MaxFailures: 5, Window: 15 * time.Minute}, Rule{MaxFailures: 150, Window: 15 * time.Minute}), c
}

// Many students behind one campus NAT address must not lock each other out.
func TestManyUsersBehindOneIPAreNotBlocked(t *testing.T) {
	l, _ := campusLimiter()
	const natIP = "ip:203.0.113.7"
	for i := 0; i < 100; i++ { // 100 different students each mistype once
		acct := fmt.Sprintf("acct:2024%04d|203.0.113.7", i)
		for _, k := range []string{natIP, acct} {
			if ok, _ := l.Allow(k); !ok {
				t.Fatalf("student %d blocked after only %d aggregated failures from the shared IP", i, i)
			}
			l.Fail(k)
		}
	}
	if ok, _ := l.Allow(natIP); !ok {
		t.Fatal("100 aggregated failures must not block the shared IP (limit is 150)")
	}
	// The same student retrying is still limited by the account rule.
	acct := "acct:20240001|203.0.113.7"
	for i := 0; i < 4; i++ {
		l.Fail(acct)
	}
	if ok, _ := l.Allow(acct); ok {
		t.Fatal("5 failures for one account from one IP must block that pair")
	}
	// A genuinely abusive IP is still stopped.
	for i := 0; i < 50; i++ {
		l.Fail(natIP)
	}
	if ok, _ := l.Allow(natIP); ok {
		t.Fatal("150 failures from one IP must block it")
	}
}

func TestRetryAfterCountsDownFromFirstFailure(t *testing.T) {
	l, c := campusLimiter()
	key := "acct:20240001|203.0.113.7"
	l.Fail(key) // window starts here
	c.advance(2 * time.Minute)
	for i := 0; i < 4; i++ {
		l.Fail(key)
	}
	ok, wait := l.Allow(key)
	if ok || wait != 13*time.Minute {
		t.Fatalf("ok=%v wait=%v, want blocked for exactly 13m (15m window - 2m elapsed)", ok, wait)
	}
	c.advance(10 * time.Minute)
	if _, wait = l.Allow(key); wait != 3*time.Minute {
		t.Fatalf("wait = %v, want 3m", wait)
	}
	c.advance(3 * time.Minute)
	if ok, _ := l.Allow(key); !ok {
		t.Fatal("must be allowed again exactly when the window ends")
	}
}

func TestUnblocksWhenWindowEnds(t *testing.T) {
	l, c := newLimiter()
	key := "acct:x|ip"
	for i := 0; i < 3; i++ {
		l.Fail(key)
	}
	c.advance(9 * time.Minute)
	if ok, _ := l.Allow(key); ok {
		t.Fatal("still inside window, should be blocked")
	}
	c.advance(2 * time.Minute)
	if ok, _ := l.Allow(key); !ok {
		t.Fatal("window ended, should be allowed")
	}
	l.Fail(key) // counting restarts from scratch
	if ok, _ := l.Allow(key); !ok {
		t.Fatal("one failure after expiry must not block")
	}
}

func TestResetClearsFailures(t *testing.T) {
	l, _ := newLimiter()
	key := "acct:x|ip"
	l.Fail(key)
	l.Fail(key)
	l.Reset(key)
	l.Fail(key)
	l.Fail(key)
	if ok, _ := l.Allow(key); !ok {
		t.Fatal("reset should have cleared earlier failures")
	}
}

func TestIPAndAccountRulesAndIndependence(t *testing.T) {
	l, _ := newLimiter()
	for i := 0; i < 4; i++ {
		l.Fail("ip:192.0.2.1")
	}
	if ok, _ := l.Allow("ip:192.0.2.1"); !ok {
		t.Fatal("ip rule allows 5 failures; 4 must not block")
	}
	l.Fail("ip:192.0.2.1")
	if ok, _ := l.Allow("ip:192.0.2.1"); ok {
		t.Fatal("5th ip failure should block")
	}
	if ok, _ := l.Allow("ip:192.0.2.2"); !ok {
		t.Fatal("other IP must be unaffected")
	}
	if ok, _ := l.Allow("acct:20240001|192.0.2.9"); !ok {
		t.Fatal("unrelated account key must be unaffected")
	}
}

func TestExpiredEntriesAreCollected(t *testing.T) {
	l, c := newLimiter()
	for i := 0; i < 100; i++ {
		l.Fail(fmt.Sprintf("acct:%d|ip", i))
	}
	if l.Len() != 100 {
		t.Fatalf("len = %d", l.Len())
	}
	c.advance(11 * time.Minute)
	l.Allow("anything") // triggers gc
	if l.Len() != 0 {
		t.Fatalf("len = %d after expiry, want 0", l.Len())
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	l, _ := newLimiter()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("acct:%d|ip", i%4)
			for j := 0; j < 50; j++ {
				l.Allow(key)
				l.Fail(key)
				if j%10 == 0 {
					l.Reset(key)
				}
			}
		}(i)
	}
	wg.Wait()
}
