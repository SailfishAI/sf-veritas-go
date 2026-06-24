package sfveritas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Pre-computed constant JSON for nil return values (no allocation per call).
const nullReturnJSON = `{"type":"null","has_value":true,"value":null}`

// Span represents an active function span. Use StartSpan to create one,
// then call End when the function completes.
type Span struct {
	ctx          context.Context
	spanID       string
	parentSpanID string
	filePath     string
	lineNumber   int
	columnNumber int
	functionName string
	argsJSON     string
	startTimeNs  int64
	prevSpanID   string // previous span ID for context restoration
	noop         bool   // true for sampled-out spans

	// Effective capture settings resolved from override header / .sailfish / global config
	captureReturn bool // whether to capture return value
	retLimitBytes int  // effective return value size limit
}

// StartSpan begins tracking a function span. The returned Span should be
// ended with span.End(returnValue) in a defer statement.
//
// Usage:
//
//	func MyFunction(ctx context.Context, arg1 string) (string, error) {
//	    span := sfveritas.StartSpan(ctx, "MyFunction")
//	    defer func() { span.End("result") }()
//	    // ... function body ...
//	}
func StartSpan(ctx context.Context, functionName string) *Span {
	return startSpanInternal(ctx, functionName, "{}", 2)
}

// StartSpanWithArgs begins tracking a function span with argument capture.
// Arguments are JSON-serialized immediately and truncated to the configured
// size limit (default 1 MB, configurable via SF_FUNCSPAN_ARG_LIMIT_MB).
//
// Usage:
//
//	span := sfveritas.StartSpanWithArgs(ctx, "MyFunction", map[string]interface{}{"arg1": arg1})
//	defer func() { span.End(result) }()
func StartSpanWithArgs(ctx context.Context, functionName string, args interface{}) *Span {
	argsJSON := marshalWithLimit(args, getArgLimitBytes())
	return startSpanInternal(ctx, functionName, argsJSON, 2)
}

// StartSpanNoCtx is like StartSpanWithArgs but for functions that have no
// context.Context to thread through; it uses context.Background(). It exists so
// the toolexec auto-instrumenter can start a span for a context-less function
// WITHOUT having to inject a `context` import into the user's package (the only
// import the instrumenter then needs to add is sfveritas itself).
func StartSpanNoCtx(functionName string, args interface{}) *Span {
	argsJSON := marshalWithLimit(args, getArgLimitBytes())
	return startSpanInternal(context.Background(), functionName, argsJSON, 2)
}

// funcspanOverrideConfig holds parsed override values from the X-Sf3-FunctionSpanCaptureOverride header.
type funcspanOverrideConfig struct {
	captureArgs       bool
	captureReturn     bool
	argLimitMB        int
	retLimitMB        int
	captureChildren   bool
	sampleRate        float64
	samplingEnabled   bool
	sfVeritasEnabled  bool
	parseJSON         bool
}

// parseFuncSpanOverride parses the override header format:
// "args-ret-argMB-retMB-children-rate-sampling-sfVeritas-parseJson"
func parseFuncSpanOverride(override string) *funcspanOverrideConfig {
	parts := strings.Split(override, "-")
	if len(parts) < 9 {
		return nil
	}
	cfg := &funcspanOverrideConfig{
		captureArgs:      parts[0] == "1",
		captureReturn:    parts[1] == "1",
		captureChildren:  parts[4] == "1",
		sfVeritasEnabled: parts[7] == "1",
		parseJSON:        parts[8] == "1",
	}
	if v, err := strconv.Atoi(parts[2]); err == nil {
		cfg.argLimitMB = v
	}
	if v, err := strconv.Atoi(parts[3]); err == nil {
		cfg.retLimitMB = v
	}
	if v, err := strconv.ParseFloat(parts[5], 64); err == nil {
		cfg.sampleRate = v
	}
	cfg.samplingEnabled = parts[6] == "1"
	return cfg
}

