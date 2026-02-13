package sfveritas

import (
	"os"
	"path/filepath"
	"testing"
)

// --- parseSailfishData ---

func TestParseSailfishData_JSON(t *testing.T) {
	data := []byte(`{
		"default": {"capture_arguments": true, "sample_rate": 0.5},
		"files": {"*.go": {"capture_return_value": false}},
		"functions": {"MyFunc": {"arg_limit_mb": 2}}
	}`)

	cfg, err := parseSailfishData(data)
	if err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if cfg.Default == nil {
		t.Fatal("expected default section")
	}
	if cfg.Default.CaptureArguments == nil || !*cfg.Default.CaptureArguments {
		t.Error("expected default.capture_arguments=true")
	}
	if cfg.Default.SampleRate == nil || *cfg.Default.SampleRate != 0.5 {
		t.Error("expected default.sample_rate=0.5")
	}
	if len(cfg.Files) != 1 {
		t.Errorf("expected 1 file config, got %d", len(cfg.Files))
	}
	if len(cfg.Functions) != 1 {
		t.Errorf("expected 1 function config, got %d", len(cfg.Functions))
	}
}

func TestParseSailfishData_YAML(t *testing.T) {
	data := []byte(`
default:
  capture_arguments: true
  sample_rate: 0.75
files:
  "*.go":
    capture_return_value: false
functions:
  MyFunc:
    arg_limit_mb: 3
`)
	cfg, err := parseSailfishData(data)
	if err != nil {
		t.Fatalf("failed to parse YAML: %v", err)
	}
	if cfg.Default == nil {
		t.Fatal("expected default section")
	}
	if cfg.Default.CaptureArguments == nil || !*cfg.Default.CaptureArguments {
		t.Error("expected default.capture_arguments=true")
	}
	if cfg.Default.SampleRate == nil || *cfg.Default.SampleRate != 0.75 {
		t.Errorf("expected default.sample_rate=0.75, got %v", *cfg.Default.SampleRate)
	}
}

func TestParseSailfishData_TOML(t *testing.T) {
	data := []byte(`
[default]
capture_arguments = true
sample_rate = 0.8

[files."*.go"]
capture_return_value = false

[functions.MyFunc]
arg_limit_mb = 4
`)
	cfg, err := parseSailfishData(data)
	if err != nil {
		t.Fatalf("failed to parse TOML: %v", err)
	}
	if cfg.Default == nil {
		t.Fatal("expected default section")
	}
	if cfg.Default.CaptureArguments == nil || !*cfg.Default.CaptureArguments {
		t.Error("expected default.capture_arguments=true")
	}
	if cfg.Default.SampleRate == nil || *cfg.Default.SampleRate != 0.8 {
		t.Errorf("expected default.sample_rate=0.8, got %v", *cfg.Default.SampleRate)
	}
	if len(cfg.Functions) != 1 {
		t.Errorf("expected 1 function config, got %d", len(cfg.Functions))
	}
	fc := cfg.Functions["MyFunc"]
	if fc.ArgLimitMB == nil || *fc.ArgLimitMB != 4 {
		t.Error("expected MyFunc.arg_limit_mb=4")
	}
}

func TestParseSailfishData_InvalidData(t *testing.T) {
	data := []byte(`<<<not valid anything>>>`)
	_, err := parseSailfishData(data)
	if err == nil {
		t.Error("expected error for invalid data")
	}
}

// --- getFuncspanConfig ---

func TestGetFuncspanConfig_NilConfig(t *testing.T) {
	// Save and restore
	saved := sailfishConfig
	sailfishConfig = nil
	defer func() { sailfishConfig = saved }()

	fc := getFuncspanConfig("test.go", "MyFunc")
	if fc != nil {
		t.Error("expected nil when sailfishConfig is nil")
	}
}

