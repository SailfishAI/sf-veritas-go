# Sailfish Veritas — Go SDK

`sf-veritas-go` is the [Sailfish](https://sailfish.ai) telemetry collector for Go
applications. With a single call at startup it captures structured logs, print
statements, exceptions/panics, inbound & outbound HTTP telemetry, and function
execution spans, and sends them to the Sailfish backend over a non-blocking,
batched transport.

> **Full documentation:** https://docs.sailfish.ai/enterprise/integrate-with-your-code/backend/go

## Install

```bash
go get github.com/SailfishAI/sf-veritas-go@latest
```

Requires Go 1.22+.

## Quick start

```go
package main

import (
	"net/http"

	sfveritas "github.com/SailfishAI/sf-veritas-go"
)

func main() {
	sfveritas.SetupInterceptors(sfveritas.Options{
		APIKey:            "your-api-key", // from the Sailfish dashboard → Settings → Configuration
		ServiceIdentifier: "acme-corp/go-api/cmd/server/main.go",
		ServiceVersion:    "1.0.0",
	})
	defer sfveritas.Shutdown()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/users", handleUsers)

	// Wrap your handler for inbound HTTP tracing. gin/echo engines are http.Handlers too.
	http.ListenAndServe(":8080", sfveritas.Middleware(mux))
}
```

Once `SetupInterceptors` is called you get, with no extra code: `slog` + print
capture, inbound HTTP tracing (via `Middleware`), outbound HTTP tracing (the
default transport is patched), panic recovery, and `sfveritas.TransmitError(ctx, err)`
for caught errors.

## Function spans

- **Manual (guaranteed):** wrap a call with `sfveritas.TraceFunc` /
  `TraceFuncWithArgs`, or `StartSpan` / `StartSpanWithArgs` + `span.End(...)`.
- **Automatic (best-effort):** build with the `-toolexec` instrumenter:
  ```bash
  go install github.com/SailfishAI/sf-veritas-go/cmd/sfveritas-instrument@latest
  go build -toolexec="sfveritas-instrument" ./...
  ```
  It instruments functions in packages that import `sfveritas`; third-party and
  CGO packages are skipped. See the docs for scope and limitations.

## Capturing exceptions

`sfveritas.TransmitError(ctx, err)` reports a caught error (with stack) from anywhere.

Panics are only auto-captured where they actually reach the SDK. With **Gin**,
`gin.Default()`'s built-in `Recovery()` swallows handler panics *before* the outer
`sfveritas.Middleware` can see them — so register the Gin middleware, which captures the
panic (and its stack) and re-panics so Gin still returns the 500:

```go
import veritasgin "github.com/SailfishAI/sf-veritas-go/gin"

r := gin.Default()
r.Use(veritasgin.Middleware()) // register BEFORE your routes
```

It also reports `c.Error(err)` errors and handled 5xx responses. For the precise stack at
a specific error site, call `sfveritas.TransmitError(c.Request.Context(), err)` there.

## Configuration

`Options` covers the common cases; behavior is further tunable via environment
variables (`SAILFISH_GRAPHQL_ENDPOINT`, `SF_FUNCSPAN_*`, `SF_NETWORKHOP_*`,
`SF_NBPOST_*`, …) and an optional `.sailfish` config file. See the
[full guide](https://docs.sailfish.ai/enterprise/integrate-with-your-code/backend/go)
for the complete reference.

## License

Business Source License 1.1 (BUSL-1.1) — see [LICENSE](./LICENSE). Converts to
Apache License 2.0 four years after each version's release.
