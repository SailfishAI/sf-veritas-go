package veritasgin_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	sfveritas "github.com/SailfishAI/sf-veritas-go"
	veritasgin "github.com/SailfishAI/sf-veritas-go/gin"
)

// TestMiddlewareCapturesPanicAndError verifies the Gin middleware reports a
// handler panic to Sailfish WITH a stack trace that includes the handler frame,
// and reports a handled 5xx — wired exactly like the documented prod setup
// (gin.Recovery present, sfveritas.Middleware wrapping the engine).
func TestMiddlewareCapturesPanicAndError(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer sink.Close()

	t.Setenv("SAILFISH_GRAPHQL_ENDPOINT", sink.URL+"/graphql/")
	t.Setenv("SF_NBPOST_FLUSH_MS", "1")
	sfveritas.SetupInterceptors(sfveritas.Options{APIKey: "test-key", ServiceIdentifier: "gin-test"})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())          // mimic gin.Default()'s built-in recovery (outer)
	r.Use(veritasgin.Middleware()) // our middleware (inner — recovers first)
	r.GET("/boom", func(c *gin.Context) { panic("kaboom in handler") })
	r.GET("/err", func(c *gin.Context) { c.AbortWithStatus(http.StatusInternalServerError) })

	srv := httptest.NewServer(sfveritas.Middleware(r))
	defer srv.Close()

	for _, path := range []string{"/boom", "/err"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != 500 {
			t.Errorf("GET %s: expected 500, got %d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	time.Sleep(150 * time.Millisecond)
	sfveritas.Shutdown() // drains the transmit queue

	mu.Lock()
	joined := strings.Join(bodies, "\n")
	mu.Unlock()

	if !strings.Contains(joined, "CollectExceptions") {
		t.Fatalf("no CollectExceptions sent to sink; bodies=%d", len(bodies))
	}
	if !strings.Contains(joined, "kaboom in handler") {
		t.Errorf("panic message not captured")
	}
	// Proof of stack capture: the panicking handler is a closure in THIS test file.
	if !strings.Contains(joined, "veritasgin_test.go") {
		t.Errorf("captured stack does not include the handler frame (no veritasgin_test.go)")
	}
}
