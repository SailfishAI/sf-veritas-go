package sfveritas

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// stackFrame represents a single frame in a stack trace, matching the
// JSON format expected by the Sailfish backend.
type stackFrame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Code     string `json:"code"`
	Locals   string `json:"locals,omitempty"`
}

// sourceFileCache caches file contents to avoid re-reading the same file per frame.
var sourceFileCache sync.Map // map[string][]string (file path → lines)

// readCodeSnippet reads a single source line from a file (1-indexed).
// Returns the trimmed line content, or "" on any error.
func readCodeSnippet(file string, line int) string {
	if file == "" || line <= 0 {
		return ""
	}

	// Check cache first
	if cached, ok := sourceFileCache.Load(file); ok {
		lines := cached.([]string)
		if line <= len(lines) {
			return strings.TrimSpace(lines[line-1])
		}
		return ""
	}

	// Read file
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if scanner.Err() != nil {
		return ""
	}

	sourceFileCache.Store(file, lines)

	if line <= len(lines) {
		return strings.TrimSpace(lines[line-1])
	}
	return ""
}

// captureStackTrace captures the current goroutine's call stack.
// skip is the number of frames to skip (caller should pass 2+ to skip
// captureStackTrace itself and the calling function).
// Respects SAILFISH_EXCEPTION_STACK_DEPTH_CODE_TRACE_DEPTH: -1 = all frames,
// N = first N+1 frames (matching JS/TS behavior).
func captureStackTrace(skip int) []stackFrame {
	pcs := make([]uintptr, 64)
	n := runtime.Callers(skip, pcs)
	pcs = pcs[:n]

	// Get depth limit from config (default -1 = unlimited)
	maxFrames := -1
	if cfg := getConfig(); cfg != nil {
		maxFrames = cfg.stackDepthCodeTraceDepth
	}

	frames := runtime.CallersFrames(pcs)
	var result []stackFrame

	for {
		frame, more := frames.Next()
		result = append(result, stackFrame{
			File:     frame.File,
			Line:     frame.Line,
			Function: frame.Function,
			Code:     readCodeSnippet(frame.File, frame.Line),
		})

		// Stop after maxFrames+1 frames if depth limit is set (matches JS/TS: slice(0, depth+1))
		if maxFrames >= 0 && len(result) >= maxFrames+1 {
			break
		}
		if !more {
			break
		}
	}
	return result
}

// --- Exception deduplication ---

const (
	exceptionDedupSize    = 64
	exceptionDedupTTL     = 5 * time.Second
)

type dedupEntry struct {
	message   string
	timestamp time.Time
}

var (
	exceptionDedup      [exceptionDedupSize]dedupEntry
	exceptionDedupIndex int
	exceptionDedupMu    sync.Mutex
)

// isDuplicateException checks if this exception message was recently transmitted.
// Returns true if it's a duplicate (should be skipped).
func isDuplicateException(message string) bool {
	exceptionDedupMu.Lock()
	defer exceptionDedupMu.Unlock()

	now := time.Now()
	for i := range exceptionDedup {
		if exceptionDedup[i].message == message && now.Sub(exceptionDedup[i].timestamp) < exceptionDedupTTL {
			return true
		}
	}

	// Record this exception
	exceptionDedup[exceptionDedupIndex] = dedupEntry{message: message, timestamp: now}
	exceptionDedupIndex = (exceptionDedupIndex + 1) % exceptionDedupSize
	return false
}

// --- Error chain unwrapping ---

// unwrapErrorChain walks the error chain via errors.Unwrap and builds
// a cause chain for the trace JSON. Returns the full message and cause entries.
func unwrapErrorChain(err error) (fullMessage string, causes []map[string]interface{}) {
	fullMessage = err.Error()

	// Walk the chain — skip the outer error (it's the main message)
	inner := errors.Unwrap(err)
	for inner != nil {
		causes = append(causes, map[string]interface{}{
			"message": inner.Error(),
		})
		inner = errors.Unwrap(inner)
	}
	return fullMessage, causes
}

// transmitException sends an exception to the Sailfish backend.
func transmitException(ctx context.Context, message string, wasCaught bool, trace []stackFrame) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	// Deduplication check
	if isDuplicateException(message) {
		if cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] Skipping duplicate exception: %s\n", message)
		}
		return
	}

	traceJSON, err := json.Marshal(trace)
	if err != nil {
		traceJSON = []byte("[]")
	}

	sessionID := sessionIDFromContext(ctx)
	parentSpanID := GetCurrentSpanID(ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"exceptionMessage":         message,
		"wasCaught":                wasCaught,
		"traceJson":                string(traceJSON),
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"isFromLocalService":       cfg.isFromLocalService,
		"parentSpanId":             nilIfEmpty(parentSpanID),
	})

	nonBlockingPost("CollectExceptions", mutationCollectExceptions, vars)

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Captured exception: %s (caught=%v, frames=%d)\n",
			message, wasCaught, len(trace))
	}
}

