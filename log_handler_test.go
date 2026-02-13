package sfveritas

import (
	"log/slog"
	"testing"
)

// --- mapSlogLevel ---

func TestMapSlogLevel_Error(t *testing.T) {
	if mapSlogLevel(slog.LevelError) != "ERROR" {
		t.Errorf("expected ERROR, got %s", mapSlogLevel(slog.LevelError))
	}
}

func TestMapSlogLevel_Warn(t *testing.T) {
	if mapSlogLevel(slog.LevelWarn) != "WARN" {
		t.Errorf("expected WARN, got %s", mapSlogLevel(slog.LevelWarn))
	}
}

func TestMapSlogLevel_Info(t *testing.T) {
	if mapSlogLevel(slog.LevelInfo) != "INFO" {
		t.Errorf("expected INFO, got %s", mapSlogLevel(slog.LevelInfo))
	}
}

func TestMapSlogLevel_Debug(t *testing.T) {
	if mapSlogLevel(slog.LevelDebug) != "DEBUG" {
		t.Errorf("expected DEBUG, got %s", mapSlogLevel(slog.LevelDebug))
	}
}

func TestMapSlogLevel_CustomLevel(t *testing.T) {
	// slog.Level is just an int, test values between standard levels
	if mapSlogLevel(slog.LevelWarn+1) != "WARN" {
		t.Error("expected WARN for level between Warn and Error")
	}
	if mapSlogLevel(slog.LevelError+1) != "ERROR" {
		t.Error("expected ERROR for level above Error")
	}
	if mapSlogLevel(slog.LevelDebug-1) != "DEBUG" {
		t.Error("expected DEBUG for level below Debug")
	}
}

// --- formatLogMessage ---

