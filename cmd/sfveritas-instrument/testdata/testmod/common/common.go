package common

import sfveritas "github.com/SailfishAI/sf-veritas-go"

// _ references sfveritas so the package's importcfg resolves it, opting this
// package into auto-instrumentation (the instrumenter only injects spans where
// the import is already resolvable).
var _ = sfveritas.LibraryType

// Compute has NO context parameter and declares locals AFTER the top of the
// function. This exercises the no-ctx span path (StartSpanNoCtx, so no `context`
// import is injected) and the locals-scoping fix (the instrumentation defer must
// not reference these later-declared locals).
func Compute(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	result := total * 2
	return result
}

// Describe is another context-less, multi-statement function.
func Describe(label string, n int) string {
	prefix := "[" + label + "]"
	out := prefix
	for i := 0; i < n; i++ {
		out += "."
	}
	return out
}
