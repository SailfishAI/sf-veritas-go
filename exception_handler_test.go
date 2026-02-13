package sfveritas

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- readCodeSnippet ---

func TestReadCodeSnippet_EmptyFile(t *testing.T) {
	got := readCodeSnippet("", 1)
	if got != "" {
		t.Errorf("expected empty string for empty file path, got %q", got)
	}
}

func TestReadCodeSnippet_ZeroLine(t *testing.T) {
	got := readCodeSnippet("some/file.go", 0)
	if got != "" {
		t.Errorf("expected empty string for line 0, got %q", got)
	}
}

func TestReadCodeSnippet_NegativeLine(t *testing.T) {
	got := readCodeSnippet("some/file.go", -5)
	if got != "" {
		t.Errorf("expected empty string for negative line, got %q", got)
	}
}

func TestReadCodeSnippet_NonexistentFile(t *testing.T) {
	got := readCodeSnippet("/nonexistent/file.go", 1)
	if got != "" {
		t.Errorf("expected empty string for nonexistent file, got %q", got)
	}
}

func TestReadCodeSnippet_LineExceedsFileLength(t *testing.T) {
	// This file itself has content, so line 999999 should be out of range
	got := readCodeSnippet("exception_handler_test.go", 999999)
	if got != "" {
		t.Errorf("expected empty string for line beyond file, got %q", got)
	}
}

// --- captureStackTrace ---

func TestCaptureStackTrace_ReturnsFrames(t *testing.T) {
	frames := captureStackTrace(2) // skip self + caller
	if len(frames) == 0 {
		t.Fatal("expected at least one frame")
	}
	// First frame should be the test function or testing infrastructure
	found := false
	for _, f := range frames {
		if f.Function != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one frame with a function name")
	}
}

func TestCaptureStackTrace_DepthLimiting(t *testing.T) {
	// Save and restore
	saved := globalConfig
	globalConfig = &config{stackDepthCodeTraceDepth: 2}
	defer func() { globalConfig = saved }()

	frames := captureStackTrace(2)
	// maxFrames=2 means we capture at most 3 frames (depth+1, matching JS/TS)
	if len(frames) > 3 {
		t.Errorf("expected at most 3 frames with depth=2, got %d", len(frames))
	}
}

func TestCaptureStackTrace_UnlimitedDepth(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{stackDepthCodeTraceDepth: -1}
	defer func() { globalConfig = saved }()

	frames := captureStackTrace(2)
	if len(frames) < 2 {
		t.Errorf("expected multiple frames with unlimited depth, got %d", len(frames))
	}
}

func TestCaptureStackTrace_ZeroDepth(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{stackDepthCodeTraceDepth: 0}
	defer func() { globalConfig = saved }()

	frames := captureStackTrace(2)
	// maxFrames=0 means at most 1 frame (0+1)
	if len(frames) > 1 {
		t.Errorf("expected at most 1 frame with depth=0, got %d", len(frames))
	}
}

// --- isDuplicateException ---

func TestIsDuplicateException_FirstOccurrence(t *testing.T) {
	// Reset dedup state
	exceptionDedupMu.Lock()
	exceptionDedup = [exceptionDedupSize]dedupEntry{}
	exceptionDedupIndex = 0
	exceptionDedupMu.Unlock()

	if isDuplicateException("unique-error-12345") {
		t.Error("expected first occurrence to NOT be duplicate")
	}
}

func TestIsDuplicateException_SecondOccurrence(t *testing.T) {
	// Reset dedup state
	exceptionDedupMu.Lock()
	exceptionDedup = [exceptionDedupSize]dedupEntry{}
	exceptionDedupIndex = 0
	exceptionDedupMu.Unlock()

	msg := "repeated-error-67890"
	isDuplicateException(msg) // first time
	if !isDuplicateException(msg) {
		t.Error("expected second occurrence to be detected as duplicate")
	}
}

func TestIsDuplicateException_DifferentMessages(t *testing.T) {
	exceptionDedupMu.Lock()
	exceptionDedup = [exceptionDedupSize]dedupEntry{}
	exceptionDedupIndex = 0
	exceptionDedupMu.Unlock()

	isDuplicateException("error-A")
	if isDuplicateException("error-B") {
		t.Error("expected different message to NOT be duplicate")
	}
}