func startSpanInternal(ctx context.Context, functionName, argsJSON string, callerSkip int) *Span {
	cfg := getConfig()

	// Check if child capture is suppressed from parent context
	if GetSuppressChildren(ctx) {
		spanID := fastUUID()
		return &Span{ctx: SetCurrentSpanID(ctx, spanID), noop: true}
	}

	// Parse override header at function scope so it's available for both
	// sampling decisions and capture config resolution below.
	var oc *funcspanOverrideConfig
	if override := GetFuncSpanOverride(ctx); override != "" {
		oc = parseFuncSpanOverride(override)
	}

	// sfVeritasEnabled kill switch (override field 7)
	if oc != nil && !oc.sfVeritasEnabled {
		spanID := fastUUID()
		return &Span{ctx: SetCurrentSpanID(ctx, spanID), noop: true}
	}

	// Check override-header sampling
	if oc != nil {
		if oc.samplingEnabled && oc.sampleRate == 0 {
			spanID := fastUUID()
			return &Span{ctx: SetCurrentSpanID(ctx, spanID), noop: true}
		}
		if oc.samplingEnabled && oc.sampleRate < 1.0 {
			if rand.Float64() >= oc.sampleRate {
				spanID := fastUUID()
				return &Span{ctx: SetCurrentSpanID(ctx, spanID), noop: true}
			}
		}
	}

	// Check global sampling
	if cfg != nil && cfg.funcspanSamplingEnabled && cfg.funcspanSampleRate < 1.0 {
		if rand.Float64() >= cfg.funcspanSampleRate {
			spanID := fastUUID()
			return &Span{ctx: SetCurrentSpanID(ctx, spanID), noop: true}
		}
	}

	_, file, line, _ := runtime.Caller(callerSkip)

	// --- Resolve effective capture config ---
	// Priority: override header > .sailfish per-function/file > global env
	effectiveCaptureArgs := cfg == nil || cfg.globalCaptureArgs
	effectiveCaptureReturn := cfg == nil || cfg.globalCaptureReturn
	effectiveArgLimit := getArgLimitBytes()
	effectiveRetLimit := getReturnLimitBytes()
	effectiveParseJSON := cfg != nil && cfg.parseJSONStrings
	effectiveCaptureChildren := cfg == nil || cfg.autoCaptureChildren

	// .sailfish config (lower priority)
	if fc := getFuncspanConfig(file, functionName); fc != nil {
		if fc.CaptureArguments != nil {
			effectiveCaptureArgs = *fc.CaptureArguments
		}
		if fc.CaptureReturnValue != nil {
			effectiveCaptureReturn = *fc.CaptureReturnValue
		}
		if fc.ArgLimitMB != nil {
			effectiveArgLimit = *fc.ArgLimitMB * 1024 * 1024
		}
		if fc.ReturnLimitMB != nil {
			effectiveRetLimit = *fc.ReturnLimitMB * 1024 * 1024
		}
		if fc.ParseJSONStrings != nil {
			effectiveParseJSON = *fc.ParseJSONStrings
		}
		if fc.CaptureChildren != nil {
			effectiveCaptureChildren = *fc.CaptureChildren
		}
		// Per-function/file capture_sf_veritas kill switch from .sailfish
		if fc.CaptureSfVeritas != nil && !*fc.CaptureSfVeritas {
			spanID := fastUUID()
			return &Span{ctx: SetCurrentSpanID(ctx, spanID), noop: true}
		}
		// Per-function/file sampling from .sailfish
		// enable_sampling gates whether sample_rate is used (matching env var behavior)
		enableSampling := fc.EnableSampling == nil || *fc.EnableSampling
		if enableSampling && fc.SampleRate != nil && *fc.SampleRate < 1.0 {
			if rand.Float64() >= *fc.SampleRate {
				spanID := fastUUID()
				return &Span{ctx: SetCurrentSpanID(ctx, spanID), noop: true}
			}
		}
	}

	// Override header (highest priority — overrides .sailfish)
	if oc != nil {
		effectiveCaptureArgs = oc.captureArgs
		effectiveCaptureReturn = oc.captureReturn
		if oc.argLimitMB > 0 {
			effectiveArgLimit = oc.argLimitMB * 1024 * 1024
		}
		if oc.retLimitMB > 0 {
			effectiveRetLimit = oc.retLimitMB * 1024 * 1024
		}
		effectiveParseJSON = oc.parseJSON
		effectiveCaptureChildren = oc.captureChildren
	}

	// Apply effective arg config (limit == 0 also disables capture)
	if !effectiveCaptureArgs || effectiveArgLimit == 0 {
		argsJSON = "{}"
	} else if effectiveArgLimit > 0 && len(argsJSON) > effectiveArgLimit {
		argsJSON = marshalWithLimit(nil, effectiveArgLimit)
	}

	// Parse JSON strings in args if enabled
	if effectiveParseJSON && effectiveCaptureArgs && argsJSON != "{}" {
		argsJSON = parseJSONInArgs(argsJSON)
	}

	spanID := fastUUID()
	parentSpanID := GetCurrentSpanID(ctx)
	startTimeNs := time.Now().UnixNano()

	spanCtx := SetCurrentSpanID(ctx, spanID)

	// Propagate child suppression if captureChildren is disabled
	if !effectiveCaptureChildren {
		spanCtx = SetSuppressChildren(spanCtx, true)
	}

	return &Span{
		ctx:           spanCtx,
		spanID:        spanID,
		parentSpanID:  parentSpanID,
		filePath:      file,
		lineNumber:    line,
		columnNumber:  0,
		functionName:  functionName,
		argsJSON:      argsJSON,
		startTimeNs:   startTimeNs,
		prevSpanID:    parentSpanID,
		captureReturn: effectiveCaptureReturn,
		retLimitBytes: effectiveRetLimit,
	}
}

