package sfveritas

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type transmitItem struct {
	query         string
	variables     map[string]interface{}
	operationName string
}

// bufPool reuses byte buffers for JSON serialization to reduce GC pressure.
var bufPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 4096))
	},
}

type transmitter struct {
	ch       chan transmitItem
	client   *http.Client
	endpoint string
	wg       sync.WaitGroup
	quit     chan struct{}
	quitOnce sync.Once

	batchMax    int
	flushMs     int
	gzipEnabled bool
}

var (
	globalTransmitter *transmitter
	transmitterOnce   sync.Once
)

func initTransmitter(endpoint string) *transmitter {
	transmitterOnce.Do(func() {
		// Default 1: telemetry is sent one operation per HTTP request (see flush),
		// because the Sailfish GraphQL endpoint does not support GraphQL transport
		// batching. The batch buffer only groups how many items a flush drains; it
		// never affects the wire shape (each item is still its own POST), so this is
		// a conservative default rather than a correctness requirement.
		batchMax := 1
		if v := os.Getenv("SF_NBPOST_BATCH_MAX"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				batchMax = n
			}
		}

		flushMs := 2
		if v := os.Getenv("SF_NBPOST_FLUSH_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				flushMs = n
			}
		}

		// Configurable timeouts
		connectTimeout := 100 * time.Millisecond
		if v := os.Getenv("SF_NBPOST_CONNECT_TIMEOUT"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				connectTimeout = d
			}
		}
		totalTimeout := 700 * time.Millisecond
		if v := os.Getenv("SF_NBPOST_TOTAL_TIMEOUT"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				totalTimeout = d
			}
		}

		// Gzip compression
		gzipEnabled := os.Getenv("SF_NBPOST_GZIP") == "1"

		globalTransmitter = &transmitter{
			ch:          make(chan transmitItem, 4096),
			endpoint:    endpoint,
			quit:        make(chan struct{}),
			batchMax:    batchMax,
			flushMs:     flushMs,
			gzipEnabled: gzipEnabled,
			client: &http.Client{
				Transport: &http.Transport{
					DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
					MaxIdleConns:        20,
					MaxIdleConnsPerHost: 20,
					IdleConnTimeout:     30 * time.Second,
				},
				Timeout: totalTimeout,
			},
		}
		globalTransmitter.wg.Add(1)
		go globalTransmitter.run()
	})
	return globalTransmitter
}

func (t *transmitter) run() {
	defer t.wg.Done()
	flushInterval := time.Duration(t.flushMs) * time.Millisecond
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([]transmitItem, 0, t.batchMax)

	for {
		select {
		case item := <-t.ch:
			batch = append(batch, item)
			if len(batch) >= t.batchMax {
				t.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				t.flush(batch)
				batch = batch[:0]
			}
		case <-t.quit:
			// Drain what is buffered right now, then stop. We deliberately do NOT
			// close(t.ch): a request still in flight during graceful shutdown can
			// call nonBlockingPost concurrently, and a send on a closed channel
			// panics even inside a select-with-default. Leaving the channel open
			// (abandoned after we return) makes a late send a harmless no-op into
			// the buffer instead of a process-killing panic. Items that arrive
			// after this drain are dropped — acceptable fire-and-forget semantics.
			//
			// We snapshot len(t.ch) and read exactly that many rather than looping
			// until empty: we are the only reader, so those items are guaranteed
			// present, and a steady stream of concurrent sends can't starve us into
			// draining forever.
			for n := len(t.ch); n > 0; n-- {
				batch = append(batch, <-t.ch)
			}
			if len(batch) > 0 {
				t.flush(batch)
			}
			return
		}
	}
}

