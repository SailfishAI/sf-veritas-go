package sfveritas

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The WebSocket "uplink" — a long-lived control channel to the Sailfish backend
// that turns runtime function-span debug capture on/off ("backend debugger").
// Go port of the Python SDK's uplink_client.py. The backend already supports
// clientKind=backendCollector on /ws/notify/; this is the client side.

type uplink struct {
	cfg        *config
	origClient *http.Client // dials over the ORIGINAL (un-instrumented) transport
	quit       chan struct{}
	quitOnce   sync.Once
	wg         sync.WaitGroup
	conn       atomic.Pointer[websocket.Conn]
	writeMu    sync.Mutex
}

var (
	globalUplink *uplink
	uplinkOnce   sync.Once
)

// startUplink launches the uplink goroutine (gated on SF_UPLINK_ENABLE). It is
// given the original http transport so the WS handshake is never re-instrumented
// by the patched http.DefaultTransport.
func startUplink(cfg *config, origTransport http.RoundTripper) {
	if cfg == nil || !cfg.uplinkEnabled {
		return
	}
	baseURL, ok := deriveUplinkURL(cfg)
	if !ok {
		if cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] uplink: could not derive ws URL from endpoint %q; disabled\n", cfg.graphqlEndpoint)
		}
		return
	}
	uplinkOnce.Do(func() {
		u := &uplink{
			cfg:        cfg,
			origClient: &http.Client{Transport: origTransport},
			quit:       make(chan struct{}),
		}
		globalUplink = u
		setSessionDeactivateCallback(u.sendSessionExpired)
		u.wg.Add(1)
		go u.run(baseURL)
	})
}

func deriveUplinkURL(cfg *config) (string, bool) {
	if cfg.uplinkURL != "" {
		return cfg.uplinkURL, true
	}
	if cfg.graphqlEndpoint == "" {
		return "", false
	}
	pu, err := url.Parse(cfg.graphqlEndpoint)
	if err != nil {
		return "", false
	}
	var scheme string
	switch pu.Scheme {
	case "https":
		scheme = "wss"
	case "http":
		scheme = "ws"
	default:
		return "", false
	}
	return (&url.URL{Scheme: scheme, Host: pu.Host, Path: wsNotifyPath}).String(), true
}

func buildQueryString(cfg *config) string {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("apiKey", cfg.apiKey)
	set("serviceUuid", cfg.serviceUUID)
	set("serviceIdentifier", cfg.serviceIdentifier)
	set("serviceVersion", cfg.serviceVersion)
	v.Set("clientKind", clientKindBackendCollector)
	return v.Encode()
}

func (u *uplink) run(baseURL string) {
	defer u.wg.Done()
	backoff := 1.0
	for {
		select {
		case <-u.quit:
			return
		default:
		}
		connected, err := u.connectAndRun(baseURL)
		select {
		case <-u.quit:
			return
		default:
		}
		if connected {
			backoff = 1.0 // reset after a real connection
		}
		jitter := backoff * 0.2 * (rand.Float64()*2 - 1)
		wait := math.Min(30, math.Max(1, backoff+jitter))
		if u.cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] uplink disconnected (%v); reconnecting in %.1fs\n", err, wait)
		}
		select {
		case <-u.quit:
			return
		case <-time.After(time.Duration(wait * float64(time.Second))):
		}
		backoff = math.Min(30, backoff*1.5)
	}
}

