package sfveritas

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Transport wraps an http.RoundTripper to add Sailfish tracing headers
// to outbound HTTP requests and record network request telemetry.
type Transport struct {
	Base http.RoundTripper
}

// NewTransport creates a tracing Transport that wraps the given base transport.
// If base is nil, http.DefaultTransport is used.
func NewTransport(base http.RoundTripper) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{Base: base}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg := getConfig()

	// Skip instrumentation if not configured
	if cfg == nil {
		return t.Base.RoundTrip(req)
	}

	// Skip if this is our own telemetry outbound request (reentrancy guard)
	if req.Header.Get(telemetryOutboundHeader) != "" {
		return t.Base.RoundTrip(req)
	}

	// Check if this domain is excluded
	if t.isDomainExcluded(req.URL.Host) {
		return t.Base.RoundTrip(req)
	}

	ctx := req.Context()

	// Get or create trace ID
	_, traceID := GetOrSetTraceID(ctx)
	_, pageVisitID := GetOrSetPageVisitID(ctx)
	requestUUID := fastUUID()

	// Build X-Sf3-Rid: sessionId/pageVisitId/requestUUID
	ridValue := fmt.Sprintf("%s/%s/%s", traceID, pageVisitID, requestUUID)

	// Add tracing headers directly (avoids expensive req.Clone deep copy of all headers)
	req.Header.Set(tracingHeader, ridValue)
	req.Header.Set(parentSessionHeader, traceID)

	// Add funcspan override if present
	if override := GetFuncSpanOverride(ctx); override != "" {
		req.Header.Set(funcspanOverrideHeader, override)
	}

	// Buffer request body for retry on 400/403 and optional body capture
	var reqBodyBytes []byte
	if req.Body != nil {
		var err error
		reqBodyBytes, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			reqBodyBytes = nil
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
	}

	startTime := time.Now()

	// Execute the actual request
	resp, err := t.Base.RoundTrip(req)

	endTime := time.Now()

	// Retry without trace headers on 400/403
	retryWithoutTraceId := false
	if err == nil && resp != nil && (resp.StatusCode == 400 || resp.StatusCode == 403) {
		// Remove tracing headers and retry
		req.Header.Del(tracingHeader)
		req.Header.Del(parentSessionHeader)
		req.Header.Del(funcspanOverrideHeader)

		// Reset request body for retry
		if reqBodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
		}

		resp.Body.Close()
		startTime = time.Now()
		resp, err = t.Base.RoundTrip(req)
		endTime = time.Now()
		retryWithoutTraceId = true
	}

	// Record the outbound network request
	responseCode := 0
	success := false
	var errMsg interface{} = nil
	if err != nil {
		errMsg = err.Error()
	}
	if resp != nil {
		responseCode = resp.StatusCode
		success = responseCode >= 200 && responseCode < 400
	}

	// Collect headers (gated by env var, default off for OTEL compliance)
	var reqHeaders, respHeaders map[string]interface{}
	if cfg.captureRequestHeaders {
		reqHeaders = collectHeaders(req.Header)
	}
	if cfg.captureResponseHeaders && resp != nil {
		respHeaders = collectHeaders(resp.Header)
	}

	// Get current span ID and walk stack for user frame
	parentSpanID := GetCurrentSpanID(ctx)
	callerFile, callerLine, callerFunc := findUserFrame(2)

	data := map[string]interface{}{
		"apiKey":              cfg.apiKey,
		"requestId":          requestUUID,
		"pageVisitId":        pageVisitID,
		"recordingSessionId": traceID,
		"serviceUuid":        cfg.serviceUUID,
		"timestampStart":     startTime.UnixMilli(),
		"timestampEnd":       endTime.UnixMilli(),
		"responseCode":       responseCode,
		"success":            success,
		"error":              errMsg,
		"url":                req.URL.String(),
		"method":             req.Method,
		"requestHeaders":     reqHeaders,
		"responseHeaders":    respHeaders,
		"name":               req.URL.String(),
		"parentSpanId":       nilIfEmpty(parentSpanID),
		"parentSessionId":    traceID,
	}

	if retryWithoutTraceId {
		data["retryWithoutTraceId"] = true
	}

	// Capture request body
	if cfg.captureRequestBody && len(reqBodyBytes) > 0 {
		ct := req.Header.Get("Content-Type")
		body := captureBodyFromBytes(reqBodyBytes, ct, cfg.requestBodyLimitBytes)
		if body != "" {
			data["requestBody"] = body
		}
	}

	// Capture response body
	if cfg.captureResponseBody && resp != nil && resp.Body != nil {
		ct := resp.Header.Get("Content-Type")
		if isTextContentType(ct) {
			respBodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(cfg.responseBodyLimitBytes+1)))
			resp.Body.Close()
			if readErr == nil && len(respBodyBytes) > 0 {
				body := captureBodyFromBytes(respBodyBytes, ct, cfg.responseBodyLimitBytes)
				if body != "" {
					data["responseBody"] = body
				}
				// Restore the body so the caller can still read it
				resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
			}
		}
	}

	nonBlockingPost("collectNetworkRequest", mutationCollectNetworkRequest, map[string]interface{}{
		"data": data,
	})

	// Send network hops for outbound
	hopsVars := map[string]interface{}{
		"apiKey":      cfg.apiKey,
		"sessionId":   traceID,
		"timestampMs": fmt.Sprintf("%d", time.Now().UnixMilli()),
		"line":        fmt.Sprintf("%d", callerLine),
		"column":      "0",
		"name":        callerFunc,
		"entrypoint":  callerFile,
		"serviceUuid": cfg.serviceUUID,
	}
	nonBlockingPost("collectNetworkHops", mutationCollectNetworkHops, hopsVars)

	if cfg.debug {
		extra := ""
		if retryWithoutTraceId {
			extra = " (retried without trace header)"
		}
		fmt.Fprintf(os.Stderr, "[sfveritas] Outbound request: %s %s → %d (%s)%s\n",
			req.Method, req.URL.String(), responseCode, endTime.Sub(startTime), extra)
	}

	return resp, err
}

// isDomainExcluded checks if a host matches the excluded domains list.
// Uses pre-computed map for O(1) exact lookups and minimal suffix checks.
func (t *Transport) isDomainExcluded(host string) bool {
	cfg := getConfig()
	if cfg == nil || (len(cfg.excludedDomainsExact) == 0 && len(cfg.excludedDomainsSuffix) == 0) {
		return false
	}
	// Strip port
	if idx := strings.IndexByte(host, ':'); idx >= 0 {
		host = host[:idx]
	}
	host = strings.ToLower(host)

	// O(1) exact match
	if _, ok := cfg.excludedDomainsExact[host]; ok {
		return true
	}
	// Suffix matches (typically very few entries)
	for _, suffix := range cfg.excludedDomainsSuffix {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
