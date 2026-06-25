package sfveritas

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Universal user-attribution API — the Go analog of the Python SDK's
// sf_veritas.user_attribution. A backend trace (an inbound HTTP request, a
// queue consumer, ...) has no inherent user; instead it carries event-sourced
// attribution intervals: 0+ users active over distinct time slices, e.g.
// 14:00-14:05 -> {A}, 14:05-14:10 -> {A,B}. Each Add/Remove/Set diffs against
// the current active set for the trace and enqueues add/end interval events,
// debounced (~50ms) and flushed as ONE batched CollectUserAttribution mutation
// (events sent as a JSON array variable — a single GraphQL operation, not a
// top-level array of operations).
//
// Attribution is always explicit and trace-scoped: a call whose context carries
// no Sailfish trace (no inbound request and no goroutine-local request trace)
// is a no-op. The active set is keyed by the trace's session id rather than
// stored on the context, because Go's context is immutable and Add/Remove must
// mutate the set across calls — this mirrors the per-trace semantics of the
// Python contextvar.

const uaDebounce = 50 * time.Millisecond

var (
	uaActiveMu sync.Mutex
	uaActive   = map[string]map[string]int64{} // sessionId -> userId -> intervalStartMs

	uaBufMu      sync.Mutex
	uaBuffer     []map[string]interface{}
	uaFlushTimer *time.Timer
)

// uaSession resolves the trace/session id for attribution, or "" outside a trace.
func uaSession(ctx context.Context) string {
	if tid := GetTraceID(ctx); tid != "" {
		return tid
	}
	return currentGoroutineTrace()
}

func uaNorm(userID string) string {
	return strings.TrimSpace(userID)
}

// uaEmit enqueues one interval event and arms the debounce flush. The timer is
// only armed once the SDK is configured — without config a flush is a no-op, so
// buffering (and unit tests) stay deterministic until SetupInterceptors runs.
func uaEmit(session, action, userID string, tsMs int64) {
	uaBufMu.Lock()
	uaBuffer = append(uaBuffer, map[string]interface{}{
		"sessionId": session,
		"userId":    userID,
		"action":    action,
		"tsMs":      strconv.FormatInt(tsMs, 10),
		"source":    "sdk",
	})
	if uaFlushTimer == nil && getConfig() != nil {
		uaFlushTimer = time.AfterFunc(uaDebounce, FlushUserAttribution)
	}
	uaBufMu.Unlock()
}

// uaDrain stops the pending timer and returns + clears the buffered events.
func uaDrain() []map[string]interface{} {
	uaBufMu.Lock()
	if uaFlushTimer != nil {
		uaFlushTimer.Stop()
		uaFlushTimer = nil
	}
	events := uaBuffer
	uaBuffer = nil
	uaBufMu.Unlock()
	return events
}

// FlushUserAttribution forces a synchronous send of any buffered attribution
// events. Call it on trace/session end so still-open intervals' end events are
// not lost before the debounce timer fires.
func FlushUserAttribution() {
	events := uaDrain()
	if len(events) == 0 || getConfig() == nil {
		return
	}
	vars := mergeVariables(map[string]interface{}{
		"events":               events,
		"dataSensitivityLevel": "standard",
	})
	nonBlockingPost("CollectUserAttribution", mutationCollectUserAttribution, vars)
}

// AddActiveUser declares userID active from now in the request's trace.
// Idempotent — a user already active is a no-op. No-op outside a trace.
func AddActiveUser(ctx context.Context, userID string) {
	uid := uaNorm(userID)
	if uid == "" {
		return
	}
	session := uaSession(ctx)
	if session == "" {
		return
	}
	now := time.Now().UnixMilli()
	uaActiveMu.Lock()
	set := uaActive[session]
	if set == nil {
		set = map[string]int64{}
		uaActive[session] = set
	}
	if _, ok := set[uid]; ok {
		uaActiveMu.Unlock()
		return
	}
	set[uid] = now
	uaActiveMu.Unlock()
	uaEmit(session, "add", uid, now)
}

// RemoveActiveUser closes userID's open interval. No-op if not active / outside a trace.
func RemoveActiveUser(ctx context.Context, userID string) {
	uid := uaNorm(userID)
	if uid == "" {
		return
	}
	session := uaSession(ctx)
	if session == "" {
		return
	}
	now := time.Now().UnixMilli()
	uaActiveMu.Lock()
	set := uaActive[session]
	if set == nil {
		uaActiveMu.Unlock()
		return
	}
	if _, ok := set[uid]; !ok {
		uaActiveMu.Unlock()
		return
	}
	delete(set, uid)
	if len(set) == 0 {
		delete(uaActive, session)
	}
	uaActiveMu.Unlock()
	uaEmit(session, "end", uid, now)
}

// SetActiveUsers replaces the active set with exactly userIDs: newly present
// users get an add event, no-longer-present users get an end event. No-op outside a trace.
func SetActiveUsers(ctx context.Context, userIDs []string) {
	session := uaSession(ctx)
	if session == "" {
		return
	}
	target := make(map[string]bool, len(userIDs))
	for _, u := range userIDs {
		if uid := uaNorm(u); uid != "" {
			target[uid] = true
		}
	}
	now := time.Now().UnixMilli()
	var adds, ends []string
	uaActiveMu.Lock()
	set := uaActive[session]
	if set == nil {
		set = map[string]int64{}
		uaActive[session] = set
	}
	for uid := range target {
		if _, ok := set[uid]; !ok {
			set[uid] = now
			adds = append(adds, uid)
		}
	}
	for uid := range set {
		if !target[uid] {
			delete(set, uid)
			ends = append(ends, uid)
		}
	}
	if len(set) == 0 {
		delete(uaActive, session)
	}
	uaActiveMu.Unlock()
	for _, uid := range adds {
		uaEmit(session, "add", uid, now)
	}
	for _, uid := range ends {
		uaEmit(session, "end", uid, now)
	}
}

// ClearActiveUsers closes every open interval for the trace. Call on session end.
func ClearActiveUsers(ctx context.Context) {
	session := uaSession(ctx)
	if session == "" {
		return
	}
	now := time.Now().UnixMilli()
	var ends []string
	uaActiveMu.Lock()
	for uid := range uaActive[session] {
		ends = append(ends, uid)
	}
	delete(uaActive, session)
	uaActiveMu.Unlock()
	for _, uid := range ends {
		uaEmit(session, "end", uid, now)
	}
}

// ActiveUserScope marks userID active for the trace and returns a function that
// ends the interval — the Go idiom for Python's `active_user` context manager:
//
//	defer sfveritas.ActiveUserScope(ctx, "customer-7")()
func ActiveUserScope(ctx context.Context, userID string) func() {
	AddActiveUser(ctx, userID)
	return func() { RemoveActiveUser(ctx, userID) }
}
