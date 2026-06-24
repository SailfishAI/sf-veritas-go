package sfveritas

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// safeSend mimics nonBlockingPost's non-blocking enqueue. With the old shutdown
// code (close(t.ch)), this would panic "send on closed channel" once run() had
// drained; the fixed run() never closes the channel, so a late send is a no-op.
func safeSend(tr *transmitter, item transmitItem) {
	select {
	case tr.ch <- item:
	default:
	}
}

// TestShutdownIsRaceFreeWithConcurrentPosts hammers the transmitter with
// concurrent enqueues while it shuts down. It must drain and return without a
// "send on closed channel" panic. Regression test for the graceful-shutdown
// crash (a request emitting telemetry during Shutdown took the process down).
func TestShutdownIsRaceFreeWithConcurrentPosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	tr := &transmitter{
		ch:       make(chan transmitItem, 64),
		quit:     make(chan struct{}),
		endpoint: srv.URL,
		client:   srv.Client(),
		batchMax: 1,
		flushMs:  1,
	}
	tr.wg.Add(1)
	go tr.run()

	// Bounded senders: each fires a fixed number of posts, then exits. (An
	// unbounded sender loop would never let run() drain to a stop and would
	// deadlock the test.) These race the shutdown below — the part that used to
	// panic "send on closed channel".
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				safeSend(tr, transmitItem{query: "mutation X { x }", operationName: "X"})
			}
		}()
	}

	// Shut down concurrently with the in-flight senders.
	tr.quitOnce.Do(func() { close(tr.quit) })

	// Wait for senders to finish FIRST, then for run() to return. Ordering
	// matters: run() can only retire once nothing else is touching the channel.
	wg.Wait()
	tr.wg.Wait()

	// Keep sending AFTER the worker has stopped; must remain a safe no-op, never
	// a panic (it would panic if run() had close()d the channel).
	for i := 0; i < 100; i++ {
		safeSend(tr, transmitItem{query: "mutation Y { y }", operationName: "Y"})
	}
}

// TestShutdownIsIdempotent verifies a second Shutdown of the same transmitter
// does not double-close t.quit (which would panic).
func TestShutdownIsIdempotent(t *testing.T) {
	tr := &transmitter{
		ch:       make(chan transmitItem, 4),
		quit:     make(chan struct{}),
		batchMax: 1,
		flushMs:  1,
	}
	tr.wg.Add(1)
	go tr.run()

	shutdown := func() {
		tr.quitOnce.Do(func() { close(tr.quit) })
		tr.wg.Wait()
	}
	shutdown()
	shutdown() // second call must be a no-op, not a panic
}

// TestFlushSendsOneObjectPerItem verifies that a multi-item batch is delivered as
// N separate HTTP POSTs, each with a JSON OBJECT body (never a top-level array).
// The Sailfish GraphQL endpoint does not support GraphQL transport batching.
func TestFlushSendsOneObjectPerItem(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	tr := &transmitter{endpoint: srv.URL, client: srv.Client()}

	batch := []transmitItem{
		{query: "mutation A { a }", variables: map[string]interface{}{"x": 1}, operationName: "A"},
		{query: "mutation B { b }", variables: map[string]interface{}{"y": 2}, operationName: "B"},
		{query: "mutation C { c }", variables: nil, operationName: "C"},
	}
	tr.flush(batch)

	mu.Lock()
	defer mu.Unlock()

	if len(bodies) != len(batch) {
		t.Fatalf("expected %d separate POSTs (one per item), got %d", len(batch), len(bodies))
	}
	for i, b := range bodies {
		trimmed := bytes.TrimSpace(b)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			t.Fatalf("body %d is not a top-level JSON object (got %q)", i, string(b))
		}
		var obj map[string]interface{}
		if err := json.Unmarshal(b, &obj); err != nil {
			t.Fatalf("body %d did not decode to an object: %v", i, err)
		}
		if _, ok := obj["query"]; !ok {
			t.Fatalf("body %d missing 'query' key: %v", i, obj)
		}
	}
}

// TestFlushGzip verifies the gzip path still sends one decodable object per item.
func TestFlushGzip(t *testing.T) {
	var mu sync.Mutex
	var count int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("expected gzip Content-Encoding, got %q", r.Header.Get("Content-Encoding"))
		}
		gr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip reader: %v", err)
			return
		}
		b, _ := io.ReadAll(gr)
		var obj map[string]interface{}
		if err := json.Unmarshal(b, &obj); err != nil {
			t.Errorf("gunzipped body not an object: %v (%q)", err, string(b))
		}
		mu.Lock()
		count++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	tr := &transmitter{endpoint: srv.URL, client: srv.Client(), gzipEnabled: true}
	tr.flush([]transmitItem{
		{query: "mutation A { a }", operationName: "A"},
		{query: "mutation B { b }", operationName: "B"},
	})

	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 gzip POSTs, got %d", count)
	}
}
