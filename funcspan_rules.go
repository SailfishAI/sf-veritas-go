package sfveritas

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

// Compiled debug-capture rules for the WS uplink ("backend debugger"), the Go
// analog of the Python SDK's function_span_capture_rules. A rule targets a
// function by exact name + file glob, sets capture config + sampling, and can
// carry preCall/postCall conditions and propagation. Rules are evaluated on the
// explicit-span hot path (startSpanInternal / Span.End).
//
// Deviation from Python: the file glob is matched at LOOKUP time
// (filepath.Match against runtime.Caller's build-time path, with a base-name
// fallback — the same strategy as getFuncspanConfig), not pre-expanded against
// the filesystem, because runtime paths don't match a globbed working tree.

type condition struct {
	key   string // preCall: arg name; postCall: field name
	op    string // normalized operator
	value string
}

type compiledRule struct {
	ruleID            string
	functionName      string // exact match
	filePattern       string // glob
	captureArgs       bool
	captureReturn     bool
	argLimitMB        int
	returnLimitMB     int
	preCall           []condition
	postCall          []condition
	logic             string // "and" | "or"
	propagateEnabled  bool
	propagateMaxDepth int
	sampleRate        float64
}

type ruleSet struct {
	byFunc map[string][]*compiledRule // functionName → candidate rules
}

// ruleCaptureConfig is the effective capture decision returned to the hot path.
type ruleCaptureConfig struct {
	ruleID            string
	captureArgs       bool
	captureReturn     bool
	argLimitMB        int
	returnLimitMB     int
	propagate         bool
	propagateMaxDepth int
	hasPostCall       bool
}

// activeRuleSet holds the current compiled rules. Lock-free reads on the hot
// path: hasActiveRules / evaluateRulePreCall do a single atomic load. The set
// is immutable once built; updates swap the whole pointer.
var activeRuleSet atomic.Pointer[ruleSet]

// --- wire shapes ---

type wireRule struct {
	RuleID string `json:"ruleId"`
	Target struct {
		FilePattern  string `json:"filePattern"`
		FunctionName string `json:"functionName"`
	} `json:"target"`
	Capture struct {
		Args          bool `json:"args"`
		ReturnValue   bool `json:"returnValue"`
		ArgLimitMb    int  `json:"argLimitMb"`
		ReturnLimitMb int  `json:"returnLimitMb"`
	} `json:"capture"`
	SampleRate float64 `json:"sampleRate"`
	Conditions *struct {
		PreCall []struct {
			Arg   string `json:"arg"`
			Op    string `json:"op"`
			Value string `json:"value"`
		} `json:"preCall"`
		PostCall []struct {
			Field string `json:"field"`
			Op    string `json:"op"`
			Value string `json:"value"`
		} `json:"postCall"`
		Logic string `json:"logic"`
	} `json:"conditions"`
	Propagate *struct {
		Enabled  bool `json:"enabled"`
		MaxDepth int  `json:"maxDepth"`
	} `json:"propagate"`
}

func hasActiveRules() bool {
	rs := activeRuleSet.Load()
	return rs != nil && len(rs.byFunc) > 0
}

func clearFuncspanRules() { activeRuleSet.Store(nil) }

// setFuncspanRules compiles raw rule JSON and atomically swaps the active set.
// Malformed rules are skipped (never abort the whole set).
func setFuncspanRules(raw []json.RawMessage) {
	byFunc := make(map[string][]*compiledRule)
	for _, r := range raw {
		var w wireRule
		if err := json.Unmarshal(r, &w); err != nil {
			continue
		}
		if w.RuleID == "" || w.Target.FunctionName == "" {
			continue
		}
		// sampleRate defaults to 1.0 (always). JSON can't distinguish unset from
		// explicit 0, and "off" is expressed by empty rules (not sampleRate=0),
		// so any value outside (0,1] is treated as 1.0.
		sr := w.SampleRate
		if sr <= 0 || sr > 1 {
			sr = 1.0
		}
		cr := &compiledRule{
			ruleID:        w.RuleID,
			functionName:  w.Target.FunctionName,
			filePattern:   w.Target.FilePattern,
			captureArgs:   w.Capture.Args,
			captureReturn: w.Capture.ReturnValue,
			argLimitMB:    w.Capture.ArgLimitMb,
			returnLimitMB: w.Capture.ReturnLimitMb,
			logic:         "and",
			sampleRate:    sr,
		}
		if w.Conditions != nil {
			if l := strings.ToLower(w.Conditions.Logic); l == "or" {
				cr.logic = "or"
			}
			for _, c := range w.Conditions.PreCall {
				cr.preCall = append(cr.preCall, condition{key: c.Arg, op: normalizeOp(c.Op), value: c.Value})
			}
			for _, c := range w.Conditions.PostCall {
				cr.postCall = append(cr.postCall, condition{key: c.Field, op: normalizeOp(c.Op), value: c.Value})
			}
		}
		if w.Propagate != nil {
			cr.propagateEnabled = w.Propagate.Enabled
			cr.propagateMaxDepth = w.Propagate.MaxDepth
		}
		byFunc[cr.functionName] = append(byFunc[cr.functionName], cr)
	}
	if len(byFunc) == 0 {
		activeRuleSet.Store(nil)
		return
	}
	activeRuleSet.Store(&ruleSet{byFunc: byFunc})
}

