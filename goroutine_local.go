package sfveritas

import (
	"runtime"
	"strconv"
	"sync"
)

// Go has no goroutine-local storage, so a plain slog.Info() / fmt.Println() /
// panic inside a request handler carries no way to reach the request's trace —
// unlike Python (contextvars), JS (AsyncLocalStorage) or Java (ThreadLocal),
// where the SDK correlates such telemetry to the request automatically.
//
// To get the same zero-code-change correlation in Go, the inbound HTTP
// middleware registers the active request's trace ID against the CURRENT
// goroutine ID for the duration of the handler (set before ServeHTTP, cleared
// on return). Context-less emit paths (logs, prints, exceptions, identify) then
// resolve the trace via, in order: an explicit ctx trace → this goroutine-local
// trace → the process-stable non-session ID. The map is bounded by the number
// of in-flight requests because every set is paired with a deferred clear.
//
// Limitation: telemetry emitted from a NEW goroutine the handler spawns
// (`go func(){ slog.Info(...) }()`) runs under a different goroutine ID and will
// NOT find the request trace; pass the request context explicitly there
// (slog.InfoContext(ctx, ...)). This matches the ThreadLocal limitation in the
// Java SDK when work is handed to a new thread.
var (
	goroutineTracesMu sync.RWMutex
	goroutineTraces   = make(map[int64]string)
)

// setGoroutineTrace registers traceID for the given goroutine ID.
func setGoroutineTrace(gid int64, traceID string) {
	if gid == 0 || traceID == "" {
		return
	}
	goroutineTracesMu.Lock()
	goroutineTraces[gid] = traceID
	goroutineTracesMu.Unlock()
}

// clearGoroutineTrace removes the registration for the given goroutine ID.
func clearGoroutineTrace(gid int64) {
	if gid == 0 {
		return
	}
	goroutineTracesMu.Lock()
	delete(goroutineTraces, gid)
	goroutineTracesMu.Unlock()
}

// currentGoroutineTrace returns the trace ID registered for the calling
// goroutine, or "" if none. Used only on the context-less fallback path.
func currentGoroutineTrace() string {
	gid := curGoroutineID()
	if gid == 0 {
		return ""
	}
	goroutineTracesMu.RLock()
	tid := goroutineTraces[gid]
	goroutineTracesMu.RUnlock()
	return tid
}

// curGoroutineID parses the calling goroutine's numeric ID from its stack
// header ("goroutine <id> [<state>]:"). It is allocation-free (fixed stack
// buffer) and its cost is negligible next to the network POST every captured
// log/exception already performs. It is only ever called on the context-less
// fallback path, never when the caller threads a request context.
func curGoroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	b := buf[:n]
	if len(b) < len(prefix) {
		return 0
	}
	b = b[len(prefix):]
	i := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	id, err := strconv.ParseInt(string(b[:i]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
