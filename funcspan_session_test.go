package sfveritas

import (
	"sync"
	"testing"
	"time"
)

type deactivateRec struct {
	mu   sync.Mutex
	last string
	snap sessionSnapshot
	n    int
}

func (d *deactivateRec) cb(s sessionSnapshot, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last = reason
	d.snap = s
	d.n++
}

func resetSession(d *deactivateRec) {
	activeSession.Store(nil)
	setSessionDeactivateCallback(d.cb)
}

func futureMs() int64 { return time.Now().UnixMilli() + 60_000 }

func TestSessionActivateUpdateSupersede(t *testing.T) {
	var d deactivateRec
	resetSession(&d)

	if !funcspanSessionActivate("s1", 1, futureMs(), 100, 1000) {
		t.Fatal("activate s1 should succeed")
	}
	if !funcspanSessionIsActive() {
		t.Fatal("expected active")
	}
	// Update same id, newer version — no deactivation.
	if !funcspanSessionActivate("s1", 2, futureMs(), 200, 2000) {
		t.Fatal("update s1 should succeed")
	}
	if d.n != 0 {
		t.Errorf("update should not fire deactivate, got n=%d", d.n)
	}
	// Supersede with a different id — old session deactivates as "superseded".
	if !funcspanSessionActivate("s2", 1, futureMs(), 100, 1000) {
		t.Fatal("activate s2 should succeed")
	}
	if d.last != "superseded" || d.snap.SessionID != "s1" {
		t.Errorf("expected superseded for s1, got reason=%q id=%q", d.last, d.snap.SessionID)
	}
}

func TestSessionRejectExpiredAndMalformed(t *testing.T) {
	var d deactivateRec
	resetSession(&d)
	if funcspanSessionActivate("s", 1, time.Now().UnixMilli()-1, 10, 10) {
		t.Error("expired directive should be rejected")
	}
	if funcspanSessionActivate("", 1, futureMs(), 10, 10) {
		t.Error("empty sessionId should be rejected")
	}
	if funcspanSessionIsActive() {
		t.Error("no session should be active after rejections")
	}
}

func TestSessionSpanBudget(t *testing.T) {
	var d deactivateRec
	resetSession(&d)
	funcspanSessionActivate("s", 1, futureMs(), 3, 1<<30) // maxSpans=3
	recordSpanEmitted(10)
	recordSpanEmitted(10)
	if !funcspanSessionIsActive() {
		t.Fatal("should still be active before budget")
	}
	recordSpanEmitted(10) // 3rd → hits maxSpans
	if funcspanSessionIsActive() {
		t.Error("expected deactivation at span budget")
	}
	if d.last != "span_budget" || d.snap.SpansEmitted != 3 {
		t.Errorf("expected span_budget with 3 spans, got reason=%q spans=%d", d.last, d.snap.SpansEmitted)
	}
}

func TestSessionPayloadBudget(t *testing.T) {
	var d deactivateRec
	resetSession(&d)
	funcspanSessionActivate("s", 1, futureMs(), 1<<30, 100) // maxBytes=100
	recordSpanEmitted(60)
	if !funcspanSessionIsActive() {
		t.Fatal("active before byte budget")
	}
	recordSpanEmitted(60) // 120 >= 100
	if funcspanSessionIsActive() {
		t.Error("expected deactivation at payload budget")
	}
	if d.last != "payload_budget" {
		t.Errorf("expected payload_budget, got %q", d.last)
	}
}

func TestSessionTTL(t *testing.T) {
	var d deactivateRec
	resetSession(&d)
	funcspanSessionActivate("s", 1, time.Now().UnixMilli()+30, 1<<30, 1<<30)
	funcspanSessionCheckTTL() // not yet expired
	if !funcspanSessionIsActive() {
		t.Fatal("should be active before TTL")
	}
	time.Sleep(50 * time.Millisecond)
	funcspanSessionCheckTTL()
	if funcspanSessionIsActive() {
		t.Error("expected TTL deactivation")
	}
	if d.last != "ttl_expired" {
		t.Errorf("expected ttl_expired, got %q", d.last)
	}
}

func TestSessionConcurrentRecord(t *testing.T) {
	var d deactivateRec
	resetSession(&d)
	funcspanSessionActivate("s", 1, futureMs(), 1000, 1<<30)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				recordSpanEmitted(1)
			}
		}()
	}
	wg.Wait()
	// 1000 records hit maxSpans exactly → exactly one deactivation.
	if funcspanSessionIsActive() {
		t.Error("expected deactivation after 1000 spans")
	}
	d.mu.Lock()
	n := d.n
	d.mu.Unlock()
	if n != 1 {
		t.Errorf("expected exactly one deactivation, got %d", n)
	}
}
