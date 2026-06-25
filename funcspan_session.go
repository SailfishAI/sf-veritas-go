package sfveritas

import (
	"sync"
	"sync/atomic"
	"time"
)

// Process-wide debug-session state for the WS uplink ("backend debugger") — the
// Go analog of the Python SDK's function_span_capture_session. A session is
// activated by a backendFunctionSpanRules directive and self-deactivates on TTL
// expiry, span budget, payload budget, supersede, cancel, or shutdown.

type funcspanSession struct {
	sessionID    string
	rulesVersion int
	startedAtMs  int64
	expiresAtMs  int64
	maxSpans     int64
	maxBytes     int64
	spansEmitted int64
	bytesEmitted int64
}

// sessionSnapshot is handed to the deactivate callback (→ sessionExpired).
type sessionSnapshot struct {
	SessionID    string
	RulesVersion int
	SpansEmitted int64
	BytesEmitted int64
}

var (
	// activeSession is a lock-free presence flag for the hot path: End checks
	// activeSession.Load() != nil — a single atomic load in steady state. Its
	// counter fields are mutated only under sessionMu; hot-path readers never
	// read the counters, so there is no torn-read concern.
	activeSession atomic.Pointer[funcspanSession]
	sessionMu     sync.Mutex
	onDeactivate  atomic.Pointer[func(sessionSnapshot, string)] // set once by the uplink
)

func setSessionDeactivateCallback(fn func(sessionSnapshot, string)) {
	onDeactivate.Store(&fn)
}

func fireDeactivate(snap sessionSnapshot, reason string) {
	if cb := onDeactivate.Load(); cb != nil {
		(*cb)(snap, reason)
	}
}

func funcspanSessionIsActive() bool { return activeSession.Load() != nil }

func snapshotOf(s *funcspanSession) sessionSnapshot {
	return sessionSnapshot{
		SessionID:    s.sessionID,
		RulesVersion: s.rulesVersion,
		SpansEmitted: atomic.LoadInt64(&s.spansEmitted),
		BytesEmitted: atomic.LoadInt64(&s.bytesEmitted),
	}
}

// funcspanSessionActivate activates, updates (same sessionID), or supersedes
// (different sessionID) the active session. Returns false if the directive is
// already expired or malformed (empty id / non-positive expiry).
func funcspanSessionActivate(sessionID string, rulesVersion int, expiresAtMs, maxSpans, maxBytes int64) bool {
	if sessionID == "" || expiresAtMs <= 0 {
		return false
	}
	if expiresAtMs <= time.Now().UnixMilli() {
		return false
	}
	var supersededSnap *sessionSnapshot
	sessionMu.Lock()
	cur := activeSession.Load()
	if cur != nil && cur.sessionID == sessionID {
		// Update in place: same id, refresh version/expiry/budgets. Build a new
		// struct (preserving emitted counters) and swap the pointer.
		ns := *cur
		ns.rulesVersion = rulesVersion
		ns.expiresAtMs = expiresAtMs
		ns.maxSpans = maxSpans
		ns.maxBytes = maxBytes
		activeSession.Store(&ns)
		sessionMu.Unlock()
		return true
	}
	if cur != nil {
		s := snapshotOf(cur)
		supersededSnap = &s
	}
	activeSession.Store(&funcspanSession{
		sessionID:    sessionID,
		rulesVersion: rulesVersion,
		startedAtMs:  time.Now().UnixMilli(),
		expiresAtMs:  expiresAtMs,
		maxSpans:     maxSpans,
		maxBytes:     maxBytes,
	})
	sessionMu.Unlock()
	if supersededSnap != nil {
		fireDeactivate(*supersededSnap, "superseded")
	}
	return true
}

// funcspanSessionDeactivate ends the active session (if any) with reason and
// fires the deactivate callback exactly once with a final snapshot.
func funcspanSessionDeactivate(reason string) {
	sessionMu.Lock()
	cur := activeSession.Load()
	if cur == nil {
		sessionMu.Unlock()
		return
	}
	snap := snapshotOf(cur)
	activeSession.Store(nil)
	sessionMu.Unlock()
	fireDeactivate(snap, reason)
}

// recordSpanEmitted accounts one emitted span against the active session's
// budgets and deactivates when either bound is crossed. Called from Span.End
// after the span was posted (post-then-record, matching Python).
func recordSpanEmitted(byteCount int64) {
	if activeSession.Load() == nil {
		return
	}
	sessionMu.Lock()
	cur := activeSession.Load()
	if cur == nil {
		sessionMu.Unlock()
		return
	}
	atomic.AddInt64(&cur.spansEmitted, 1)
	atomic.AddInt64(&cur.bytesEmitted, byteCount)
	spans := atomic.LoadInt64(&cur.spansEmitted)
	bytes := atomic.LoadInt64(&cur.bytesEmitted)
	var reason string
	if cur.maxSpans > 0 && spans >= cur.maxSpans {
		reason = "span_budget"
	} else if cur.maxBytes > 0 && bytes >= cur.maxBytes {
		reason = "payload_budget"
	}
	if reason == "" {
		sessionMu.Unlock()
		return
	}
	snap := snapshotOf(cur)
	activeSession.Store(nil)
	sessionMu.Unlock()
	fireDeactivate(snap, reason)
}

// funcspanSessionCheckTTL deactivates the session if its absolute TTL passed.
// Called ~1Hz by the uplink's TTL pump.
func funcspanSessionCheckTTL() {
	if activeSession.Load() == nil {
		return
	}
	sessionMu.Lock()
	cur := activeSession.Load()
	if cur == nil {
		sessionMu.Unlock()
		return
	}
	if time.Now().UnixMilli() < cur.expiresAtMs {
		sessionMu.Unlock()
		return
	}
	snap := snapshotOf(cur)
	activeSession.Store(nil)
	sessionMu.Unlock()
	fireDeactivate(snap, "ttl_expired")
}
