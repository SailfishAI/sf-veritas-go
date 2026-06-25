# Performance benchmarks — sf-veritas-go

This documents how to measure the SDK's overhead and the numbers we observe. It is
the Go analog of the Python SDK's performance guide. Benchmarks live in
`benchmark_test.go` (Go `testing.B`) plus a standalone end-to-end runner in `bench/`.

## How to run

```bash
# Micro-benchmarks (per-operation cost: ns/op, B/op, allocs/op)
go test -bench=. -benchmem -run='^$' ./

# Stable mean/stddev across runs (optional; needs golang.org/x/perf/cmd/benchstat)
go test -bench=. -benchmem -run='^$' -count=10 ./ | benchstat -

# End-to-end with/without HTTP request latency (mean/median/stddev)
go run ./bench -n 5000 -warmup 500
```

## What is measured

The clean **"no-SDK" baseline is simply not configuring the SDK** — every hot path
short-circuits on `getConfig()==nil`. The micro-benchmarks isolate the **caller-side
synchronous cost** — the overhead a request actually pays — by building the transmitter
*without* its background sender, so the async network send (which is off the request
path in production) does not pollute the timing or the allocation accounting.

## Results

> Measured in a `golang:1.23` container on an OrbStack Linux VM (Apple Silicon host),
> `GOMAXPROCS=10`. **Numbers are hardware-dependent** — re-run on prod-like hardware for
> authoritative figures. The dominant cost on the span/log/middleware paths is Go stack
> introspection (`runtime.Caller` / `runtime.CallersFrames`), which is inherent.

### Per-operation (micro-benchmarks)

| Operation | With SDK | Baseline (no SDK) | Added overhead |
|---|---|---|---|
| Function span `StartSpan`+`End` (no args) | ~8.0 µs/op, 27 allocs | — (span always built) | ~8 µs/span |
| Function span `StartSpanWithArgs`+`End` | ~10.2 µs/op, 39 allocs | — | ~10 µs/span |
| Log capture (`Handler.Handle`) | ~9.1 µs/op, 14 allocs | ~0.36 µs/op | **~8.8 µs/log** |
| Inbound `Middleware` (per request) | ~9.2 µs/op, 29 allocs | ~0.23 µs/op | **~9 µs/request** |
| Outbound `Transport.RoundTrip` (loopback) | ~44 µs/op | ~23 µs/op | **~21 µs/outbound call** |
| `nonBlockingPost` (telemetry enqueue) | ~4.6 ns/op, 0 allocs | — | negligible |
| `fastUUID` | ~0.45 µs/op | — | — |

The telemetry **send is asynchronous** (a 4096-buffered channel, drop-on-full); enqueue
is ~5 ns. The cost a request pays synchronously is the capture/serialize work above, not
the HTTP send.

### End-to-end inbound request latency (with vs without `Middleware`)

5000 requests each, over the loopback `httptest` stack:

| Configuration | Mean | Median | StdDev |
|---|---|---|---|
| Without SDK | ~37 µs | ~14 µs | ~99 µs |
| With SDK (`Middleware`) | ~91 µs | ~44 µs | ~170 µs |

Median overhead **~+30 µs/request**. Note the end-to-end harness runs the async sender on
the *same machine* competing for CPU, which inflates the mean/stddev versus production
(where the collector is remote and the send is truly off-path); the **micro-benchmark
~9 µs/request** is the more representative synchronous overhead.

## Tuning levers (already in the SDK)

- **Function spans are the most expensive path (~8–10 µs each).** For hot functions,
  prefer **manual** `StartSpan`/`TraceFunc` on the calls you care about over instrumenting
  everything via `-toolexec`, or enable **sampling**: `SF_FUNCSPAN_ENABLE_SAMPLING=true`
  + `SF_FUNCSPAN_SAMPLE_RATE=0.1`.
- Disable argument/return capture (default off) to drop allocations:
  `SF_FUNCSPAN_CAPTURE_ARGUMENTS` / `SF_FUNCSPAN_CAPTURE_RETURN_VALUE`.
- Transmit batching/cadence: `SF_NBPOST_BATCH_MAX`, `SF_NBPOST_FLUSH_MS`.
- Skip noisy routes: `SF_DISABLE_INBOUND_NETWORK_TRACING_ON_ROUTES`.

## Not benchmarked here

- CI regression gating — benchmarks are noisy; run on-demand (as the Python SDK does).
- A profiler-overhead sampling sweep — N/A for Go: instrumentation is compile-time
  (`-toolexec`) or manual, not a runtime profiler like Python's `sys.setprofile`.
