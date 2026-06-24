package sfveritas

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// responseRecorder wraps http.ResponseWriter to capture the status code and optionally the body.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
	body       *bytes.Buffer // nil unless body capture is enabled
}

func (rr *responseRecorder) WriteHeader(code int) {
	if !rr.written {
		rr.statusCode = code
		rr.written = true
	}
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if !rr.written {
		rr.statusCode = http.StatusOK
		rr.written = true
	}
	if rr.body != nil {
		rr.body.Write(b)
	}
	return rr.ResponseWriter.Write(b)
}

// Middleware returns an http.Handler that instruments inbound HTTP requests.
// It extracts tracing headers, records request/response data, and sends
// telemetry to the Sailfish backend.
//
// Usage:
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/", handler)
//	http.ListenAndServe(":8080", sfveritas.Middleware(mux))
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := getConfig()
		if cfg == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Check route suppression before doing any work
		if isRouteDisabled(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		startTime := time.Now()

		// 1. Extract X-Sf3-Rid header → set trace ID in context
		incomingRid := r.Header.Get(tracingHeader)
		if incomingRid != "" {
			ctx = SetTraceID(ctx, incomingRid)
			if cfg.debug {
				fmt.Fprintf(os.Stderr, "[sfveritas] Inbound %s: %s\n", tracingHeader, incomingRid)
			}
		} else {
			// Generate new trace ID for requests without one
			ctx, _ = GetOrSetTraceID(ctx)
		}

		// 2. Extract funcspan override
		funcspanOverride := r.Header.Get(funcspanOverrideHeader)
		if funcspanOverride != "" {
			ctx = SetFuncSpanOverride(ctx, funcspanOverride)
		}

		// 3. Keep a page-visit ID on the context for downstream spans.
		ctx, _ = GetOrSetPageVisitID(ctx)

		// 4. Get trace ID for the goroutine-local link and the network hop.
		_, traceID := GetOrSetTraceID(ctx)

		// Update request context
		r = r.WithContext(ctx)

		// 5. Panic recovery
		defer func() {
			if recovered := recover(); recovered != nil {
				transmitExceptionFromPanicInMiddleware(ctx, recovered)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		// 5b. Register the trace against this goroutine so context-less telemetry
		// from the handler (plain slog.Info, fmt.Println, panics) correlates to
		// this request without the customer threading ctx through every call.
		// Handlers run synchronously on this goroutine; the deferred clear fires
		// even if the handler panics. See goroutine_local.go.
		gid := curGoroutineID()
		setGoroutineTrace(gid, traceID)
		defer clearGoroutineTrace(gid)

		// Serve the request.
		next.ServeHTTP(w, r)

		// 6. Record a network HOP for this inbound request — NOT a network
		// request. The network REQUEST is recorded by whoever made the call (the
		// browser/recorder, or an upstream service's outbound transport, which
		// also propagates X-Sf3-Rid). An inbound server only adds a hop showing
		// the request entered this service, so it nests under the originating
		// request instead of creating a second, duplicate request. This mirrors
		// the Python SDK, whose web-framework middleware emits a hop, while
		// collectNetworkRequest is reserved for outbound client calls.
		hopsVars := map[string]interface{}{
			"apiKey":      cfg.apiKey,
			"sessionId":   traceID,
			"timestampMs": strconv.FormatInt(time.Now().UnixMilli(), 10),
			"line":        "0",
			"column":      "0",
			"name":        r.Method + " " + r.URL.Path,
			"entrypoint":  r.URL.Path,
			"serviceUuid": cfg.serviceUUID,
		}
		nonBlockingPost("collectNetworkHops", mutationCollectNetworkHops, hopsVars)

		if cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] Inbound hop: %s %s (%s)\n",
				r.Method, r.URL.Path, time.Since(startTime))
		}
	})
}

// collectHeaders converts http.Header to map[string]interface{} with pre-sized allocation.
func collectHeaders(h http.Header) map[string]interface{} {
	if len(h) == 0 {
		return nil
	}
	m := make(map[string]interface{}, len(h))
	for k, v := range h {
		if len(v) == 1 {
			m[k] = v[0]
		} else {
			m[k] = v
		}
	}
	return m
}

// findUserFrame walks the call stack to find the first frame outside the sfveritas package.
func findUserFrame(skip int) (file string, line int, funcName string) {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(skip+1, pcs)
	if n == 0 {
		return "unknown", 0, "unknown"
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		// Skip frames inside the sfveritas library
		if !isLibraryFrame(frame.Function) {
			return frame.File, frame.Line, frame.Function
		}
		if !more {
			break
		}
	}
	// Fallback: return the deepest frame
	return "unknown", 0, "unknown"
}

// isLibraryFrame returns true if the function belongs to the sfveritas library or Go standard library internals.
func isLibraryFrame(funcName string) bool {
	if funcName == "" {
		return true
	}
	if strings.Contains(funcName, "sf-veritas-go") || strings.HasPrefix(funcName, "github.com/SailfishAI/sf-veritas-go") {
		return true
	}
	// Skip net/http internals
	if strings.HasPrefix(funcName, "net/http.") {
		return true
	}
	return false
}

// captureBody reads a request/response body and returns it as a string if the content type is text-like.
// Returns empty string for binary content types or if the body exceeds the size limit.
func captureBody(body io.ReadCloser, contentType string, limitBytes int) string {
	if body == nil || !isTextContentType(contentType) {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(body, int64(limitBytes+1)))
	if err != nil {
		return ""
	}
	if len(data) > limitBytes {
		return string(data[:limitBytes]) + "...[truncated]"
	}
	return string(data)
}

// captureBodyFromBytes processes already-read body bytes for capture.
func captureBodyFromBytes(data []byte, contentType string, limitBytes int) string {
	if len(data) == 0 || !isTextContentType(contentType) {
		return ""
	}
	if len(data) > limitBytes {
		return string(data[:limitBytes]) + "...[truncated]"
	}
	return string(data)
}

// isTextContentType returns true for content types that should be captured (not binary).
func isTextContentType(ct string) bool {
	if ct == "" {
		return true // assume text if no content type specified
	}
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") ||
		strings.Contains(ct, "text/") ||
		strings.Contains(ct, "form-urlencoded") ||
		strings.Contains(ct, "graphql")
}
