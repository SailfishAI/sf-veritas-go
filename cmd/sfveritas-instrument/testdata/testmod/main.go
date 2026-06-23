package main

import (
	"context"
	"fmt"

	sfveritas "github.com/SailfishAI/sf-veritas-go"

	"example.com/testmod/articles"
	"example.com/testmod/common"
)

// main imports sfveritas directly so its compiler importcfg carries the
// sfveritas packagefile line — which the instrumenter harvests so the other
// (sfveritas-free) packages can have the import injected.
func main() {
	sfveritas.SetupInterceptors(sfveritas.Options{APIKey: "test"})
	defer sfveritas.Shutdown()

	g, _ := articles.Make(context.Background(), "world")
	s, _ := articles.Tally(context.Background(), []int{1, 2, 3})
	fmt.Println(g, common.Compute(5), common.Describe("x", 3), s)
}