func TestGetFuncspanConfig_FunctionMatch(t *testing.T) {
	boolTrue := true
	saved := sailfishConfig
	sailfishConfig = &sailfishFileConfig{
		Functions: map[string]funcspanFunctionConfig{
			"MyFunc": {CaptureArguments: &boolTrue},
		},
	}
	defer func() { sailfishConfig = saved }()

	fc := getFuncspanConfig("any/path.go", "MyFunc")
	if fc == nil {
		t.Fatal("expected function config match")
	}
	if fc.CaptureArguments == nil || !*fc.CaptureArguments {
		t.Error("expected CaptureArguments=true")
	}
}

func TestGetFuncspanConfig_FileGlobMatch(t *testing.T) {
	boolFalse := false
	saved := sailfishConfig
	sailfishConfig = &sailfishFileConfig{
		Files: map[string]funcspanFileConfig{
			"*.go": {CaptureReturnValue: &boolFalse},
		},
	}
	defer func() { sailfishConfig = saved }()

	fc := getFuncspanConfig("myfile.go", "AnyFunc")
	if fc == nil {
		t.Fatal("expected file config match via glob")
	}
	if fc.CaptureReturnValue == nil || *fc.CaptureReturnValue {
		t.Error("expected CaptureReturnValue=false")
	}
}

func TestGetFuncspanConfig_FileSuffixMatch(t *testing.T) {
	boolTrue := true
	saved := sailfishConfig
	sailfishConfig = &sailfishFileConfig{
		Files: map[string]funcspanFileConfig{
			"handlers/api.go": {CaptureArguments: &boolTrue},
		},
	}
	defer func() { sailfishConfig = saved }()

	fc := getFuncspanConfig("/app/handlers/api.go", "Handler")
	if fc == nil {
		t.Fatal("expected file config match via suffix")
	}
}

func TestGetFuncspanConfig_DefaultFallback(t *testing.T) {
	rate := 0.5
	saved := sailfishConfig
	sailfishConfig = &sailfishFileConfig{
		Default: &funcspanFunctionConfig{SampleRate: &rate},
	}
	defer func() { sailfishConfig = saved }()

	fc := getFuncspanConfig("no_match.go", "NoMatch")
	if fc == nil {
		t.Fatal("expected default fallback")
	}
	if fc.SampleRate == nil || *fc.SampleRate != 0.5 {
		t.Error("expected SampleRate=0.5 from default")
	}
}

func TestGetFuncspanConfig_FunctionPriorityOverFile(t *testing.T) {
	boolTrue := true
	boolFalse := false
	saved := sailfishConfig
	sailfishConfig = &sailfishFileConfig{
		Files: map[string]funcspanFileConfig{
			"*.go": {CaptureArguments: &boolFalse},
		},
		Functions: map[string]funcspanFunctionConfig{
			"HighPriority": {CaptureArguments: &boolTrue},
		},
	}
	defer func() { sailfishConfig = saved }()

	// Function-level should take priority
	fc := getFuncspanConfig("test.go", "HighPriority")
	if fc == nil || fc.CaptureArguments == nil || !*fc.CaptureArguments {
		t.Error("expected function config to take priority over file config")
	}
}

// --- loadSailfishConfig with temp files ---

func TestLoadSailfishConfig_JSONFile(t *testing.T) {
	// Create a temp dir with a .sailfish JSON file
	tmpDir := t.TempDir()
	sfFile := filepath.Join(tmpDir, ".sailfish")
	os.WriteFile(sfFile, []byte(`{"default": {"capture_arguments": true}}`), 0644)

	// Save and restore global state
	saved := sailfishConfig
	sailfishConfig = nil
	origDir, _ := os.Getwd()
	defer func() {
		sailfishConfig = saved
		os.Chdir(origDir)
	}()

	os.Chdir(tmpDir)
	loadSailfishConfig()

	if sailfishConfig == nil {
		t.Fatal("expected config to be loaded")
	}
	if sailfishConfig.Default == nil || sailfishConfig.Default.CaptureArguments == nil {
		t.Fatal("expected default.capture_arguments to be set")
	}
	if !*sailfishConfig.Default.CaptureArguments {
		t.Error("expected capture_arguments=true")
	}
}

