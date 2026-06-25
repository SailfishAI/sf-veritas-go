// Command bench measures the end-to-end per-request latency added by the Go SDK's
// inbound middleware — the Go analog of the Python SDK README's with/without
// benchmark table. It serves a trivial handler with and without
// sfveritas.Middleware, fires N sequential requests against each, and prints
// Mean / Median / StdDev (+ overhead).
//
//	go run ./bench            # default N
//	go run ./bench -n 5000
//
// The SDK is pointed at a local discard collector so telemetry is enqueued and
// background-sent without real network egress (matching production's
// non-blocking transport). Numbers are hardware-dependent — see BENCHMARKS.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"time"

	sfveritas "github.com/SailfishAI/sf-veritas-go"
)

func main() {
	n := flag.Int("n", 2000, "number of requests per configuration")
	warmup := flag.Int("warmup", 200, "warmup requests (discarded)")
	flag.Parse()

	// Discard collector so SDK telemetry has somewhere to go without egress.
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer collector.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// Baseline: bare handler, SDK not configured.
	base := measure(handler, *n, *warmup)

	// With SDK: configure + wrap with Middleware.
	sfveritas.SetupInterceptors(sfveritas.Options{
		APIKey:          "bench",
		GraphQLEndpoint: collector.URL + "/graphql/",
	})
	defer sfveritas.Shutdown()
	withSDK := measure(sfveritas.Middleware(handler), *n, *warmup)

	fmt.Printf("\nEnd-to-end inbound request latency (%d requests each, after %d warmup)\n", *n, *warmup)
	fmt.Printf("%-22s %10s %10s %10s\n", "Configuration", "Mean(µs)", "Median(µs)", "StdDev(µs)")
	printRow("Without SDK", base)
	printRow("With SDK (Middleware)", withSDK)
	fmt.Printf("\nOverhead: mean +%.2f µs (%.1f%%), median +%.2f µs\n",
		withSDK.mean-base.mean, pct(base.mean, withSDK.mean), withSDK.median-base.median)
}

type stats struct{ mean, median, stddev float64 }

func measure(h http.Handler, n, warmup int) stats {
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := srv.Client()
	url := srv.URL

	do := func() {
		resp, err := client.Get(url)
		if err != nil {
			fmt.Fprintln(os.Stderr, "request error:", err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	for i := 0; i < warmup; i++ {
		do()
	}
	samples := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		do()
		samples = append(samples, float64(time.Since(start).Microseconds()))
	}
	return computeStats(samples)
}

func computeStats(xs []float64) stats {
	if len(xs) == 0 {
		return stats{}
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var sq float64
	for _, x := range xs {
		d := x - mean
		sq += d * d
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	return stats{mean: mean, median: median, stddev: math.Sqrt(sq / float64(len(xs)))}
}

func pct(base, with float64) float64 {
	if base == 0 {
		return 0
	}
	return (with - base) / base * 100
}

func printRow(name string, s stats) {
	fmt.Printf("%-22s %10.2f %10.2f %10.2f\n", name, s.mean, s.median, s.stddev)
}