func TestIsDuplicateException_ExpiredEntry(t *testing.T) {
	exceptionDedupMu.Lock()
	exceptionDedup = [exceptionDedupSize]dedupEntry{}
	exceptionDedupIndex = 0
	// Manually insert an expired entry
	exceptionDedup[0] = dedupEntry{
		message:   "expired-error",
		timestamp: time.Now().Add(-10 * time.Second), // older than TTL
	}
	exceptionDedupIndex = 1
	exceptionDedupMu.Unlock()

	if isDuplicateException("expired-error") {
		t.Error("expected expired entry to NOT be detected as duplicate")
	}
}

func TestIsDuplicateException_RingBufferWraps(t *testing.T) {
	exceptionDedupMu.Lock()
	exceptionDedup = [exceptionDedupSize]dedupEntry{}
	exceptionDedupIndex = 0
	exceptionDedupMu.Unlock()

	// Fill the ring buffer
	for i := 0; i < exceptionDedupSize; i++ {
		isDuplicateException(fmt.Sprintf("error-%d", i))
	}

	// The first error should have been evicted
	if isDuplicateException(fmt.Sprintf("error-%d", exceptionDedupSize)) {
		t.Error("new error after buffer wrap should not be duplicate")
	}
}

func TestIsDuplicateException_ConcurrentAccess(t *testing.T) {
	exceptionDedupMu.Lock()
	exceptionDedup = [exceptionDedupSize]dedupEntry{}
	exceptionDedupIndex = 0
	exceptionDedupMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			isDuplicateException(fmt.Sprintf("concurrent-error-%d", idx%10))
		}(i)
	}
	wg.Wait()
	// No panic means mutex is working
}

// --- unwrapErrorChain ---

func TestUnwrapErrorChain_SimpleError(t *testing.T) {
	err := errors.New("simple error")
	msg, causes := unwrapErrorChain(err)
	if msg != "simple error" {
		t.Errorf("expected 'simple error', got %s", msg)
	}
	if len(causes) != 0 {
		t.Errorf("expected no causes, got %d", len(causes))
	}
}

func TestUnwrapErrorChain_WrappedError(t *testing.T) {
	inner := errors.New("inner error")
	outer := fmt.Errorf("outer error: %w", inner)
	msg, causes := unwrapErrorChain(outer)
	if msg != "outer error: inner error" {
		t.Errorf("expected 'outer error: inner error', got %s", msg)
	}
	if len(causes) != 1 {
		t.Fatalf("expected 1 cause, got %d", len(causes))
	}
	if causes[0]["message"] != "inner error" {
		t.Errorf("expected cause message 'inner error', got %v", causes[0]["message"])
	}
}

func TestUnwrapErrorChain_DeepChain(t *testing.T) {
	err1 := errors.New("root cause")
	err2 := fmt.Errorf("middle: %w", err1)
	err3 := fmt.Errorf("top level: %w", err2)
	_, causes := unwrapErrorChain(err3)
	if len(causes) != 2 {
		t.Fatalf("expected 2 causes, got %d", len(causes))
	}
	if causes[0]["message"] != "middle: root cause" {
		t.Errorf("expected first cause 'middle: root cause', got %v", causes[0]["message"])
	}
	if causes[1]["message"] != "root cause" {
		t.Errorf("expected second cause 'root cause', got %v", causes[1]["message"])
	}
}

// --- timestampMs ---

func TestTimestampMs_ReturnsNonEmpty(t *testing.T) {
	ts := timestampMs()
	if ts == "" {
		t.Error("expected non-empty timestamp")
	}
	if len(ts) < 13 {
		t.Errorf("expected 13+ digit millisecond timestamp, got %s (len=%d)", ts, len(ts))
	}
}

// --- stackFrame serialization ---

func TestStackFrame_HasExpectedFields(t *testing.T) {
	sf := stackFrame{
		File:     "/app/main.go",
		Line:     42,
		Function: "main.handler",
		Code:     "return nil",
	}
	if sf.File != "/app/main.go" {
		t.Error("unexpected File")
	}
	if sf.Line != 42 {
		t.Error("unexpected Line")
	}
	if sf.Function != "main.handler" {
		t.Error("unexpected Function")
	}
	if sf.Code != "return nil" {
		t.Error("unexpected Code")
	}
}

func TestStackFrame_LocalsOmitEmpty(t *testing.T) {
	sf := stackFrame{
		File:     "/app/main.go",
		Line:     1,
		Function: "main",
	}
	// Locals should be omitempty, so empty string means it's omitted in JSON
	if sf.Locals != "" {
		t.Error("expected empty Locals by default")
	}
}
