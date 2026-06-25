package sfveritas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// End-to-end uplink test against a local coder/websocket server: handshake
// (query params + telemetry-outbound header + clientHello), rules push → ack,
// ping → pong, and budget → sessionExpired.
func TestUplinkIntegration(t *testing.T) {
	activeSession.Store(nil)
	clearFuncspanRules()

	type obs struct {
		query    map[string][]string
		outbound string
		hello    chan []byte
		ack      chan []byte
		pong     chan []byte
		expired  chan []byte
		done     chan struct{}
	}
	o := &obs{
		hello: make(chan []byte, 1), ack: make(chan []byte, 1),
		pong: make(chan []byte, 1), expired: make(chan []byte, 1),
		done: make(chan struct{}),
	}
	future := time.Now().Add(time.Minute).UnixMilli()
	rulesMsg := fmt.Sprintf(`{"type":"backendFunctionSpanRules","sessionId":"sess-1","rulesVersion":1,"expiresAtMs":%d,"budgets":{"maxSpansPerPod":2,"maxPayloadBytesPerPod":"1000000000"},"rules":[{"ruleId":"r1","target":{"filePattern":"*","functionName":"F"},"capture":{"args":true,"returnValue":true},"sampleRate":1.0}]}`, future)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/notify/", func(w http.ResponseWriter, r *http.Request) {
		o.query = r.URL.Query()
		o.outbound = r.Header.Get(telemetryOutboundHeader)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := context.Background()
		read := func(ch chan []byte) bool {
			_, data, err := c.Read(ctx)
			if err != nil {
				return false
			}
			ch <- data
			return true
		}
		if !read(o.hello) {
			return
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(rulesMsg))
		if !read(o.ack) {
			return
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"ping","ts":123}`))
		if !read(o.pong) {
			return
		}
		read(o.expired)
		<-o.done
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ws/notify/"
	cfg := &config{apiKey: "k", serviceUUID: "svc-uuid", serviceIdentifier: "svc", uplinkEnabled: true, uplinkURL: wsURL}
	u := &uplink{cfg: cfg, origClient: &http.Client{}, quit: make(chan struct{})}
	setSessionDeactivateCallback(u.sendSessionExpired)
	u.wg.Add(1)
	go u.run(wsURL)
	defer func() {
		close(o.done)
		u.quitOnce.Do(func() { close(u.quit) })
		stopped := make(chan struct{})
		go func() { u.wg.Wait(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("uplink did not stop within 3s")
		}
	}()

	recv := func(name string, ch chan []byte) map[string]any {
		select {
		case b := <-ch:
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("%s: bad json: %v", name, err)
			}
			return m
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", name)
			return nil
		}
	}

	hello := recv("clientHello", o.hello)
	if hello["type"] != "clientHello" || hello["runtime"] != "go" {
		t.Errorf("unexpected clientHello: %v", hello)
	}
	if o.query["apiKey"][0] != "k" || o.query["serviceUuid"][0] != "svc-uuid" || o.query["clientKind"][0] != "backendCollector" {
		t.Errorf("unexpected connect query: %v", o.query)
	}
	if o.outbound != "True" {
		t.Errorf("expected telemetry-outbound header True, got %q", o.outbound)
	}

	ack := recv("rulesAck", o.ack)
	if ack["type"] != "rulesAck" || ack["sessionId"] != "sess-1" || ack["podId"] != "svc-uuid" {
		t.Errorf("unexpected rulesAck: %v", ack)
	}
	if !funcspanSessionIsActive() {
		t.Error("expected session active after rules")
	}
	if !hasActiveRules() {
		t.Error("expected rules armed")
	}

	pong := recv("pong", o.pong)
	if pong["type"] != "pong" || fmt.Sprintf("%v", pong["ts"]) != "123" {
		t.Errorf("unexpected pong: %v", pong)
	}

	// Trigger the span budget (maxSpansPerPod=2) → sessionExpired{span_budget}.
	recordSpanEmitted(1)
	recordSpanEmitted(1)
	expired := recv("sessionExpired", o.expired)
	if expired["type"] != "sessionExpired" || expired["reason"] != "span_budget" || expired["sessionId"] != "sess-1" {
		t.Errorf("unexpected sessionExpired: %v", expired)
	}
	if funcspanSessionIsActive() {
		t.Error("session should be deactivated after budget")
	}
}
