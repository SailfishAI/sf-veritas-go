package sfveritas

import (
	"context"
	"fmt"
	"sync"
)

type ctxKey int

const (
	ctxKeyTraceID ctxKey = iota
	ctxKeyPageVisitID
	ctxKeyParentSpanID
	ctxKeyFuncSpanOverride
	ctxKeySuppressChildren
	ctxKeyFuncSpanPropagation
)

// propagationConfig carries a debug rule's capture config down to child spans
// (the Go analog of the Python SDK's thread-local propagation stack). Because Go
// threads context explicitly, propagation is a depth-bounded value on the
// context rather than a stack: each inheriting child decrements remainingDepth.
//
// Limitation vs Python: this only reaches children that are themselves
// instrumented AND receive the parent's span.Context(). It cannot cross a broken
// context chain or StartSpanNoCtx (context.Background()).
type propagationConfig struct {
	ruleID         string
	captureArgs    bool
	captureReturn  bool
	argLimitBytes  int
	retLimitBytes  int
	remainingDepth int
}

// setPropagation returns ctx carrying pc for child spans to inherit.
func setPropagation(ctx context.Context, pc *propagationConfig) context.Context {
	return context.WithValue(ctx, ctxKeyFuncSpanPropagation, pc)
}

// getPropagation returns the inheritable propagation config from ctx, or nil if
// absent or exhausted (remainingDepth <= 0).
func getPropagation(ctx context.Context) *propagationConfig {
	pc, ok := ctx.Value(ctxKeyFuncSpanPropagation).(*propagationConfig)
	if !ok || pc == nil || pc.remainingDepth <= 0 {
		return nil
	}
	return pc
}

// GetOrSetTraceID returns the trace ID from ctx, or generates a new non-session
// trace ID and returns a new context with it set.
func GetOrSetTraceID(ctx context.Context) (context.Context, string) {
	if tid, ok := ctx.Value(ctxKeyTraceID).(string); ok && tid != "" {
		return ctx, tid
	}
	apiKey := ""
	if cfg := getConfig(); cfg != nil {
		apiKey = cfg.apiKey
	}
	tid := fmt.Sprintf("%s-v3/%s/%s", nonsessionApplogs, apiKey, fastUUID())
	return context.WithValue(ctx, ctxKeyTraceID, tid), tid
}

// SetTraceID sets an explicit trace ID in the context (e.g. from an inbound header).
func SetTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxKeyTraceID, traceID)
}

// GetTraceID returns the trace ID from context, or empty string.
func GetTraceID(ctx context.Context) string {
	if tid, ok := ctx.Value(ctxKeyTraceID).(string); ok {
		return tid
	}
	return ""
}

var (
	nonSessionTraceOnce sync.Once
	nonSessionTraceVal  string
)

// nonSessionTraceID returns a process-stable non-session trace ID, used for
// telemetry emitted WITHOUT a request context (a plain slog.Info() / fmt.Println()).
// It is generated once per process so such telemetry groups together, instead of
// minting a fresh request ID per log/print/exception (which orphaned every line and
// fabricated a one-off request for each).
func nonSessionTraceID() string {
	nonSessionTraceOnce.Do(func() {
		apiKey := ""
		if cfg := getConfig(); cfg != nil {
			apiKey = cfg.apiKey
		}
		nonSessionTraceVal = fmt.Sprintf("%s-v3/%s/%s", nonsessionApplogs, apiKey, fastUUID())
	})
	return nonSessionTraceVal
}

// sessionIDFromContext resolves the trace ID for a piece of context-less telemetry
// (a log, print, exception, or identify) in priority order:
//
//  1. An explicit trace on ctx — an inbound HTTP request, or slog.InfoContext(ctx, …)
//     using the request context.
//  2. The goroutine-local trace registered by the inbound middleware for the current
//     handler goroutine — this is what makes a plain slog.Info() / fmt.Println()
//     correlate to the request WITHOUT the customer threading ctx everywhere
//     (see goroutine_local.go). Go's stand-in for Python contextvars / JS
//     AsyncLocalStorage / Java ThreadLocal.
//  3. The process-stable non-session ID, so telemetry emitted with no request at all
//     groups together instead of fabricating a one-off request per emit.
//
// Use this for context-less telemetry EMIT paths only; do NOT use it for the
// inbound/outbound network paths, which must mint a fresh trace per request via
// GetOrSetTraceID.
func sessionIDFromContext(ctx context.Context) string {
	if tid := GetTraceID(ctx); tid != "" {
		return tid
	}
	if tid := currentGoroutineTrace(); tid != "" {
		return tid
	}
	return nonSessionTraceID()
}

// GetOrSetPageVisitID returns the page visit UUID from ctx, or generates one.
func GetOrSetPageVisitID(ctx context.Context) (context.Context, string) {
	if pvid, ok := ctx.Value(ctxKeyPageVisitID).(string); ok && pvid != "" {
		return ctx, pvid
	}
	pvid := fastUUID()
	return context.WithValue(ctx, ctxKeyPageVisitID, pvid), pvid
}

// GetPageVisitID returns the page visit ID from context, or empty string.
func GetPageVisitID(ctx context.Context) string {
	if pvid, ok := ctx.Value(ctxKeyPageVisitID).(string); ok {
		return pvid
	}
	return ""
}

// SetCurrentSpanID sets the current function span ID in context.
func SetCurrentSpanID(ctx context.Context, spanID string) context.Context {
	return context.WithValue(ctx, ctxKeyParentSpanID, spanID)
}

// GetCurrentSpanID returns the current function span ID from context.
func GetCurrentSpanID(ctx context.Context) string {
	if sid, ok := ctx.Value(ctxKeyParentSpanID).(string); ok {
		return sid
	}
	return ""
}

// SetFuncSpanOverride sets the function span capture override header value in context.
func SetFuncSpanOverride(ctx context.Context, override string) context.Context {
	return context.WithValue(ctx, ctxKeyFuncSpanOverride, override)
}

// GetFuncSpanOverride returns the function span capture override from context.
func GetFuncSpanOverride(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyFuncSpanOverride).(string); ok {
		return v
	}
	return ""
}

// SetSuppressChildren sets a flag in the context to suppress child span creation.
// When true, child spans created from this context will be noop.
func SetSuppressChildren(ctx context.Context, suppress bool) context.Context {
	return context.WithValue(ctx, ctxKeySuppressChildren, suppress)
}

// GetSuppressChildren returns whether child spans should be suppressed in this context.
func GetSuppressChildren(ctx context.Context) bool {
	if v, ok := ctx.Value(ctxKeySuppressChildren).(bool); ok {
		return v
	}
	return false
}