// Context returns the context with this span's ID set as the current span.
// Use this context for child operations that should be linked to this span.
func (s *Span) Context() context.Context {
	if s == nil {
		return context.Background()
	}
	return s.ctx
}

// End completes the span and sends the telemetry data to the backend.
// The returnValue parameter will be JSON-serialized with size limits.
// Pass nil if no return value.
func (s *Span) End(returnValue interface{}) {
	if s == nil || s.noop {
		return
	}

	durationNs := time.Now().UnixNano() - s.startTimeNs

	var returnValueStr *string
	if s.captureReturn && s.retLimitBytes > 0 && returnValue != nil {
		rv := formatReturnValue(returnValue, s.retLimitBytes)
		returnValueStr = &rv
	}

	sessionID := sessionIDFromContext(s.ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":    sessionID,
		"spanId":       s.spanID,
		"parentSpanId": nilIfEmpty(s.parentSpanID),
		"library":      LibraryType,
		"version":      Version,
		"filePath":     s.filePath,
		"lineNumber":   s.lineNumber,
		"columnNumber": s.columnNumber,
		"functionName": s.functionName,
		"arguments":    s.argsJSON,
		"returnValue":  returnValueStr,
		"startTimeNs":  strconv.FormatInt(s.startTimeNs, 10),
		"durationNs":   strconv.FormatInt(durationNs, 10),
	})

	nonBlockingPost("CollectFunctionSpans", mutationCollectFunctionSpans, vars)

	if cfg := getConfig(); cfg != nil && cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Function span: %s (%s) duration=%dns\n",
			s.functionName, s.spanID, durationNs)
	}
}

// formatReturnValue formats a return value into the standard JSON format
// expected by the backend: {"type": <type>, "has_value": bool, "value": <value>}
//
// Uses direct JSON string building to avoid map[string]interface{} allocation
// and fmt.Sprintf("%T") reflection on every call.
func formatReturnValue(v interface{}, limitBytes int) string {
	if v == nil {
		return nullReturnJSON
	}

	// Marshal the value first to check size
	valueBytes, err := json.Marshal(v)
	if err != nil {
		// Fallback: use string representation
		s := fmt.Sprint(v)
		if len(s) > 500 {
			s = s[:500] + "..."
		}
		return buildReturnJSON("unknown", s)
	}

	// Check size limit and truncate if needed
	if limitBytes > 0 && len(valueBytes) > limitBytes {
		return buildTruncatedReturnJSON(len(valueBytes), limitBytes, valueBytes)
	}

	// Build the envelope JSON directly without map allocation.
	// This avoids: map alloc + fmt.Sprintf("%T") + json.Marshal(map) + []byte→string
	typeName := typeNameFast(v)

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	buf.WriteString(`{"type":`)
	typeBytes, _ := json.Marshal(typeName)
	buf.Write(typeBytes)
	buf.WriteString(`,"has_value":true,"value":`)
	buf.Write(valueBytes)
	buf.WriteByte('}')
	result := buf.String()
	bufPool.Put(buf)
	return result
}