// evaluateRulePreCall returns the effective capture config for a span, or nil if
// no rule matches (the common path). args may be empty for arg-less spans.
func evaluateRulePreCall(filePath, functionName string, args map[string]any) *ruleCaptureConfig {
	rs := activeRuleSet.Load()
	if rs == nil {
		return nil
	}
	candidates := rs.byFunc[functionName]
	for _, rule := range candidates {
		if !fileMatches(rule.filePattern, filePath) {
			continue
		}
		if !evalConditions(rule.preCall, func(k string) any { return args[k] }, rule.logic) {
			continue
		}
		if rule.sampleRate < 1.0 && rand.Float64() > rule.sampleRate {
			continue
		}
		return &ruleCaptureConfig{
			ruleID:            rule.ruleID,
			captureArgs:       rule.captureArgs,
			captureReturn:     rule.captureReturn,
			argLimitMB:        rule.argLimitMB,
			returnLimitMB:     rule.returnLimitMB,
			propagate:         rule.propagateEnabled,
			propagateMaxDepth: rule.propagateMaxDepth,
			hasPostCall:       len(rule.postCall) > 0,
		}
	}
	return nil
}

// evaluateRulePostCall decides whether a captured span should be emitted. It is
// permissive: if the rule is gone or has no postCall conditions, emit.
func evaluateRulePostCall(ruleID string, metadata map[string]any) bool {
	rs := activeRuleSet.Load()
	if rs == nil || ruleID == "" {
		return true
	}
	for _, rules := range rs.byFunc {
		for _, rule := range rules {
			if rule.ruleID != ruleID {
				continue
			}
			if len(rule.postCall) == 0 {
				return true
			}
			return evalConditions(rule.postCall, func(k string) any { return metadata[k] }, rule.logic)
		}
	}
	return true
}

func fileMatches(pattern, filePath string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if m, _ := filepath.Match(pattern, filePath); m {
		return true
	}
	if m, _ := filepath.Match(pattern, filepath.Base(filePath)); m {
		return true
	}
	// Plain substring/suffix convenience (no glob metachars).
	if !strings.ContainsAny(pattern, "*?[") {
		return strings.HasSuffix(filePath, pattern)
	}
	return false
}

func evalConditions(conds []condition, lookup func(string) any, logic string) bool {
	if len(conds) == 0 {
		return true
	}
	or := logic == "or"
	for _, c := range conds {
		ok := evalOp(c.op, lookup(c.key), c.value)
		if or && ok {
			return true
		}
		if !or && !ok {
			return false
		}
	}
	return !or // AND with no early-false → true; OR with no early-true → false
}

func normalizeOp(op string) string {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "==", "eq", "equals":
		return "=="
	case "!=", "ne", "not_equals":
		return "!="
	case "<", "lt":
		return "<"
	case "<=", "le", "lte":
		return "<="
	case ">", "gt":
		return ">"
	case ">=", "ge", "gte":
		return ">="
	case "starts_with", "startswith", "prefix":
		return "starts_with"
	case "contains", "includes":
		return "contains"
	default:
		return strings.ToLower(strings.TrimSpace(op))
	}
}

// evalOp evaluates one operator. Never panics (returns false on bad input).
func evalOp(op string, actual any, value string) (result bool) {
	defer func() {
		if recover() != nil {
			result = false
		}
	}()
	actualStr := toStr(actual)
	switch op {
	case "==":
		if af, bf, ok := bothFloats(actual, value); ok {
			return af == bf
		}
		return actualStr == value
	case "!=":
		if af, bf, ok := bothFloats(actual, value); ok {
			return af != bf
		}
		return actualStr != value
	case "<", "<=", ">", ">=":
		af, bf, ok := bothFloats(actual, value)
		if !ok {
			return false
		}
		switch op {
		case "<":
			return af < bf
		case "<=":
			return af <= bf
		case ">":
			return af > bf
		default:
			return af >= bf
		}
	case "starts_with":
		return strings.HasPrefix(actualStr, value)
	case "contains":
		return strings.Contains(actualStr, value)
	default:
		return false
	}
}

func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func bothFloats(actual any, value string) (float64, float64, bool) {
	af, ok := toFloat(actual)
	if !ok {
		return 0, 0, false
	}
	bf, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, 0, false
	}
	return af, bf, true
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