// flush delivers every buffered item. Each telemetry item is sent as its OWN
// HTTP POST (one GraphQL operation per request). The Sailfish GraphQL endpoint
// (Strawberry) does not support GraphQL transport batching — a top-level JSON
// array of operations is rejected with HTTP 500 ("'list' object has no attribute
// 'get'"). The batch buffer in run() therefore only controls flush *timing*, not
// the wire shape. This mirrors the Java/JS SDKs, which also send one op per request.
func (t *transmitter) flush(batch []transmitItem) {
	for i := range batch {
		t.postOne(batch[i])
	}
}

// postOne sends a single GraphQL operation as one HTTP POST with a JSON object
// body {query, variables, operationName}.
func (t *transmitter) postOne(item transmitItem) {
	cfg := getConfig()

	// Reuse buffer from pool to avoid allocation per send
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	enc := json.NewEncoder(buf)
	if err := enc.Encode(map[string]interface{}{
		"query":         item.query,
		"variables":     item.variables,
		"operationName": item.operationName,
	}); err != nil {
		if cfg != nil && cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] JSON marshal error: %v\n", err)
		}
		return
	}

	var req *http.Request
	if t.gzipEnabled {
		gzBuf := bufPool.Get().(*bytes.Buffer)
		gzBuf.Reset()
		gw := gzip.NewWriter(gzBuf)
		gw.Write(buf.Bytes())
		gw.Close()
		var err error
		req, err = http.NewRequest("POST", t.endpoint, gzBuf)
		if err != nil {
			bufPool.Put(gzBuf)
			if cfg != nil && cfg.debug {
				fmt.Fprintf(os.Stderr, "[sfveritas] HTTP request create error: %v\n", err)
			}
			return
		}
		req.Header.Set("Content-Encoding", "gzip")
		// gzBuf will be read by the client, return it after request completes
		defer bufPool.Put(gzBuf)
	} else {
		var err error
		req, err = http.NewRequest("POST", t.endpoint, buf)
		if err != nil {
			if cfg != nil && cfg.debug {
				fmt.Fprintf(os.Stderr, "[sfveritas] HTTP request create error: %v\n", err)
			}
			return
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(telemetryOutboundHeader, "True")

	resp, err := t.client.Do(req)
	if err != nil {
		if cfg != nil && cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] HTTP send error: %v\n", err)
		}
		return
	}
	resp.Body.Close()

	if cfg != nil && cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Sent %s, status=%d\n", item.operationName, resp.StatusCode)
	}
}

// nonBlockingPost enqueues a GraphQL mutation for background delivery.
func nonBlockingPost(operationName, query string, variables map[string]interface{}) {
	t := globalTransmitter
	if t == nil {
		return
	}
	select {
	case t.ch <- transmitItem{
		query:         query,
		variables:     variables,
		operationName: operationName,
	}:
	default:
		// Channel full — drop item silently (fire-and-forget).
		if cfg := getConfig(); cfg != nil && cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] Transmit channel full, dropping %s\n", operationName)
		}
	}
}

// Shutdown flushes any remaining telemetry and stops the background goroutines
// (transmitter + uplink). Idempotent.
func Shutdown() {
	// Tear down the WS uplink first so it can send sessionExpired("shutdown")
	// before transports go away.
	stopUplink()
	t := globalTransmitter
	if t == nil {
		return
	}
	// Idempotent: a second Shutdown (e.g. defer + signal handler) must not
	// double-close t.quit, which would panic.
	t.quitOnce.Do(func() {
		close(t.quit)
	})
	t.wg.Wait()
}

// getDefaultVariables returns the common variables for all mutations.
func getDefaultVariables() map[string]interface{} {
	cfg := getConfig()
	if cfg == nil {
		return nil
	}
	return map[string]interface{}{
		"apiKey":      cfg.apiKey,
		"serviceUuid": cfg.serviceUUID,
		"timestampMs": strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
}

// mergeVariables merges default variables with additional ones.
func mergeVariables(additional map[string]interface{}) map[string]interface{} {
	vars := getDefaultVariables()
	if vars == nil {
		return additional
	}
	for k, v := range additional {
		vars[k] = v
	}
	return vars
}
