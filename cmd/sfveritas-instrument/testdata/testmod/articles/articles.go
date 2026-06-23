package articles

import (
	"context"

	sfveritas "github.com/SailfishAI/sf-veritas-go"
)

// Opt this package into auto-instrumentation by referencing sfveritas.
var _ = sfveritas.LibraryType

// Make has a context.Context parameter and UNNAMED return values. This exercises
// the ctx-param span path (StartSpanWithArgs + ctx reassignment) and named-return
// synthesis for return-value capture.
func Make(ctx context.Context, name string) (string, error) {
	greeting := "hi " + name
	decorated := greeting + "!"
	return decorated, nil
}

// Tally has NAMED return values already.
func Tally(ctx context.Context, xs []int) (sum int, err error) {
	for _, x := range xs {
		sum += x
	}
	return sum, nil
}
