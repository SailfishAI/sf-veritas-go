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
		batchMax := 512
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
			// Drain remaining items
			close(t.ch)
			for item := range t.ch {
				batch = append(batch, item)
			}
			if len(batch) > 0 {
				t.flush(batch)
			}
			return
		}
	}
}

func (t *transmitter) flush(batch []transmitItem) {
	if len(batch) == 0 {
		return
	}

	cfg := getConfig()

	// Reuse buffer from pool to avoid allocation per flush
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	enc := json.NewEncoder(buf)

	if len(batch) == 1 {
		item := batch[0]
		err := enc.Encode(map[string]interface{}{
			"query":         item.query,
			"variables":     item.variables,
			"operationName": item.operationName,
		})
		if err != nil {
			if cfg != nil && cfg.debug {
				fmt.Fprintf(os.Stderr, "[sfveritas] JSON marshal error: %v\n", err)
			}
			return
		}
	} else {
		payloads := make([]map[string]interface{}, 0, len(batch))
		for _, item := range batch {
			payloads = append(payloads, map[string]interface{}{
				"query":         item.query,
				"variables":     item.variables,
				"operationName": item.operationName,
			})
		}
		if err := enc.Encode(payloads); err != nil {
			if cfg != nil && cfg.debug {
				fmt.Fprintf(os.Stderr, "[sfveritas] JSON marshal error: %v\n", err)
			}
			return
		}
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
		fmt.Fprintf(os.Stderr, "[sfveritas] Sent batch of %d items, status=%d\n", len(batch), resp.StatusCode)
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

// Shutdown flushes any remaining telemetry and stops the background goroutine.
func Shutdown() {
	t := globalTransmitter
	if t == nil {
		return
	}
	close(t.quit)
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