func (u *uplink) connectAndRun(baseURL string) (connected bool, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	full := baseURL + "?" + buildQueryString(u.cfg)
	hdr := http.Header{}
	hdr.Set(telemetryOutboundHeader, "True")
	hdr.Set("User-Agent", "sf-veritas-go/"+Version)

	conn, _, derr := websocket.Dial(ctx, full, &websocket.DialOptions{
		HTTPClient: u.origClient,
		HTTPHeader: hdr,
	})
	if derr != nil {
		return false, derr
	}
	conn.SetReadLimit(1 << 20)
	u.conn.Store(conn)
	defer func() {
		u.conn.Store(nil)
		conn.Close(websocket.StatusNormalClosure, "")
	}()

	// Stop everything when shutdown is requested.
	go func() {
		select {
		case <-u.quit:
			cancel()
		case <-ctx.Done():
		}
	}()

	_ = u.send(ctx, conn, map[string]any{
		"type": "clientHello", "sdkVersion": Version, "runtime": "go", "pid": os.Getpid(),
	})

	// 20s protocol keepalive (coder/websocket does not auto-ping).
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 10*time.Second)
				_ = conn.Ping(pctx)
				pc()
			case <-ctx.Done():
				return
			}
		}
	}()

	// 1Hz TTL pump.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				funcspanSessionCheckTTL()
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, data, rerr := conn.Read(ctx)
		if rerr != nil {
			return true, rerr
		}
		u.dispatch(ctx, conn, data)
	}
}

func (u *uplink) dispatch(ctx context.Context, conn *websocket.Conn, data []byte) {
	var env struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &env) != nil {
		return
	}
	switch env.Type {
	case "backendFunctionSpanRules":
		u.handleRules(ctx, conn, data)
	case "ping":
		var p struct {
			Ts any `json:"ts"`
		}
		_ = json.Unmarshal(data, &p)
		_ = u.send(ctx, conn, map[string]any{"type": "pong", "ts": p.Ts})
	}
	// unknown types ignored (forward-compat)
}

type directive struct {
	SessionID    string `json:"sessionId"`
	RulesVersion int    `json:"rulesVersion"`
	ExpiresAtMs  int64  `json:"expiresAtMs"`
	Budgets      struct {
		MaxSpansPerPod        int64  `json:"maxSpansPerPod"`
		MaxPayloadBytesPerPod string `json:"maxPayloadBytesPerPod"`
	} `json:"budgets"`
	Rules []json.RawMessage `json:"rules"`
}

func (u *uplink) handleRules(ctx context.Context, conn *websocket.Conn, data []byte) {
	var d directive
	if json.Unmarshal(data, &d) != nil {
		return
	}
	if len(d.Rules) == 0 {
		clearFuncspanRules()
		if funcspanSessionIsActive() {
			funcspanSessionDeactivate("cancelled")
		}
		return
	}
	var maxBytes int64
	if d.Budgets.MaxPayloadBytesPerPod != "" {
		if n, perr := strconv.ParseInt(d.Budgets.MaxPayloadBytesPerPod, 10, 64); perr == nil {
			maxBytes = n
		}
	}
	if !funcspanSessionActivate(d.SessionID, d.RulesVersion, d.ExpiresAtMs, d.Budgets.MaxSpansPerPod, maxBytes) {
		if u.cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] uplink: rejected directive session=%s (expired/malformed)\n", d.SessionID)
		}
		return
	}
	setFuncspanRules(d.Rules)
	_ = u.send(ctx, conn, map[string]any{
		"type": "rulesAck", "sessionId": d.SessionID, "rulesVersion": d.RulesVersion, "podId": u.cfg.serviceUUID,
	})
}

// sendSessionExpired is the session deactivate-callback. It may run from any
// goroutine (budget deactivation in Span.End, TTL pump, shutdown).
func (u *uplink) sendSessionExpired(snap sessionSnapshot, reason string) {
	conn := u.conn.Load()
	if conn == nil {
		return
	}
	_ = u.send(context.Background(), conn, map[string]any{
		"type":         "sessionExpired",
		"sessionId":    snap.SessionID,
		"reason":       reason,
		"spansEmitted": snap.SpansEmitted,
		"bytesEmitted": strconv.FormatInt(snap.BytesEmitted, 10),
	})
}

func (u *uplink) send(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// stopUplink deactivates any active session (→ sessionExpired "shutdown") and
// closes the socket, bounded to ~2s. Idempotent.
func stopUplink() {
	u := globalUplink
	if u == nil {
		return
	}
	if funcspanSessionIsActive() {
		funcspanSessionDeactivate("shutdown")
	}
	u.quitOnce.Do(func() { close(u.quit) })
	done := make(chan struct{})
	go func() { u.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
