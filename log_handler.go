package sfveritas

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

// reentrancy guard to prevent capturing telemetry-related logs
var inLogCapture atomic.Bool

// Handler is an slog.Handler that intercepts structured logs and sends
// them to the Sailfish backend while forwarding to an inner handler.
type Handler struct {
	inner slog.Handler
	attrs []slog.Attr
	group string
}

// NewHandler wraps an existing slog.Handler so that all log records are
// forwarded both to the original handler and to the Sailfish backend.
func NewHandler(inner slog.Handler) *Handler {
	return &Handler{inner: inner}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Always forward to inner handler first
	var err error
	if h.inner != nil {
		err = h.inner.Handle(ctx, r)
	}

	// Reentrancy guard
	if !inLogCapture.CompareAndSwap(false, true) {
		return err
	}
	defer inLogCapture.Store(false)

	cfg := getConfig()
	if cfg == nil {
		return err
	}

	level := mapSlogLevel(r.Level)
	msg := formatLogMessage(r, h.attrs, h.group)

	// Check log ignore regex — skip telemetry but still forward to inner handler
	if cfg.logIgnoreRegex != nil && cfg.logIgnoreRegex.MatchString(msg) {
		return err
	}

	// Determine source file and line
	sourceFile := ""
	sourceLine := 0
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		sourceFile = f.File
		sourceLine = f.Line
	}

	// Link to the request's trace if ctx carries one (e.g. slog.InfoContext with
	// the request context); otherwise use the stable non-session ID. Do NOT mint
	// a fresh ID per log.
	sessionID := sessionIDFromContext(ctx)
	parentSpanID := GetCurrentSpanID(ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"level":                    level,
		"contents":                 msg,
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"parentSpanId":             nilIfEmpty(parentSpanID),
		"sourceFile":               nilIfEmpty(sourceFile),
		"sourceLine":               nilIntIfZero(sourceLine),
	})

	nonBlockingPost("CollectLogs", mutationCollectLogs, vars)

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Captured log: level=%s msg=%q file=%s:%d\n",
			level, msg, sourceFile, sourceLine)
	}

	return err
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		inner: h.inner.WithAttrs(attrs),
		attrs: append(h.attrs, attrs...),
		group: h.group,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		inner: h.inner.WithGroup(name),
		attrs: h.attrs,
		group: name,
	}
}

// formatLogMessage builds the full log message including structured attributes.
// This ensures attributes from slog.Info("msg", "key", val) are not lost.
func formatLogMessage(r slog.Record, handlerAttrs []slog.Attr, group string) string {
	// Fast path: no attributes at all
	if r.NumAttrs() == 0 && len(handlerAttrs) == 0 {
		return r.Message
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	buf.WriteString(r.Message)

	// Include handler-level attrs (from WithAttrs)
	if group != "" {
		buf.WriteByte(' ')
		buf.WriteString(group)
		buf.WriteByte('.')
	}
	for _, a := range handlerAttrs {
		buf.WriteByte(' ')
		appendAttr(buf, a)
	}

	// Include record-level attrs
	r.Attrs(func(a slog.Attr) bool {
		buf.WriteByte(' ')
		appendAttr(buf, a)
		return true
	})

	return buf.String()
}

// appendAttr writes a single slog.Attr as "key=value" to the buffer.
func appendAttr(buf *bytes.Buffer, a slog.Attr) {
	buf.WriteString(a.Key)
	buf.WriteByte('=')
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		buf.WriteString(v.String())
	case slog.KindInt64:
		buf.WriteString(strconv.FormatInt(v.Int64(), 10))
	case slog.KindUint64:
		buf.WriteString(strconv.FormatUint(v.Uint64(), 10))
	case slog.KindFloat64:
		buf.WriteString(strconv.FormatFloat(v.Float64(), 'g', -1, 64))
	case slog.KindBool:
		if v.Bool() {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case slog.KindTime:
		buf.WriteString(v.Time().Format(time.RFC3339))
	case slog.KindDuration:
		buf.WriteString(v.Duration().String())
	case slog.KindGroup:
		buf.WriteByte('{')
		for i, ga := range v.Group() {
			if i > 0 {
				buf.WriteByte(' ')
			}
			appendAttr(buf, ga)
		}
		buf.WriteByte('}')
	default:
		fmt.Fprintf(buf, "%v", v.Any())
	}
}

func mapSlogLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// LogWriter adapts the standard log package to send output to Sailfish.
// Use it with log.SetOutput(sfveritas.NewLogWriter()).
type LogWriter struct{}

// NewLogWriter returns an io.Writer that captures standard log output.
func NewLogWriter() *LogWriter {
	return &LogWriter{}
}

func (w *LogWriter) Write(p []byte) (n int, err error) {
	// Always write to original stderr
	n, err = os.Stderr.Write(p)

	// Reentrancy guard
	if !inLogCapture.CompareAndSwap(false, true) {
		return n, err
	}
	defer inLogCapture.Store(false)

	cfg := getConfig()
	if cfg == nil {
		return n, err
	}

	msg := string(p)

	// Check log ignore regex
	if cfg.logIgnoreRegex != nil && cfg.logIgnoreRegex.MatchString(msg) {
		return n, err
	}

	// Try to get caller info
	_, file, line, ok := runtime.Caller(3) // skip Write, log.Output, log.Print*
	sourceFile := ""
	sourceLine := 0
	if ok {
		sourceFile = file
		sourceLine = line
	}

	// Standard log carries no context; use the stable non-session ID rather than
	// minting a fresh request ID per line.
	sessionID := sessionIDFromContext(context.Background())

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"level":                    "INFO",
		"contents":                 msg,
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"parentSpanId":             nil,
		"sourceFile":               nilIfEmpty(sourceFile),
		"sourceLine":               nilIntIfZero(sourceLine),
	})

	nonBlockingPost("CollectLogs", mutationCollectLogs, vars)
	return n, err
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nilIntIfZero(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

// TransmitLog sends a log entry to the Sailfish backend manually.
func TransmitLog(ctx context.Context, level, message string) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	_, file, line, _ := runtime.Caller(1)
	sessionID := sessionIDFromContext(ctx)
	parentSpanID := GetCurrentSpanID(ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"level":                    level,
		"contents":                 message,
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"parentSpanId":             nilIfEmpty(parentSpanID),
		"sourceFile":               nilIfEmpty(file),
		"sourceLine":               nilIntIfZero(line),
	})

	nonBlockingPost("CollectLogs", mutationCollectLogs, vars)
}

// transmitLogInternal sends a log with explicit source info (used by print capture).
func transmitLogInternal(ctx context.Context, level, message, sourceFile string, sourceLine int) {
	sessionID := sessionIDFromContext(ctx)
	parentSpanID := GetCurrentSpanID(ctx)

	vars := mergeVariables(map[string]interface{}{
		"sessionId":                sessionID,
		"level":                    level,
		"contents":                 message,
		"reentrancyGuardPreactive": false,
		"library":                  LibraryType,
		"version":                  Version,
		"parentSpanId":             nilIfEmpty(parentSpanID),
		"sourceFile":               nilIfEmpty(sourceFile),
		"sourceLine":               nilIntIfZero(sourceLine),
	})

	nonBlockingPost("CollectLogs", mutationCollectLogs, vars)
}

// timestampMsStr returns the current UTC time as a millisecond string.
func timestampMsStr() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}