func TestLoadSailfishConfig_YAMLFile(t *testing.T) {
	tmpDir := t.TempDir()
	sfFile := filepath.Join(tmpDir, ".sailfish")
	os.WriteFile(sfFile, []byte("default:\n  sample_rate: 0.42\n"), 0644)

	saved := sailfishConfig
	sailfishConfig = nil
	origDir, _ := os.Getwd()
	defer func() {
		sailfishConfig = saved
		os.Chdir(origDir)
	}()

	os.Chdir(tmpDir)
	loadSailfishConfig()

	if sailfishConfig == nil {
		t.Fatal("expected config to be loaded from YAML")
	}
	if sailfishConfig.Default == nil || sailfishConfig.Default.SampleRate == nil {
		t.Fatal("expected default.sample_rate to be set")
	}
	if *sailfishConfig.Default.SampleRate != 0.42 {
		t.Errorf("expected sample_rate=0.42, got %f", *sailfishConfig.Default.SampleRate)
	}
}

func TestLoadSailfishConfig_TOMLFile(t *testing.T) {
	tmpDir := t.TempDir()
	sfFile := filepath.Join(tmpDir, ".sailfish")
	os.WriteFile(sfFile, []byte("[default]\nsample_rate = 0.33\n"), 0644)

	saved := sailfishConfig
	sailfishConfig = nil
	origDir, _ := os.Getwd()
	defer func() {
		sailfishConfig = saved
		os.Chdir(origDir)
	}()

	os.Chdir(tmpDir)
	loadSailfishConfig()

	if sailfishConfig == nil {
		t.Fatal("expected config to be loaded from TOML")
	}
	if sailfishConfig.Default == nil || sailfishConfig.Default.SampleRate == nil {
		t.Fatal("expected default.sample_rate to be set")
	}
	if *sailfishConfig.Default.SampleRate != 0.33 {
		t.Errorf("expected sample_rate=0.33, got %f", *sailfishConfig.Default.SampleRate)
	}
}

func TestLoadSailfishConfig_DirectoryConfig(t *testing.T) {
	tmpDir := t.TempDir()
	sfDir := filepath.Join(tmpDir, ".sailfish")
	os.Mkdir(sfDir, 0755)
	os.WriteFile(filepath.Join(sfDir, "config.json"), []byte(`{"default": {"capture_arguments": false}}`), 0644)

	saved := sailfishConfig
	sailfishConfig = nil
	origDir, _ := os.Getwd()
	defer func() {
		sailfishConfig = saved
		os.Chdir(origDir)
	}()

	os.Chdir(tmpDir)
	loadSailfishConfig()

	if sailfishConfig == nil {
		t.Fatal("expected config to be loaded from .sailfish/config.json")
	}
}

func TestLoadSailfishConfig_DirectoryConfigTOML(t *testing.T) {
	tmpDir := t.TempDir()
	sfDir := filepath.Join(tmpDir, ".sailfish")
	os.Mkdir(sfDir, 0755)
	os.WriteFile(filepath.Join(sfDir, "config.toml"), []byte("[default]\ncapture_arguments = true\n"), 0644)

	saved := sailfishConfig
	sailfishConfig = nil
	origDir, _ := os.Getwd()
	defer func() {
		sailfishConfig = saved
		os.Chdir(origDir)
	}()

	os.Chdir(tmpDir)
	loadSailfishConfig()

	if sailfishConfig == nil {
		t.Fatal("expected config to be loaded from .sailfish/config.toml")
	}
}

func TestLoadSailfishConfig_DirectoryConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()
	sfDir := filepath.Join(tmpDir, ".sailfish")
	os.Mkdir(sfDir, 0755)
	os.WriteFile(filepath.Join(sfDir, "config.yaml"), []byte("default:\n  capture_arguments: true\n"), 0644)

	saved := sailfishConfig
	sailfishConfig = nil
	origDir, _ := os.Getwd()
	defer func() {
		sailfishConfig = saved
		os.Chdir(origDir)
	}()

	os.Chdir(tmpDir)
	loadSailfishConfig()

	if sailfishConfig == nil {
		t.Fatal("expected config to be loaded from .sailfish/config.yaml")
	}
}
