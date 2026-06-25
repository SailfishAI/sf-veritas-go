package sfveritas

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Benchmarks for the SDK hot paths. Run:
//
//	go test -bench=. -benchmem -run='^$' ./
//
// The "no-SDK" baseline is simply leaving globalConfig nil — every hot path
// short-circuits on getConfig()==nil. benchWithSDK configures the SDK and points
// the transmitter at a local discard server so enqueue + background send are
// exercised without real network egress. See BENCHMARKS.md.

// benchWithSDK configures the SDK and returns a restore func. The transmitter is
// built WITHOUT a background sender goroutine on purpose: nonBlockingPost then
// fills the buffered channel (or drops when full) without any network I/O. This
// isolates the *caller-side* hot-path cost — the synchronous overhead a request
// actually pays — from the async background send (which is off the request path
// in production), and keeps b.ReportAllocs free of cross-goroutine accounting.
func benchWithSDK(b *testing.B) func() {
	b.Helper()
	prevCfg, prevTx := globalConfig, globalTransmitter
	globalConfig = &config{
		apiKey:              "bench",
		serviceUUID:         "bench",
		argLimitBytes:       1 << 20,
		returnLimitBytes:    1 << 20,
		globalCaptureArgs:   true,
		globalCaptureReturn: true,
		autoCaptureChildren: true,
	}
	globalTransmitter = &transmitter{ch: make(chan transmitItem, 4096)}
	return func() { globalConfig, globalTransmitter = prevCfg, prevTx }
}

func benchNoSDK(b *testing.B) func() {
	b.Helper()
	prevCfg, prevTx := globalConfig, globalTransmitter
	globalConfig, globalTransmitter = nil, nil
	return func() { globalConfig, globalTransmitter = prevCfg, prevTx }
}

// --- Function spans ---

func BenchmarkFunctionSpan_StartEnd(b *testing.B) {
	defer benchWithSDK(b)()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := StartSpan(ctx, "BenchFn")
		s.End(nil)
	}
}

func BenchmarkFunctionSpan_StartEndWithArgs(b *testing.B) {
	defer benchWithSDK(b)()
	ctx := context.Background()
	args := map[string]interface{}{"id": 42, "name": "widget"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := StartSpanWithArgs(ctx, "BenchFn", args)
		s.End("ok")
	}
}

func BenchmarkFunctionSpan_Unconfigured(b *testing.B) {
	defer benchNoSDK(b)()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := StartSpan(ctx, "BenchFn")
		s.End(nil)
	}
}

// --- Log handler ---

func BenchmarkLogHandle_WithSDK(b *testing.B) {
	defer benchWithSDK(b)()
	h := NewHandler(slog.NewTextHandler(io.Discard, nil))
	rec := newBenchRecord()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, rec)
	}
}

func BenchmarkLogHandle_Baseline(b *testing.B) {
	defer benchNoSDK(b)()
	h := NewHandler(slog.NewTextHandler(io.Discard, nil))
	rec := newBenchRecord()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, rec)
	}
}

func newBenchRecord() slog.Record {
	r := slog.Record{Level: slog.LevelInfo, Message: "bench log line"}
	r.AddAttrs(slog.String("k", "v"), slog.Int("n", 7))
	return r
}

// --- Inbound middleware ---

func benchInboundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func BenchmarkMiddleware_WithSDK(b *testing.B) {
	defer benchWithSDK(b)()
	h := Middleware(benchInboundHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/bench", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkMiddleware_Baseline(b *testing.B) {
	defer benchNoSDK(b)()
	h := benchInboundHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/bench", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// --- Outbound transport (against a local server; the loopback cost is in both
// arms, so the delta isolates the instrumentation overhead) ---

func BenchmarkTransport_RoundTrip_WithSDK(b *testing.B) {
	defer benchWithSDK(b)()
	upstream := httptest.NewServer(benchInboundHandler())
	defer upstream.Close()
	rt := &Transport{Base: http.DefaultTransport}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
		resp, err := rt.RoundTrip(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

func BenchmarkTransport_RoundTrip_Baseline(b *testing.B) {
	defer benchNoSDK(b)()
	upstream := httptest.NewServer(benchInboundHandler())
	defer upstream.Close()
	rt := http.DefaultTransport
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
		resp, err := rt.RoundTrip(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

// --- Non-blocking transmit enqueue ---

func BenchmarkNonBlockingPost(b *testing.B) {
	defer benchWithSDK(b)()
	vars := map[string]interface{}{"apiKey": "bench", "contents": "x"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nonBlockingPost("CollectLogs", mutationCollectLogs, vars)
	}
}
