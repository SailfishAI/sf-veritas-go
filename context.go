package sfveritas

import (
	"context"
	"fmt"
)

type ctxKey int

const (
	ctxKeyTraceID ctxKey = iota
	ctxKeyPageVisitID
	ctxKeyParentSpanID
	ctxKeyFuncSpanOverride
	ctxKeySuppressChildren
)

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
