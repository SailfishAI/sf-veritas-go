package sfveritas

import (
	"context"
	"strings"
	"testing"
)

func TestSetAndGetTraceID(t *testing.T) {
	ctx := context.Background()
	ctx = SetTraceID(ctx, "trace-123")
	got := GetTraceID(ctx)
	if got != "trace-123" {
		t.Errorf("expected trace-123, got %s", got)
	}
}

func TestGetTraceID_Empty(t *testing.T) {
	ctx := context.Background()
	got := GetTraceID(ctx)
	if got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}

func TestGetOrSetTraceID_GeneratesNew(t *testing.T) {
	ctx := context.Background()
	newCtx, traceID := GetOrSetTraceID(ctx)
	if traceID == "" {
		t.Error("expected non-empty generated trace ID")
	}
	if !strings.HasPrefix(traceID, nonsessionApplogs) {
		t.Errorf("expected trace ID to start with %q, got %s", nonsessionApplogs, traceID)
	}
	// Verify it's stored in context
	got := GetTraceID(newCtx)
	if got != traceID {
		t.Errorf("expected %s, got %s", traceID, got)
	}
}

func TestGetOrSetTraceID_ReusesExisting(t *testing.T) {
	ctx := context.Background()
	ctx = SetTraceID(ctx, "existing-trace")
	_, traceID := GetOrSetTraceID(ctx)
	if traceID != "existing-trace" {
		t.Errorf("expected existing-trace, got %s", traceID)
	}
}

func TestNonSessionTraceID_StableAcrossCalls(t *testing.T) {
	// Context-less telemetry must share ONE process-stable non-session ID, not
	// mint a fresh request ID per call (which orphaned every log/print/span).
	a := nonSessionTraceID()
	b := nonSessionTraceID()
	if a == "" {
		t.Fatal("expected non-empty non-session trace ID")
	}
	if a != b {
		t.Errorf("expected stable non-session ID across calls, got %q then %q", a, b)
	}
	if !strings.HasPrefix(a, nonsessionApplogs) {
		t.Errorf("expected non-session ID to start with %q, got %s", nonsessionApplogs, a)
	}
}

func TestSessionIDFromContext_UsesRequestTrace(t *testing.T) {
	// When the context carries a request trace (inbound HTTP, or slog.InfoContext
	// with the request ctx), telemetry must link to that trace.
	ctx := SetTraceID(context.Background(), "req-trace-xyz")
	if got := sessionIDFromContext(ctx); got != "req-trace-xyz" {
		t.Errorf("expected req-trace-xyz, got %s", got)
	}
}

func TestSessionIDFromContext_FallsBackToStableNonSession(t *testing.T) {
	// Without a request trace, two independent context-less emits must resolve to
	// the SAME id so they group together instead of fabricating one request each.
	a := sessionIDFromContext(context.Background())
	b := sessionIDFromContext(context.Background())
	if a != b {
		t.Errorf("expected both context-less emits to share the non-session ID, got %q and %q", a, b)
	}
	if a != nonSessionTraceID() {
		t.Errorf("expected fallback to equal nonSessionTraceID(), got %s", a)
	}
}

func TestSessionIDFromContext_DoesNotMutateContext(t *testing.T) {
	// Unlike GetOrSetTraceID, the read-only helper must not write a trace into ctx.
	ctx := context.Background()
	_ = sessionIDFromContext(ctx)
	if got := GetTraceID(ctx); got != "" {
		t.Errorf("expected ctx to remain trace-less, got %s", got)
	}
}

func TestSetAndGetCurrentSpanID(t *testing.T) {
	ctx := context.Background()
	ctx = SetCurrentSpanID(ctx, "span-abc")
	got := GetCurrentSpanID(ctx)
	if got != "span-abc" {
		t.Errorf("expected span-abc, got %s", got)
	}
}

func TestGetCurrentSpanID_Empty(t *testing.T) {
	ctx := context.Background()
	got := GetCurrentSpanID(ctx)
	if got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}

func TestSetAndGetPageVisitID(t *testing.T) {
	ctx := context.Background()
	newCtx, pvid := GetOrSetPageVisitID(ctx)
	if pvid == "" {
		t.Error("expected non-empty page visit ID")
	}
	// Should reuse on second call
	_, pvid2 := GetOrSetPageVisitID(newCtx)
	if pvid2 != pvid {
		t.Errorf("expected reuse of %s, got %s", pvid, pvid2)
	}
}

func TestGetPageVisitID_Empty(t *testing.T) {
	ctx := context.Background()
	got := GetPageVisitID(ctx)
	if got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}

func TestSetAndGetFuncSpanOverride(t *testing.T) {
	ctx := context.Background()
	ctx = SetFuncSpanOverride(ctx, "1-1-5-10-1-0.5-1-1-1")
	got := GetFuncSpanOverride(ctx)
	if got != "1-1-5-10-1-0.5-1-1-1" {
		t.Errorf("expected override string, got %s", got)
	}
}

func TestGetFuncSpanOverride_Empty(t *testing.T) {
	ctx := context.Background()
	got := GetFuncSpanOverride(ctx)
	if got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}

func TestSetAndGetSuppressChildren(t *testing.T) {
	ctx := context.Background()
	if GetSuppressChildren(ctx) {
		t.Error("expected false by default")
	}
	ctx = SetSuppressChildren(ctx, true)
	if !GetSuppressChildren(ctx) {
		t.Error("expected true after setting")
	}
	ctx = SetSuppressChildren(ctx, false)
	if GetSuppressChildren(ctx) {
		t.Error("expected false after unsetting")
	}
}