func TestFormatLogMessage_SimpleMessage(t *testing.T) {
	r := slog.Record{}
	r.Message = "hello world"
	got := formatLogMessage(r, nil, "")
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestFormatLogMessage_WithAttrs(t *testing.T) {
	r := slog.Record{}
	r.Message = "request"
	r.AddAttrs(slog.String("method", "GET"), slog.Int("status", 200))
	got := formatLogMessage(r, nil, "")
	if got != "request method=GET status=200" {
		t.Errorf("expected 'request method=GET status=200', got %q", got)
	}
}

func TestFormatLogMessage_WithHandlerAttrs(t *testing.T) {
	r := slog.Record{}
	r.Message = "log"
	handlerAttrs := []slog.Attr{slog.String("service", "api")}
	got := formatLogMessage(r, handlerAttrs, "")
	if got != "log service=api" {
		t.Errorf("expected 'log service=api', got %q", got)
	}
}

func TestFormatLogMessage_WithGroup(t *testing.T) {
	r := slog.Record{}
	r.Message = "log"
	handlerAttrs := []slog.Attr{slog.String("key", "val")}
	got := formatLogMessage(r, handlerAttrs, "mygroup")
	// The implementation writes " mygroup." then " key=val" so there's a space after the dot
	if got != "log mygroup. key=val" {
		t.Errorf("expected 'log mygroup. key=val', got %q", got)
	}
}

func TestFormatLogMessage_NoAttrs(t *testing.T) {
	r := slog.Record{}
	r.Message = "simple"
	got := formatLogMessage(r, nil, "")
	if got != "simple" {
		t.Errorf("expected 'simple', got %q", got)
	}
}

// --- NewHandler ---

func TestNewHandler_WrapsInner(t *testing.T) {
	inner := slog.NewTextHandler(nil, nil)
	h := NewHandler(inner)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if h.inner != inner {
		t.Error("expected inner handler to be stored")
	}
}

func TestNewHandler_WithAttrs(t *testing.T) {
	inner := slog.NewTextHandler(nil, nil)
	h := NewHandler(inner)
	h2 := h.WithAttrs([]slog.Attr{slog.String("key", "val")})
	if h2 == nil {
		t.Fatal("expected non-nil handler from WithAttrs")
	}
	sh, ok := h2.(*Handler)
	if !ok {
		t.Fatal("expected *Handler type")
	}
	if len(sh.attrs) != 1 {
		t.Errorf("expected 1 attr, got %d", len(sh.attrs))
	}
}

func TestNewHandler_WithGroup(t *testing.T) {
	inner := slog.NewTextHandler(nil, nil)
	h := NewHandler(inner)
	h2 := h.WithGroup("mygroup")
	if h2 == nil {
		t.Fatal("expected non-nil handler from WithGroup")
	}
	sh, ok := h2.(*Handler)
	if !ok {
		t.Fatal("expected *Handler type")
	}
	if sh.group != "mygroup" {
		t.Errorf("expected group 'mygroup', got %s", sh.group)
	}
}

// --- isTextContentType ---

func TestIsTextContentType_JSON(t *testing.T) {
	if !isTextContentType("application/json") {
		t.Error("expected JSON to be text content type")
	}
}

func TestIsTextContentType_XML(t *testing.T) {
	if !isTextContentType("application/xml") {
		t.Error("expected XML to be text content type")
	}
}

func TestIsTextContentType_TextPlain(t *testing.T) {
	if !isTextContentType("text/plain") {
		t.Error("expected text/plain to be text content type")
	}
}

func TestIsTextContentType_FormURLEncoded(t *testing.T) {
	if !isTextContentType("application/x-www-form-urlencoded") {
		t.Error("expected form-urlencoded to be text content type")
	}
}

func TestIsTextContentType_GraphQL(t *testing.T) {
	if !isTextContentType("application/graphql") {
		t.Error("expected graphql to be text content type")
	}
}

func TestIsTextContentType_Binary(t *testing.T) {
	if isTextContentType("application/octet-stream") {
		t.Error("expected octet-stream to NOT be text content type")
	}
}

func TestIsTextContentType_Image(t *testing.T) {
	if isTextContentType("image/png") {
		t.Error("expected image/png to NOT be text content type")
	}
}

func TestIsTextContentType_Empty(t *testing.T) {
	if !isTextContentType("") {
		t.Error("expected empty content type to be treated as text")
	}
}

// --- collectHeaders ---

func TestCollectHeaders_Empty(t *testing.T) {
	got := collectHeaders(nil)
	if got != nil {
		t.Error("expected nil for nil headers")
	}
}

func TestCollectHeaders_SingleValues(t *testing.T) {
	h := map[string][]string{
		"Content-Type": {"application/json"},
		"Accept":       {"text/html"},
	}
	got := collectHeaders(h)
	if got["Content-Type"] != "application/json" {
		t.Errorf("expected 'application/json', got %v", got["Content-Type"])
	}
	if got["Accept"] != "text/html" {
		t.Errorf("expected 'text/html', got %v", got["Accept"])
	}
}

func TestCollectHeaders_MultiValues(t *testing.T) {
	h := map[string][]string{
		"Accept": {"text/html", "application/json"},
	}
	got := collectHeaders(h)
	vals, ok := got["Accept"].([]string)
	if !ok {
		t.Fatalf("expected []string for multi-value header, got %T", got["Accept"])
	}
	if len(vals) != 2 {
		t.Errorf("expected 2 values, got %d", len(vals))
	}
}

// --- isLibraryFrame ---

func TestIsLibraryFrame_SfVeritas(t *testing.T) {
	if !isLibraryFrame("github.com/SailfishAI/sf-veritas-go.StartSpan") {
		t.Error("expected sf-veritas-go frame to be library frame")
	}
}

func TestIsLibraryFrame_NetHTTP(t *testing.T) {
	if !isLibraryFrame("net/http.(*Server).Serve") {
		t.Error("expected net/http frame to be library frame")
	}
}

func TestIsLibraryFrame_UserCode(t *testing.T) {
	if isLibraryFrame("main.handler") {
		t.Error("expected main.handler to NOT be library frame")
	}
}

func TestIsLibraryFrame_Empty(t *testing.T) {
	if !isLibraryFrame("") {
		t.Error("expected empty function name to be library frame")
	}
}

// --- captureBodyFromBytes ---

func TestCaptureBodyFromBytes_Empty(t *testing.T) {
	got := captureBodyFromBytes(nil, "application/json", 1024)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestCaptureBodyFromBytes_JSON(t *testing.T) {
	data := []byte(`{"key": "value"}`)
	got := captureBodyFromBytes(data, "application/json", 1024)
	if got != `{"key": "value"}` {
		t.Errorf("expected body content, got %q", got)
	}
}

func TestCaptureBodyFromBytes_Truncation(t *testing.T) {
	data := []byte(`{"key": "a very long value that exceeds limit"}`)
	got := captureBodyFromBytes(data, "application/json", 10)
	if len(got) <= 10 {
		t.Error("expected truncated body with marker")
	}
	if got[len(got)-len("...[truncated]"):] != "...[truncated]" {
		t.Error("expected truncation marker")
	}
}

func TestCaptureBodyFromBytes_BinaryContentType(t *testing.T) {
	data := []byte("binary data")
	got := captureBodyFromBytes(data, "application/octet-stream", 1024)
	if got != "" {
		t.Errorf("expected empty string for binary content, got %q", got)
	}
}
