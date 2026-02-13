package sfveritas

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- parseFuncSpanOverride ---

func TestParseFuncSpanOverride_Valid(t *testing.T) {
	// Format: args-ret-argMB-retMB-children-rate-sampling-sfVeritas-parseJson
	oc := parseFuncSpanOverride("1-1-5-10-1-0.5-1-1-1")
	if oc == nil {
		t.Fatal("expected non-nil override config")
	}
	if !oc.captureArgs {
		t.Error("expected captureArgs=true")
	}
	if !oc.captureReturn {
		t.Error("expected captureReturn=true")
	}
	if oc.argLimitMB != 5 {
		t.Errorf("expected argLimitMB=5, got %d", oc.argLimitMB)
	}
	if oc.retLimitMB != 10 {
		t.Errorf("expected retLimitMB=10, got %d", oc.retLimitMB)
	}
	if !oc.captureChildren {
		t.Error("expected captureChildren=true")
	}
	if oc.sampleRate != 0.5 {
		t.Errorf("expected sampleRate=0.5, got %f", oc.sampleRate)
	}
	if !oc.samplingEnabled {
		t.Error("expected samplingEnabled=true")
	}
	if !oc.sfVeritasEnabled {
		t.Error("expected sfVeritasEnabled=true")
	}
	if !oc.parseJSON {
		t.Error("expected parseJSON=true")
	}
}

func TestParseFuncSpanOverride_AllDisabled(t *testing.T) {
	oc := parseFuncSpanOverride("0-0-1-1-0-1.0-0-0-0")
	if oc == nil {
		t.Fatal("expected non-nil override config")
	}
	if oc.captureArgs {
		t.Error("expected captureArgs=false")
	}
	if oc.captureReturn {
		t.Error("expected captureReturn=false")
	}
	if oc.captureChildren {
		t.Error("expected captureChildren=false")
	}
	if oc.samplingEnabled {
		t.Error("expected samplingEnabled=false")
	}
	if oc.sfVeritasEnabled {
		t.Error("expected sfVeritasEnabled=false")
	}
	if oc.parseJSON {
		t.Error("expected parseJSON=false")
	}
}

func TestParseFuncSpanOverride_TooFewFields(t *testing.T) {
	oc := parseFuncSpanOverride("1-1-5")
	if oc != nil {
		t.Error("expected nil for too few fields")
	}
}

func TestParseFuncSpanOverride_ExactlyEightFields(t *testing.T) {
	oc := parseFuncSpanOverride("1-1-5-10-1-0.5-1-1")
	if oc != nil {
		t.Error("expected nil for exactly 8 fields (needs 9)")
	}
}

func TestParseFuncSpanOverride_EmptyString(t *testing.T) {
	oc := parseFuncSpanOverride("")
	if oc != nil {
		t.Error("expected nil for empty string")
	}
}

// --- typeNameFast ---