// transmitExceptionWithCauses sends an exception with wrapped error cause chain.
func transmitExceptionWithCauses(ctx context.Context, message string, wasCaught bool, trace []stackFrame, causes []map[string]interface{}) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	// Deduplication check
	if isDuplicateException(message) {
		if cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] Skipping duplicate exception: %s\n", message)
		}
		return
	}

	// Build trace JSON that includes causes if present
	traceData := map[string]interface{}{
		"frames": trace,
	}
	if len(causes) > 0 {
		traceData["causes"] = causes
	}
	traceJSON, err := json.Marshal(traceData)
	if err != nil {
		traceJSON = []byte("[]")
	}

	sessionID := sessionIDFromContext(ctx)
	parentSpanID := GetCurrentSpanID(ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"exceptionMessage":         message,
		"wasCaught":                wasCaught,
		"traceJson":                string(traceJSON),
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"isFromLocalService":       cfg.isFromLocalService,
		"parentSpanId":             nilIfEmpty(parentSpanID),
	})

	nonBlockingPost("CollectExceptions", mutationCollectExceptions, vars)

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Captured exception: %s (caught=%v, frames=%d, causes=%d)\n",
			message, wasCaught, len(trace), len(causes))
	}
}

// TransmitError reports a caught error to the Sailfish backend.
// This is the public API for manually reporting errors.
// It unwraps error chains to capture the full cause chain.
func TransmitError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	trace := captureStackTrace(3)
	message, causes := unwrapErrorChain(err)
	if len(causes) > 0 {
		transmitExceptionWithCauses(ctx, message, true, trace, causes)
	} else {
		transmitException(ctx, message, true, trace)
	}
}

// TransmitPanicWithLocals reports a recovered panic to the Sailfish backend,
// including captured local variable values at the time of the panic. This is
// called by the sfveritas-instrument toolexec wrapper's injected defer blocks.
func TransmitPanicWithLocals(ctx context.Context, recovered interface{}, locals map[string]interface{}) {
	if recovered == nil {
		return
	}
	msg := fmt.Sprintf("%v", recovered)
	trace := captureStackTrace(3)

	// Attach local variables to the innermost frame
	if len(trace) > 0 && len(locals) > 0 {
		localsJSON, err := json.Marshal(locals)
		if err == nil {
			trace[0].Locals = string(localsJSON)
		}
	}

	transmitException(ctx, msg, true, trace)
}

// TransmitPanic reports a recovered panic to the Sailfish backend.
// Typically called in a deferred function after recover().
func TransmitPanic(ctx context.Context, recovered interface{}) {
	if recovered == nil {
		return
	}
	msg := fmt.Sprintf("%v", recovered)
	trace := captureStackTrace(3)
	transmitException(ctx, msg, true, trace)
}

// RecoverAndTransmit is a convenience function to be used in defer statements.
// It recovers from a panic, transmits the error to Sailfish, and optionally re-panics.
//
// Usage:
//
//	defer sfveritas.RecoverAndTransmit(ctx, false)
func RecoverAndTransmit(ctx context.Context, rePanic bool) {
	if r := recover(); r != nil {
		msg := fmt.Sprintf("%v", r)
		trace := captureStackTrace(4)
		transmitException(ctx, msg, true, trace)

		if getConfig() != nil && getConfig().debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] Recovered panic: %s\n", msg)
		}

		if rePanic {
			panic(r)
		}
	}
}

// transmitExceptionFromPanicInMiddleware is used by the HTTP middleware to
// report panics with the correct stack trace depth.
func transmitExceptionFromPanicInMiddleware(ctx context.Context, recovered interface{}) {
	if recovered == nil {
		return
	}
	msg := fmt.Sprintf("%v", recovered)

	// Capture a raw stack buffer for more detailed panic info
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	stackStr := string(buf[:n])

	// Also capture structured frames
	trace := captureStackTrace(4)

	cfg := getConfig()
	if cfg == nil {
		return
	}

	traceJSON, err := json.Marshal(trace)
	if err != nil {
		traceJSON = []byte("[]")
	}

	sessionID := sessionIDFromContext(ctx)
	parentSpanID := GetCurrentSpanID(ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"exceptionMessage":         msg,
		"wasCaught":                true,
		"traceJson":                string(traceJSON),
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"isFromLocalService":       cfg.isFromLocalService,
		"parentSpanId":             nilIfEmpty(parentSpanID),
	})

	nonBlockingPost("CollectExceptions", mutationCollectExceptions, vars)

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Recovered panic in middleware: %s\nStack:\n%s\n",
			msg, stackStr)
	}
}

// timestampMs returns the current time in milliseconds as a string.
func timestampMs() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}