// buildReturnJSON builds the envelope for a string fallback value.
func buildReturnJSON(typeName, value string) string {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	buf.WriteString(`{"type":`)
	tb, _ := json.Marshal(typeName)
	buf.Write(tb)
	buf.WriteString(`,"has_value":true,"value":`)
	vb, _ := json.Marshal(value)
	buf.Write(vb)
	buf.WriteByte('}')
	result := buf.String()
	bufPool.Put(buf)
	return result
}

// buildTruncatedReturnJSON builds a truncation marker matching the JS/Python format.
func buildTruncatedReturnJSON(actualSize, limitBytes int, preview []byte) string {
	previewStr := string(preview)
	if len(previewStr) > 500 {
		previewStr = previewStr[:500] + "..."
	}
	b, _ := json.Marshal(map[string]interface{}{
		"_truncated":      true,
		"_originalSizeKB": actualSize / 1024,
		"_limitKB":        limitBytes / 1024,
		"_preview":        previewStr,
	})
	return string(b)
}

// typeNameFast returns the Go type name without using fmt.Sprintf("%T") reflection.
// Handles common types inline; falls back to fmt for uncommon types.
func typeNameFast(v interface{}) string {
	switch v.(type) {
	case string:
		return "string"
	case int:
		return "int"
	case int32:
		return "int32"
	case int64:
		return "int64"
	case float64:
		return "float64"
	case float32:
		return "float32"
	case bool:
		return "bool"
	case []byte:
		return "[]uint8"
	case map[string]interface{}:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// parseJSONInArgs walks the args JSON map and attempts to parse any string values
// that look like JSON objects or arrays into their parsed form.
func parseJSONInArgs(argsJSON string) string {
	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &argsMap); err != nil {
		return argsJSON
	}

	modified := false
	for k, v := range argsMap {
		s, ok := v.(string)
		if !ok || len(s) < 2 {
			continue
		}
		first := s[0]
		if first != '{' && first != '[' {
			continue
		}
		var parsed interface{}
		if json.Unmarshal([]byte(s), &parsed) == nil {
			argsMap[k] = parsed
			modified = true
		}
	}

	if !modified {
		return argsJSON
	}

	b, err := json.Marshal(argsMap)
	if err != nil {
		return argsJSON
	}
	return string(b)
}

// marshalWithLimit serializes a value to JSON with a size limit.
// Returns "{}" for nil, truncation marker if over limit.
func marshalWithLimit(v interface{}, limitBytes int) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	if limitBytes > 0 && len(b) > limitBytes {
		preview := string(b)
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		trunc, _ := json.Marshal(map[string]interface{}{
			"_truncated":      true,
			"_originalSizeKB": len(b) / 1024,
			"_limitKB":        limitBytes / 1024,
			"_preview":        preview,
		})
		return string(trunc)
	}
	return string(b)
}

// getArgLimitBytes returns the configured argument size limit.
func getArgLimitBytes() int {
	if cfg := getConfig(); cfg != nil {
		return cfg.argLimitBytes
	}
	return defaultArgLimitBytes
}

// getReturnLimitBytes returns the configured return value size limit.
func getReturnLimitBytes() int {
	if cfg := getConfig(); cfg != nil {
		return cfg.returnLimitBytes
	}
	return defaultReturnLimitBytes
}

// TraceFunc wraps a function call with span tracking. It captures timing,
// return value, and sends the data to the Sailfish backend.
//
// Usage:
//
//	result, err := sfveritas.TraceFunc(ctx, "myFunction", func(ctx context.Context) (string, error) {
//	    return doWork(ctx)
//	})
func TraceFunc[T any](ctx context.Context, name string, fn func(ctx context.Context) (T, error)) (T, error) {
	span := startSpanInternal(ctx, name, "{}", 2)

	result, err := fn(span.Context())

	if err != nil {
		TransmitError(span.Context(), err)
		span.End(nil)
	} else {
		span.End(result)
	}

	return result, err
}

// TraceFuncWithArgs wraps a function call with span tracking and argument capture.
func TraceFuncWithArgs[T any](ctx context.Context, name string, args interface{}, fn func(ctx context.Context) (T, error)) (T, error) {
	argsJSON := marshalWithLimit(args, getArgLimitBytes())
	span := startSpanInternal(ctx, name, argsJSON, 2)

	result, err := fn(span.Context())

	if err != nil {
		TransmitError(span.Context(), err)
		span.End(nil)
	} else {
		span.End(result)
	}

	return result, err
}
