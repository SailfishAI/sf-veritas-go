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
