package sfveritas

import (
	"context"
	"testing"
)

func TestPropagationContextHelper(t *testing.T) {
	ctx := context.Background()
	if getPropagation(ctx) != nil {
		t.Fatal("no propagation initially")
	}
	ctx = setPropagation(ctx, &propagationConfig{ruleID: "r", remainingDepth: 1})
	if pc := getPropagation(ctx); pc == nil || pc.ruleID != "r" {
		t.Fatal("expected propagation present")
	}
	// remainingDepth <= 0 is treated as absent.
	ctx0 := setPropagation(context.Background(), &propagationConfig{ruleID: "r", remainingDepth: 0})
	if getPropagation(ctx0) != nil {
		t.Error("depth 0 should be treated as absent")
	}
}

// End-to-end: a propagating rule on a parent function makes child spans inherit
// the capture config even though the child function has no rule of its own.
func TestRulePropagationThroughSpans(t *testing.T) {
	prev := globalConfig
	globalConfig = &config{autoCaptureChildren: true} // production default
	defer func() { globalConfig = prev }()
	clearFuncspanRules()
	defer clearFuncspanRules()

	setFuncspanRules(rawRules(t, `{
		"ruleId":"r1",
		"target":{"filePattern":"*","functionName":"Parent"},
		"capture":{"args":true,"returnValue":true},
		"sampleRate":1.0,
		"propagate":{"enabled":true,"maxDepth":2}
	}`))

	parent := StartSpan(context.Background(), "Parent")
	if parent.ruleID != "r1" {
		t.Fatalf("parent should match rule, got %q", parent.ruleID)
	}
	child := StartSpan(parent.Context(), "Child") // no rule of its own
	if child.ruleID != "r1" {
		t.Errorf("child should inherit via propagation, got %q", child.ruleID)
	}
	gc := StartSpan(child.Context(), "GC")
	if gc.ruleID != "r1" {
		t.Errorf("grandchild (within maxDepth 2) should inherit, got %q", gc.ruleID)
	}
	ggc := StartSpan(gc.Context(), "GGC")
	if ggc.ruleID != "" {
		t.Errorf("great-grandchild (beyond maxDepth) should NOT inherit, got %q", ggc.ruleID)
	}

	// A child that breaks the context chain does not inherit.
	orphan := StartSpan(context.Background(), "Child")
	if orphan.ruleID != "" {
		t.Errorf("orphan (broken context chain) should not inherit, got %q", orphan.ruleID)
	}
}
