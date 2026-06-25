// Package sfveritas is the Sailfish telemetry collector for Go applications.
// It captures logs, print statements, exceptions/panics, HTTP request/response
// telemetry, and function execution spans, sending them to the Sailfish backend
// via a non-blocking batched GraphQL transmitter.
//
// Quick start:
//
//	import "github.com/SailfishAI/sf-veritas-go"
//
//	func main() {
//	    sfveritas.SetupInterceptors(sfveritas.Options{
//	        APIKey:            "your-api-key",
//	        ServiceIdentifier: "my-service",
//	    })
//	    defer sfveritas.Shutdown()
//
//	    mux := http.NewServeMux()
//	    // ... register handlers ...
//	    http.ListenAndServe(":8080", sfveritas.Middleware(mux))
//	}
package sfveritas

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

// SetupInterceptors initializes the Sailfish telemetry collector.
// It validates configuration, starts the background transmitter, identifies
// the service, installs log and print capture, and patches the default HTTP
// transport for outbound tracing.
//
// This function should be called once at application startup, before any
// HTTP servers or clients are created.
func SetupInterceptors(opts Options) {
	// 1. Validate API key
	if opts.APIKey == "" {
		fmt.Fprintf(os.Stderr, "[sfveritas] WARNING: No API key provided. Telemetry will not be sent.\n")
		return
	}

	// 2. Start UUID pre-generation pool (avoids crypto/rand syscall per UUID)
	initUUIDPool()

	// 3. Initialize configuration
	cfg := initConfig(opts)

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Initializing Go %s collector v%s\n", LibraryType, Version)
		fmt.Fprintf(os.Stderr, "[sfveritas] Endpoint: %s\n", cfg.graphqlEndpoint)
		fmt.Fprintf(os.Stderr, "[sfveritas] Service UUID: %s\n", cfg.serviceUUID)
		fmt.Fprintf(os.Stderr, "[sfveritas] Service Identifier: %s\n", cfg.serviceIdentifier)
	}

	// 3. Start background transmitter goroutine
	initTransmitter(cfg.graphqlEndpoint)

	// 4. Send IdentifyServiceDetails
	_, callerFile, callerLine, _ := runtime.Caller(1)
	sendServiceIdentification(callerFile, callerLine)

	// 5. Send DomainsToNotPassHeaderTo (if configured)
	if len(cfg.excludedDomains) > 0 {
		sendDomainsToExclude(cfg.excludedDomains)
	}

	// 6. Install slog handler (wrap a fresh text handler writing to stderr)
	// We create a fresh handler instead of wrapping slog.Default().Handler()
	// because the default handler may route through log.Default() which
	// can re-enter slog and cause infinite recursion.
	textHandler := slog.NewTextHandler(os.Stderr, nil)
	sfHandler := NewHandler(textHandler)
	slog.SetDefault(slog.New(sfHandler))

	// 7. Start print capture (stdout pipe)
	// Note: Print capture via os.Pipe redirects os.Stdout. This can be
	// disabled by setting SF_DISABLE_PRINT_CAPTURE=true.
	if os.Getenv("SF_DISABLE_PRINT_CAPTURE") != "true" {
		startPrintCapture()
	}

	// 8. Load .sailfish config file
	loadSailfishConfig()

	// 9. Patch http.DefaultTransport with tracing transport. Capture the
	// original first so the uplink can dial over it without self-instrumenting.
	origTransport := http.DefaultTransport
	http.DefaultTransport = NewTransport(origTransport)

	// 10. Start the WS uplink ("backend debugger"), gated on SF_UPLINK_ENABLE.
	startUplink(cfg, origTransport)

	// 11. Register shutdown hook (os.Signal listener)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		if cfg.debug {
			fmt.Fprintf(os.Stderr, "[sfveritas] Received shutdown signal, flushing telemetry...\n")
		}
		stopPrintCapture()
		Shutdown()
	}()

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Setup complete. Interceptors active.\n")
	}
}