func TestTypeNameFast(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected string
	}{
		{"hello", "string"},
		{42, "int"},
		{int32(42), "int32"},
		{int64(42), "int64"},
		{3.14, "float64"},
		{float32(3.14), "float32"},
		{true, "bool"},
		{[]byte{1, 2, 3}, "[]uint8"},
		{map[string]interface{}{"key": "val"}, "object"},
	}
	for _, tt := range tests {
		got := typeNameFast(tt.input)
		if got != tt.expected {
			t.Errorf("typeNameFast(%v) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestTypeNameFast_CustomType(t *testing.T) {
	type myStruct struct{ X int }
	got := typeNameFast(myStruct{X: 1})
	if got != "sfveritas.myStruct" {
		t.Errorf("expected sfveritas.myStruct, got %q", got)
	}
}

// --- formatReturnValue ---

func TestFormatReturnValue_Nil(t *testing.T) {
	got := formatReturnValue(nil, 1024*1024)
	if got != nullReturnJSON {
		t.Errorf("expected nullReturnJSON, got %s", got)
	}
}

func TestFormatReturnValue_String(t *testing.T) {
	got := formatReturnValue("hello", 1024*1024)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("failed to parse return JSON: %v", err)
	}
	if parsed["type"] != "string" {
		t.Errorf("expected type=string, got %v", parsed["type"])
	}
	if parsed["has_value"] != true {
		t.Errorf("expected has_value=true, got %v", parsed["has_value"])
	}
	if parsed["value"] != "hello" {
		t.Errorf("expected value=hello, got %v", parsed["value"])
	}
}

func TestFormatReturnValue_Int(t *testing.T) {
	got := formatReturnValue(42, 1024*1024)
	var parsed map[string]interface{}
	json.Unmarshal([]byte(got), &parsed)
	if parsed["type"] != "int" {
		t.Errorf("expected type=int, got %v", parsed["type"])
	}
	if parsed["value"] != float64(42) { // JSON numbers are float64
		t.Errorf("expected value=42, got %v", parsed["value"])
	}
}

func TestFormatReturnValue_Map(t *testing.T) {
	got := formatReturnValue(map[string]interface{}{"key": "val"}, 1024*1024)
	var parsed map[string]interface{}
	json.Unmarshal([]byte(got), &parsed)
	if parsed["type"] != "object" {
		t.Errorf("expected type=object, got %v", parsed["type"])
	}
}

func TestFormatReturnValue_Truncation(t *testing.T) {
	// Create a value larger than the limit
	bigStr := strings.Repeat("x", 2000)
	got := formatReturnValue(bigStr, 100)
	var parsed map[string]interface{}
	json.Unmarshal([]byte(got), &parsed)
	if parsed["_truncated"] != true {
		t.Error("expected _truncated=true for oversized value")
	}
}

// --- parseJSONInArgs ---

func TestParseJSONInArgs_ParsesJSONStrings(t *testing.T) {
	input := `{"data":"{\"name\":\"John\",\"age\":30}","count":"5"}`
	got := parseJSONInArgs(input)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	// "data" should be parsed from string to object
	data, ok := parsed["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be parsed as object, got %T", parsed["data"])
	}
	if data["name"] != "John" {
		t.Errorf("expected data.name=John, got %v", data["name"])
	}

	// "count" should remain a string (not valid JSON object/array)
	if parsed["count"] != "5" {
		t.Errorf("expected count to remain string '5', got %v", parsed["count"])
	}
}

func TestParseJSONInArgs_ParsesJSONArrays(t *testing.T) {
	input := `{"items":"[1,2,3]"}`
	got := parseJSONInArgs(input)

	var parsed map[string]interface{}
	json.Unmarshal([]byte(got), &parsed)

	items, ok := parsed["items"].([]interface{})
	if !ok {
		t.Fatalf("expected items to be parsed as array, got %T", parsed["items"])
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}
}

func TestParseJSONInArgs_LeavesNonJSONAlone(t *testing.T) {
	input := `{"name":"hello","number":42}`
	got := parseJSONInArgs(input)
	if got != input {
		t.Errorf("expected no modification for non-JSON string values, got %s", got)
	}
}

func TestParseJSONInArgs_InvalidJSON(t *testing.T) {
	input := "not json at all"
	got := parseJSONInArgs(input)
	if got != input {
		t.Error("expected invalid JSON to be returned as-is")
	}
}

func TestParseJSONInArgs_EmptyObject(t *testing.T) {
	got := parseJSONInArgs("{}")
	if got != "{}" {
		t.Errorf("expected empty object to pass through, got %s", got)
	}
}

// --- marshalWithLimit ---

func TestMarshalWithLimit_Nil(t *testing.T) {
	got := marshalWithLimit(nil, 1024)
	if got != "{}" {
		t.Errorf("expected '{}' for nil, got %s", got)
	}
}

func TestMarshalWithLimit_SmallValue(t *testing.T) {
	got := marshalWithLimit(map[string]string{"key": "val"}, 1024*1024)
	if !strings.Contains(got, "key") {
		t.Errorf("expected marshaled value to contain 'key', got %s", got)
	}
}

func TestMarshalWithLimit_Truncation(t *testing.T) {
	big := strings.Repeat("x", 2000)
	got := marshalWithLimit(big, 100)
	var parsed map[string]interface{}
	json.Unmarshal([]byte(got), &parsed)
	if parsed["_truncated"] != true {
		t.Error("expected truncation marker for oversized value")
	}
}

func TestMarshalWithLimit_ZeroLimit(t *testing.T) {
	// Zero limit should not truncate (special case: 0 means "default" behavior,
	// truncation is only when limitBytes > 0 and len exceeds it)
	got := marshalWithLimit("hello", 0)
	if !strings.Contains(got, "hello") {
		t.Errorf("expected value to pass through with 0 limit, got %s", got)
	}
}
