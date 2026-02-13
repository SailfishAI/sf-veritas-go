package sfveritas

import (
	"net/http"
	"sync"
	"testing"
)

// --- isDomainExcluded ---

func TestIsDomainExcluded_NilConfig(t *testing.T) {
	saved := globalConfig
	globalConfig = nil
	defer func() { globalConfig = saved }()

	tr := &Transport{Base: http.DefaultTransport}
	if tr.isDomainExcluded("example.com") {
		t.Error("expected false when config is nil")
	}
}

func TestIsDomainExcluded_EmptyLists(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{
		excludedDomainsExact:  map[string]struct{}{},
		excludedDomainsSuffix: nil,
	}
	defer func() { globalConfig = saved }()

	tr := &Transport{Base: http.DefaultTransport}
	if tr.isDomainExcluded("example.com") {
		t.Error("expected false with empty exclusion lists")
	}
}

func TestIsDomainExcluded_ExactMatch(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{
		excludedDomainsExact: map[string]struct{}{
			"api.sailfishqa.com": {},
			"example.com":       {},
		},
		excludedDomainsSuffix: nil,
	}
	defer func() { globalConfig = saved }()

	tr := &Transport{Base: http.DefaultTransport}
	if !tr.isDomainExcluded("api.sailfishqa.com") {
		t.Error("expected api.sailfishqa.com to be excluded")
	}
	if !tr.isDomainExcluded("example.com") {
		t.Error("expected example.com to be excluded")
	}
	if tr.isDomainExcluded("other.com") {
		t.Error("expected other.com to NOT be excluded")
	}
}

func TestIsDomainExcluded_SuffixMatch(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{
		excludedDomainsExact:  map[string]struct{}{},
		excludedDomainsSuffix: []string{".googleapis.com", ".internal.net"},
	}
	defer func() { globalConfig = saved }()

	tr := &Transport{Base: http.DefaultTransport}
	if !tr.isDomainExcluded("storage.googleapis.com") {
		t.Error("expected storage.googleapis.com to be excluded via suffix")
	}
	if !tr.isDomainExcluded("service.internal.net") {
		t.Error("expected service.internal.net to be excluded via suffix")
	}
	if tr.isDomainExcluded("example.com") {
		t.Error("expected example.com to NOT match suffix")
	}
}

func TestIsDomainExcluded_StripsPort(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{
		excludedDomainsExact:  map[string]struct{}{"example.com": {}},
		excludedDomainsSuffix: nil,
	}
	defer func() { globalConfig = saved }()

	tr := &Transport{Base: http.DefaultTransport}
	if !tr.isDomainExcluded("example.com:8080") {
		t.Error("expected domain with port to match after stripping port")
	}
}

func TestIsDomainExcluded_CaseInsensitive(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{
		excludedDomainsExact:  map[string]struct{}{"example.com": {}},
		excludedDomainsSuffix: nil,
	}
	defer func() { globalConfig = saved }()

	tr := &Transport{Base: http.DefaultTransport}
	if !tr.isDomainExcluded("Example.COM") {
		t.Error("expected case-insensitive match")
	}
}

// --- NewTransport ---

func TestNewTransport_NilBase(t *testing.T) {
	tr := NewTransport(nil)
	if tr.Base == nil {
		t.Error("expected non-nil Base when nil is passed")
	}
}

func TestNewTransport_WithBase(t *testing.T) {
	base := &http.Transport{}
	tr := NewTransport(base)
	if tr.Base != base {
		t.Error("expected Base to be the provided transport")
	}
}

// --- mergeVariables ---

func TestMergeVariables_NilConfig(t *testing.T) {
	saved := globalConfig
	globalConfig = nil
	defer func() { globalConfig = saved }()

	additional := map[string]interface{}{"key": "val"}
	got := mergeVariables(additional)
	if got["key"] != "val" {
		t.Error("expected additional variables to pass through when config is nil")
	}
}

func TestMergeVariables_WithConfig(t *testing.T) {
	saved := globalConfig
	configOnce = sync.Once{}
	globalConfig = &config{
		apiKey:      "test-api-key",
		serviceUUID: "test-uuid",
	}
	defer func() {
		globalConfig = saved
		configOnce = sync.Once{}
	}()

	additional := map[string]interface{}{"sessionId": "sess-123"}
	got := mergeVariables(additional)
	if got["apiKey"] != "test-api-key" {
		t.Errorf("expected apiKey from config, got %v", got["apiKey"])
	}
	if got["serviceUuid"] != "test-uuid" {
		t.Errorf("expected serviceUuid from config, got %v", got["serviceUuid"])
	}
	if got["sessionId"] != "sess-123" {
		t.Errorf("expected sessionId from additional, got %v", got["sessionId"])
	}
}

func TestMergeVariables_OverridesDefaults(t *testing.T) {
	saved := globalConfig
	configOnce = sync.Once{}
	globalConfig = &config{
		apiKey:      "default-key",
		serviceUUID: "default-uuid",
	}
	defer func() {
		globalConfig = saved
		configOnce = sync.Once{}
	}()

	// Additional vars should override defaults
	additional := map[string]interface{}{"apiKey": "override-key"}
	got := mergeVariables(additional)
	if got["apiKey"] != "override-key" {
		t.Errorf("expected override-key, got %v", got["apiKey"])
	}
}
