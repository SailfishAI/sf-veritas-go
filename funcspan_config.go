package sfveritas

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// sailfishFileConfig represents the parsed .sailfish configuration file.
type sailfishFileConfig struct {
	Default   *funcspanFunctionConfig           `json:"default" yaml:"default" toml:"default"`
	Files     map[string]funcspanFileConfig     `json:"files" yaml:"files" toml:"files"`
	Functions map[string]funcspanFunctionConfig `json:"functions" yaml:"functions" toml:"functions"`
}

// funcspanFileConfig holds per-file function span configuration.
type funcspanFileConfig struct {
	CaptureArguments   *bool    `json:"capture_arguments" yaml:"capture_arguments" toml:"capture_arguments"`
	CaptureReturnValue *bool    `json:"capture_return_value" yaml:"capture_return_value" toml:"capture_return_value"`
	ArgLimitMB         *int     `json:"arg_limit_mb" yaml:"arg_limit_mb" toml:"arg_limit_mb"`
	ReturnLimitMB      *int     `json:"return_limit_mb" yaml:"return_limit_mb" toml:"return_limit_mb"`
	SampleRate         *float64 `json:"sample_rate" yaml:"sample_rate" toml:"sample_rate"`
	ParseJSONStrings   *bool    `json:"parse_json_strings" yaml:"parse_json_strings" toml:"parse_json_strings"`
	CaptureChildren    *bool    `json:"autocapture_all_children" yaml:"autocapture_all_children" toml:"autocapture_all_children"`
	EnableSampling     *bool    `json:"enable_sampling" yaml:"enable_sampling" toml:"enable_sampling"`
	CaptureSfVeritas   *bool    `json:"capture_sf_veritas" yaml:"capture_sf_veritas" toml:"capture_sf_veritas"`
}

// funcspanFunctionConfig holds per-function span configuration.
type funcspanFunctionConfig struct {
	CaptureArguments   *bool    `json:"capture_arguments" yaml:"capture_arguments" toml:"capture_arguments"`
	CaptureReturnValue *bool    `json:"capture_return_value" yaml:"capture_return_value" toml:"capture_return_value"`
	ArgLimitMB         *int     `json:"arg_limit_mb" yaml:"arg_limit_mb" toml:"arg_limit_mb"`
	ReturnLimitMB      *int     `json:"return_limit_mb" yaml:"return_limit_mb" toml:"return_limit_mb"`
	SampleRate         *float64 `json:"sample_rate" yaml:"sample_rate" toml:"sample_rate"`
	ParseJSONStrings   *bool    `json:"parse_json_strings" yaml:"parse_json_strings" toml:"parse_json_strings"`
	CaptureChildren    *bool    `json:"autocapture_all_children" yaml:"autocapture_all_children" toml:"autocapture_all_children"`
	EnableSampling     *bool    `json:"enable_sampling" yaml:"enable_sampling" toml:"enable_sampling"`
	CaptureSfVeritas   *bool    `json:"capture_sf_veritas" yaml:"capture_sf_veritas" toml:"capture_sf_veritas"`
}

var sailfishConfig *sailfishFileConfig

// parseSailfishData attempts to parse data as JSON, then TOML, then YAML.
func parseSailfishData(data []byte) (*sailfishFileConfig, error) {
	// Try JSON first
	var cfg sailfishFileConfig
	if err := json.Unmarshal(data, &cfg); err == nil {
		return &cfg, nil
	}

	// Try TOML
	cfg = sailfishFileConfig{}
	if _, err := toml.Decode(string(data), &cfg); err == nil {
		return &cfg, nil
	}

	// Try YAML
	cfg = sailfishFileConfig{}
	if err := yaml.Unmarshal(data, &cfg); err == nil {
		return &cfg, nil
	}

	return nil, fmt.Errorf("failed to parse as JSON, TOML, or YAML")
}

// loadSailfishConfig walks from cwd to root looking for a .sailfish config file.
// Supports JSON, TOML, and YAML formats (matching Python reference implementation).
func loadSailfishConfig() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}

	for {
		sfPath := filepath.Join(dir, ".sailfish")

		// Try .sailfish as a file (JSON / TOML / YAML auto-detect)
		if data, err := os.ReadFile(sfPath); err == nil {
			if cfg, err := parseSailfishData(data); err == nil {
				sailfishConfig = cfg
				if c := getConfig(); c != nil && c.debug {
					fmt.Fprintf(os.Stderr, "[sfveritas] Loaded .sailfish config from %s\n", sfPath)
				}
				return
			}
		}

		// Try .sailfish/ directory with format-specific config files
		for _, name := range []string{"config.json", "config.toml", "config.yaml", "config.yml"} {
			configPath := filepath.Join(dir, ".sailfish", name)
			if data, err := os.ReadFile(configPath); err == nil {
				if cfg, err := parseSailfishData(data); err == nil {
					sailfishConfig = cfg
					if c := getConfig(); c != nil && c.debug {
						fmt.Fprintf(os.Stderr, "[sfveritas] Loaded .sailfish config from %s\n", configPath)
					}
					return
				}
			}
		}

		// Walk up
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

// getFuncspanConfig resolves the effective funcspan configuration for a given file/function.
// Returns nil if no config matches.
func getFuncspanConfig(filePath, functionName string) *funcspanFunctionConfig {
	if sailfishConfig == nil {
		return nil
	}

	// Check function-level config first (most specific)
	if fc, ok := sailfishConfig.Functions[functionName]; ok {
		return &fc
	}

	// Check file-level config with glob matching
	for pattern, fc := range sailfishConfig.Files {
		matched := false
		if strings.Contains(pattern, "*") || strings.Contains(pattern, "?") {
			matched, _ = filepath.Match(pattern, filePath)
			if !matched {
				// Also try matching just the filename
				matched, _ = filepath.Match(pattern, filepath.Base(filePath))
			}
		} else {
			matched = strings.HasSuffix(filePath, pattern)
		}
		if matched {
			return &funcspanFunctionConfig{
				CaptureArguments:   fc.CaptureArguments,
				CaptureReturnValue: fc.CaptureReturnValue,
				ArgLimitMB:         fc.ArgLimitMB,
				ReturnLimitMB:      fc.ReturnLimitMB,
				SampleRate:         fc.SampleRate,
				ParseJSONStrings:   fc.ParseJSONStrings,
				CaptureChildren:    fc.CaptureChildren,
				EnableSampling:     fc.EnableSampling,
				CaptureSfVeritas:   fc.CaptureSfVeritas,
			}
		}
	}

	// Fall back to default section if present
	if sailfishConfig.Default != nil {
		return sailfishConfig.Default
	}

	return nil
}
