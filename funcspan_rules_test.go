package sfveritas

import (
	"encoding/json"
	"testing"
)

func rawRules(t *testing.T, rules ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(rules))
	for i, r := range rules {
		out[i] = json.RawMessage(r)
	}
	return out
}

func TestEvalOp(t *testing.T) {
	cases := []struct {
		op   string
		a    any
		v    string
		want bool
	}{
		{"==", "abc", "abc", true},
		{"==", "abc", "abd", false},
		{"==", float64(5), "5", true},
		{"==", 5, "5.0", true},
		{"!=", "x", "y", true},
		{"<", 3, "5", true},
		{"<", 5, "3", false},
		{"<=", 5, "5", true},
		{">", 7, "2", true},
		{">=", 2, "7", false},
		{">", "notnum", "2", false}, // non-coercible → false, no panic
		{"starts_with", "checkout-api", "checkout", true},
		{"starts_with", "api", "checkout", false},
		{"contains", "hello world", "o w", true},
		{"contains", "hello", "zzz", false},
	}
	for _, c := range cases {
		if got := evalOp(normalizeOp(c.op), c.a, c.v); got != c.want {
			t.Errorf("evalOp(%q, %v, %q) = %v, want %v", c.op, c.a, c.v, got, c.want)
		}
	}
	// enum-form operators normalize.
	if !evalOp(normalizeOp("GTE"), 5, "5") {
		t.Error("expected GTE to normalize to >=")
	}
}

func TestEvalConditions(t *testing.T) {
	args := map[string]any{"tenant": "acme", "count": float64(10)}
	lk := func(k string) any { return args[k] }
	and := []condition{{"tenant", "==", "acme"}, {"count", ">", "5"}}
	if !evalConditions(and, lk, "and") {
		t.Error("AND should pass")
	}
	and2 := []condition{{"tenant", "==", "acme"}, {"count", ">", "50"}}
	if evalConditions(and2, lk, "and") {
		t.Error("AND should fail (count not >50)")
	}
	if !evalConditions(and2, lk, "or") {
		t.Error("OR should pass (tenant matches)")
	}
	if !evalConditions(nil, lk, "and") {
		t.Error("empty conditions should match")
	}
	// missing arg → condition false
	if evalConditions([]condition{{"missing", "==", "x"}}, lk, "and") {
		t.Error("missing arg should not match")
	}
}

func TestPreCallMatchAndCapture(t *testing.T) {
	clearFuncspanRules()
	defer clearFuncspanRules()
	setFuncspanRules(rawRules(t, `{
		"ruleId":"r1",
		"target":{"filePattern":"*.go","functionName":"ProcessOrder"},
		"capture":{"args":true,"returnValue":true,"argLimitMb":2,"returnLimitMb":3},
		"sampleRate":1.0,
		"propagate":{"enabled":true,"maxDepth":4}
	}`))
	if !hasActiveRules() {
		t.Fatal("expected active rules")
	}
	rc := evaluateRulePreCall("/app/orders.go", "ProcessOrder", nil)
	if rc == nil {
		t.Fatal("expected a match")
	}
	if rc.ruleID != "r1" || !rc.captureArgs || !rc.captureReturn || rc.argLimitMB != 2 || rc.returnLimitMB != 3 {
		t.Errorf("unexpected capture config: %+v", rc)
	}
	if !rc.propagate || rc.propagateMaxDepth != 4 {
		t.Errorf("expected propagation maxDepth 4, got %+v", rc)
	}
	// Non-matching function name → nil.
	if evaluateRulePreCall("/app/orders.go", "OtherFn", nil) != nil {
		t.Error("expected no match for different function")
	}
	// Non-matching file glob → nil.
	clearFuncspanRules()
	setFuncspanRules(rawRules(t, `{"ruleId":"r2","target":{"filePattern":"billing/*.go","functionName":"ProcessOrder"},"capture":{"args":true,"returnValue":true},"sampleRate":1.0}`))
	if evaluateRulePreCall("/app/orders.go", "ProcessOrder", nil) != nil {
		t.Error("expected no match for non-matching file pattern")
	}
}

func TestPreCallConditions(t *testing.T) {
	clearFuncspanRules()
	defer clearFuncspanRules()
	setFuncspanRules(rawRules(t, `{
		"ruleId":"r1","target":{"filePattern":"*","functionName":"Handle"},
		"capture":{"args":true,"returnValue":true},"sampleRate":1.0,
		"conditions":{"preCall":[{"arg":"tenant","op":"==","value":"acme"}],"logic":"and"}
	}`))
	if evaluateRulePreCall("/x.go", "Handle", map[string]any{"tenant": "other"}) != nil {
		t.Error("preCall should reject tenant=other")
	}
	if evaluateRulePreCall("/x.go", "Handle", map[string]any{"tenant": "acme"}) == nil {
		t.Error("preCall should accept tenant=acme")
	}
}

func TestPostCallGate(t *testing.T) {
	clearFuncspanRules()
	defer clearFuncspanRules()
	setFuncspanRules(rawRules(t, `{
		"ruleId":"r1","target":{"filePattern":"*","functionName":"Q"},
		"capture":{"args":false,"returnValue":true},"sampleRate":1.0,
		"conditions":{"postCall":[{"field":"durationNs","op":">","value":"1000"}],"logic":"and"}
	}`))
	// gone rule id → permissive true
	if !evaluateRulePostCall("missing", map[string]any{"durationNs": int64(5)}) {
		t.Error("gone rule should be permissive")
	}
	// condition holds → emit
	if !evaluateRulePostCall("r1", map[string]any{"durationNs": int64(2000)}) {
		t.Error("postCall should pass (2000>1000)")
	}
	// condition fails → drop
	if evaluateRulePostCall("r1", map[string]any{"durationNs": int64(10)}) {
		t.Error("postCall should fail (10>1000 is false)")
	}
}

func TestEmptyRulesClears(t *testing.T) {
	setFuncspanRules(rawRules(t, `{"ruleId":"r","target":{"filePattern":"*","functionName":"F"},"capture":{"args":true,"returnValue":true},"sampleRate":1.0}`))
	if !hasActiveRules() {
		t.Fatal("expected rules")
	}
	setFuncspanRules(nil)
	if hasActiveRules() {
		t.Error("empty rules should clear")
	}
}
