// Package veritasgin provides a Gin middleware that reports panics (with full
// stack traces) and handler errors to Sailfish.
//
// It exists because the generic sfveritas.Middleware wraps the Gin engine from
// the OUTSIDE, while gin.Default()'s built-in gin.Recovery() recovers panics
// from INSIDE the engine — so the outer middleware never sees them. This Gin
// middleware runs inside the engine, captures the panic (and its stack) before
// Gin's Recovery swallows it, then re-panics so Gin's Recovery still produces
// the 500 response.
//
// Usage — register it right after creating the engine, BEFORE your routes:
//
//	import (
//	    "github.com/gin-gonic/gin"
//	    sfveritas "github.com/SailfishAI/sf-veritas-go"
//	    veritasgin "github.com/SailfishAI/sf-veritas-go/gin"
//	)
//
//	func main() {
//	    sfveritas.SetupInterceptors(sfveritas.Options{APIKey: "..."})
//	    defer sfveritas.Shutdown()
//
//	    r := gin.Default()             // includes gin.Recovery()
//	    r.Use(veritasgin.Middleware()) // <-- add this, before routes
//	    // ... register routes ...
//	    http.ListenAndServe(":8080", sfveritas.Middleware(r))
//	}
package veritasgin

import (
	"fmt"

	"github.com/gin-gonic/gin"

	sfveritas "github.com/SailfishAI/sf-veritas-go"
)

// Middleware returns a Gin middleware that reports exceptions to Sailfish:
//
//   - Panics are captured WITH their stack trace and re-panicked, so any outer
//     recovery (gin.Recovery / sfveritas.Middleware) still produces the response.
//   - Errors a handler attaches via c.Error(err) are reported.
//   - A handled response with status >= 500 (and no explicit error) is reported
//     as a (stack-less) exception so server errors are never silently dropped.
//
// Register it before your routes. For the richest capture on a specific
// handled error, call sfveritas.TransmitError(c.Request.Context(), err) at the
// error site (that captures the stack where the error actually occurred).
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Capture panics with their stack, then re-panic so the app's existing
		// recovery (gin.Recovery, or sfveritas.Middleware) handles the response.
		defer sfveritas.RecoverAndTransmit(c.Request.Context(), true)

		c.Next()

		// Reached only on the non-panic path.
		ctx := c.Request.Context()
		if len(c.Errors) > 0 {
			for _, e := range c.Errors {
				if e != nil && e.Err != nil {
					sfveritas.TransmitError(ctx, e.Err)
				}
			}
			return
		}
		if c.Writer.Status() >= 500 {
			sfveritas.TransmitError(ctx, fmt.Errorf("HTTP %d on %s %s", c.Writer.Status(), c.Request.Method, c.FullPath()))
		}
	}
}
