package sfveritas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCurGoroutineID_NonZeroAndStablePerGoroutine(t *testing.T) {
	a := curGoroutineID()
	b := curGoroutineID()
	if a == 0 {
		t.Fatal("expected a non-zero goroutine ID")
	}
	if a != b {
		t.Errorf("expected stable goroutine ID within the same goroutine, got %d then %d", a, b)
	}

	var other int64
	done := make(chan struct{})
	go func() {
		other = curGoroutineID()
		close(done)
	}()
	<-done
	if other == a {
		t.Errorf("expected a different goroutine ID on another goroutine, both were %d", a)
	}
}

func TestSessionIDFromContext_UsesGoroutineLocalTrace(t *testing.T) {
	// Simulate the middleware registering the request trace for this goroutine,
	// then a plain context-less emit (slog.Info) resolving to it — no ctx threaded.
	gid := curGoroutineID()
	setGoroutineTrace(gid, "goroutine-req-trace")
	defer clearGoroutineTrace(gid)

	if got := sessionIDFromContext(context.Background()); got != "goroutine-req-trace" {
		t.Errorf("expected goroutine-local trace, got %s", got)
	}
}

func TestSessionIDFromContext_ExplicitCtxBeatsGoroutineLocal(t *testing.T) {
	gid := curGoroutineID()
	setGoroutineTrace(gid, "goroutine-req-trace")
	defer clearGoroutineTrace(gid)

	ctx := SetTraceID(context.Background(), "explicit-trace")
	if got := sessionIDFromContext(ctx); got != "explicit-trace" {
		t.Errorf("expected explicit ctx trace to win, got %s", got)
	}
}

func TestSessionIDFromContext_FallsBackWhenGoroutineCleared(t *testing.T) {
	gid := curGoroutineID()
	setGoroutineTrace(gid, "goroutine-req-trace")
	clearGoroutineTrace(gid)

	// After the handler returns and the trace is cleared, a later emit on this
	// goroutine must fall back to the stable non-session ID, not a stale trace.
	if got := sessionIDFromContext(context.Background()); got != nonSessionTraceID() {
		t.Errorf("expected non-session fallback after clear, got %s", got)
	}
}

func TestMiddleware_PlainLogCorrelatesToRequestTrace(t *testing.T) {
	// End-to-end: a handler emitting context-less telemetry (simulating a plain
	// slog.Info — context.Background(), no threaded ctx) must resolve to the
	// inbound request's trace, proving the middleware's goroutine-local registration.
	prev := globalConfig
	globalConfig = &config{apiKey: "test-key"}
	defer func() { globalConfig = prev }()

	const wantTrace = "inbound-rid-12345"

	var sawInHandler string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No ctx threaded — exactly what a plain slog.Info(...) gives the handler.
		sawInHandler = sessionIDFromContext(context.Background())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/some-path", nil)
	req.Header.Set(tracingHeader, wantTrace)
	rec := httptest.NewRecorder()

	Middleware(handler).ServeHTTP(rec, req)

	if sawInHandler != wantTrace {
		t.Errorf("expected context-less emit in handler to correlate to %q, got %q", wantTrace, sawInHandler)
	}

	// After the request completes, the goroutine-local trace must be cleared so a
	// later emit on this (reused) goroutine does not inherit a stale request trace.
	if got := currentGoroutineTrace(); got != "" {
		t.Errorf("expected goroutine-local trace cleared after request, got %q", got)
	}
}

func TestGoroutineLocalTrace_IsolatedPerGoroutine(t *testing.T) {
	// A trace registered on one goroutine must not leak into another.
	gid := curGoroutineID()
	setGoroutineTrace(gid, "main-trace")
	defer clearGoroutineTrace(gid)

	var childSaw string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		childSaw = currentGoroutineTrace()
	}()
	wg.Wait()

	if childSaw != "" {
		t.Errorf("expected child goroutine to see no registered trace, got %q", childSaw)
	}
}
