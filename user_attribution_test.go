package sfveritas

import (
	"context"
	"testing"
)

// Tests run with globalConfig == nil, so uaEmit does not arm the debounce timer
// and the buffer is deterministic; uaDrain() returns the events synchronously.

func resetUA() {
	uaActiveMu.Lock()
	uaActive = map[string]map[string]int64{}
	uaActiveMu.Unlock()
	uaBufMu.Lock()
	if uaFlushTimer != nil {
		uaFlushTimer.Stop()
		uaFlushTimer = nil
	}
	uaBuffer = nil
	uaBufMu.Unlock()
}

func actions(events []map[string]interface{}) map[string]string {
	m := map[string]string{} // userId -> last action
	for _, e := range events {
		m[e["userId"].(string)] = e["action"].(string)
	}
	return m
}

func TestUA_NoOpOutsideTrace(t *testing.T) {
	resetUA()
	AddActiveUser(context.Background(), "u1") // no trace, no goroutine-local
	SetActiveUsers(context.Background(), []string{"a", "b"})
	if ev := uaDrain(); len(ev) != 0 {
		t.Fatalf("expected no events outside a trace, got %d", len(ev))
	}
}

func TestUA_AddIsIdempotentAndRemoveCloses(t *testing.T) {
	resetUA()
	ctx := SetTraceID(context.Background(), "trace-A")
	AddActiveUser(ctx, "u1")
	AddActiveUser(ctx, "u1") // idempotent — no second event
	AddActiveUser(ctx, "  ") // blank — no-op
	RemoveActiveUser(ctx, "u1")
	RemoveActiveUser(ctx, "u1") // already closed — no-op
	ev := uaDrain()
	if len(ev) != 2 {
		t.Fatalf("expected exactly 2 events (add, end), got %d: %v", len(ev), ev)
	}
	if ev[0]["action"] != "add" || ev[1]["action"] != "end" {
		t.Errorf("expected add then end, got %v / %v", ev[0]["action"], ev[1]["action"])
	}
	for _, e := range ev {
		if e["sessionId"] != "trace-A" {
			t.Errorf("expected sessionId trace-A, got %v", e["sessionId"])
		}
		if e["source"] != "sdk" {
			t.Errorf("expected source sdk, got %v", e["source"])
		}
		if _, ok := e["tsMs"].(string); !ok {
			t.Errorf("expected tsMs to be a string, got %T", e["tsMs"])
		}
	}
}

func TestUA_SetActiveUsersDiffs(t *testing.T) {
	resetUA()
	ctx := SetTraceID(context.Background(), "trace-B")
	SetActiveUsers(ctx, []string{"a", "b"}) // add a, add b
	SetActiveUsers(ctx, []string{"b", "c"}) // end a, add c (b unchanged)
	got := actions(uaDrain())
	if got["a"] != "end" {
		t.Errorf("expected a ended, got %q", got["a"])
	}
	if got["c"] != "add" {
		t.Errorf("expected c added, got %q", got["c"])
	}
	// b was added in round 1 and untouched in round 2 → last action "add"
	if got["b"] != "add" {
		t.Errorf("expected b add (unchanged across sets), got %q", got["b"])
	}
}

func TestUA_ScopeAndClear(t *testing.T) {
	resetUA()
	ctx := SetTraceID(context.Background(), "trace-C")
	end := ActiveUserScope(ctx, "x")
	end() // closes x
	AddActiveUser(ctx, "y")
	AddActiveUser(ctx, "z")
	ClearActiveUsers(ctx) // ends y and z
	got := actions(uaDrain())
	if got["x"] != "end" || got["y"] != "end" || got["z"] != "end" {
		t.Errorf("expected x/y/z all ended, got %v", got)
	}
	// active set for the trace should be empty now
	uaActiveMu.Lock()
	_, present := uaActive["trace-C"]
	uaActiveMu.Unlock()
	if present {
		t.Error("expected trace-C active set to be cleaned up")
	}
}

func TestUA_CorrelatesToGoroutineLocalTrace(t *testing.T) {
	// With no explicit ctx trace, attribution should still bind to the
	// goroutine-local request trace (the middleware-registered one).
	resetUA()
	gid := curGoroutineID()
	setGoroutineTrace(gid, "goroutine-trace-D")
	defer clearGoroutineTrace(gid)

	AddActiveUser(context.Background(), "u9")
	ev := uaDrain()
	if len(ev) != 1 || ev[0]["sessionId"] != "goroutine-trace-D" {
		t.Fatalf("expected 1 event bound to goroutine-trace-D, got %v", ev)
	}
}
