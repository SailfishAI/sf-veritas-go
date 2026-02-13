package sfveritas

import (
	"os"
	"regexp"
	"sync"
	"testing"
)

// --- parseGitRemoteURL ---

func TestParseGitRemoteURL_HTTPS_GitHub(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("https://github.com/myorg/myrepo.git")
	if org != "myorg" {
		t.Errorf("expected org=myorg, got %s", org)
	}
	if repo != "myrepo" {
		t.Errorf("expected repo=myrepo, got %s", repo)
	}
	if provider != "github" {
		t.Errorf("expected provider=github, got %s", provider)
	}
}

func TestParseGitRemoteURL_HTTPS_GitLab(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("https://gitlab.com/team/project.git")
	if org != "team" {
		t.Errorf("expected org=team, got %s", org)
	}
	if repo != "project" {
		t.Errorf("expected repo=project, got %s", repo)
	}
	if provider != "gitlab" {
		t.Errorf("expected provider=gitlab, got %s", provider)
	}
}

func TestParseGitRemoteURL_HTTPS_Bitbucket(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("https://bitbucket.org/company/app.git")
	if org != "company" {
		t.Errorf("expected org=company, got %s", org)
	}
	if repo != "app" {
		t.Errorf("expected repo=app, got %s", repo)
	}
	if provider != "bitbucket" {
		t.Errorf("expected provider=bitbucket, got %s", provider)
	}
}

func TestParseGitRemoteURL_SSH_GitHub(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("git@github.com:myorg/myrepo.git")
	if org != "myorg" {
		t.Errorf("expected org=myorg, got %s", org)
	}
	if repo != "myrepo" {
		t.Errorf("expected repo=myrepo, got %s", repo)
	}
	if provider != "github" {
		t.Errorf("expected provider=github, got %s", provider)
	}
}

func TestParseGitRemoteURL_SSH_GitLab(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("git@gitlab.com:team/project.git")
	if org != "team" {
		t.Errorf("expected org=team, got %s", org)
	}
	if repo != "project" {
		t.Errorf("expected repo=project, got %s", repo)
	}
	if provider != "gitlab" {
		t.Errorf("expected provider=gitlab, got %s", provider)
	}
}

func TestParseGitRemoteURL_HTTPS_WithoutGitSuffix(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("https://github.com/myorg/myrepo")
	if org != "myorg" {
		t.Errorf("expected org=myorg, got %s", org)
	}
	if repo != "myrepo" {
		t.Errorf("expected repo=myrepo, got %s", repo)
	}
	if provider != "github" {
		t.Errorf("expected provider=github, got %s", provider)
	}
}

func TestParseGitRemoteURL_EmptyString(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("")
	if org != "" || repo != "" || provider != "" {
		t.Errorf("expected empty results, got org=%q repo=%q provider=%q", org, repo, provider)
	}
}

func TestParseGitRemoteURL_UnknownProvider(t *testing.T) {
	org, repo, provider := parseGitRemoteURL("https://selfhosted.example.com/team/project.git")
	if org != "team" {
		t.Errorf("expected org=team, got %s", org)
	}
	if repo != "project" {
		t.Errorf("expected repo=project, got %s", repo)
	}
	if provider != "" {
		t.Errorf("expected empty provider, got %s", provider)
	}
}

// --- detectIsLocal ---

func TestDetectIsLocal_Default(t *testing.T) {
	// Clear all cloud env vars to test default
	cloudVars := []string{
		"SF_IS_LOCAL", "KUBERNETES_SERVICE_HOST", "AWS_EXECUTION_ENV",
		"AWS_LAMBDA_FUNCTION_NAME", "GOOGLE_CLOUD_PROJECT",
		"AZURE_FUNCTIONS_ENVIRONMENT", "VERCEL", "NETLIFY",
		"FLY_APP_NAME", "RENDER_SERVICE_ID", "RAILWAY_ENVIRONMENT",
	}
	for _, v := range cloudVars {
		os.Unsetenv(v)
	}
	if !detectIsLocal() {
		t.Error("expected true when no cloud markers present")
	}
}

func TestDetectIsLocal_ExplicitTrue(t *testing.T) {
	os.Setenv("SF_IS_LOCAL", "true")
	defer os.Unsetenv("SF_IS_LOCAL")
	if !detectIsLocal() {
		t.Error("expected true when SF_IS_LOCAL=true")
	}
}

func TestDetectIsLocal_ExplicitFalse(t *testing.T) {
	os.Setenv("SF_IS_LOCAL", "false")
	defer os.Unsetenv("SF_IS_LOCAL")
	if detectIsLocal() {
		t.Error("expected false when SF_IS_LOCAL=false")
	}
}

func TestDetectIsLocal_KubernetesDetected(t *testing.T) {
	os.Unsetenv("SF_IS_LOCAL")
	os.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	defer os.Unsetenv("KUBERNETES_SERVICE_HOST")
	if detectIsLocal() {
		t.Error("expected false when KUBERNETES_SERVICE_HOST is set")
	}
}

func TestDetectIsLocal_AWSLambdaDetected(t *testing.T) {
	os.Unsetenv("SF_IS_LOCAL")
	os.Setenv("AWS_LAMBDA_FUNCTION_NAME", "my-function")
	defer os.Unsetenv("AWS_LAMBDA_FUNCTION_NAME")
	if detectIsLocal() {
		t.Error("expected false when AWS_LAMBDA_FUNCTION_NAME is set")
	}
}

func TestDetectIsLocal_VercelDetected(t *testing.T) {
	os.Unsetenv("SF_IS_LOCAL")
	os.Setenv("VERCEL", "1")
	defer os.Unsetenv("VERCEL")
	if detectIsLocal() {
		t.Error("expected false when VERCEL is set")
	}
}

// --- isRouteDisabled ---

func TestIsRouteDisabled_NilConfig(t *testing.T) {
	saved := globalConfig
	globalConfig = nil
	defer func() { globalConfig = saved }()
	if isRouteDisabled("/health") {
		t.Error("expected false when config is nil")
	}
}

func TestIsRouteDisabled_EmptyRoutes(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{disabledInboundRoutes: nil}
	defer func() { globalConfig = saved }()
	if isRouteDisabled("/api/data") {
		t.Error("expected false when no disabled routes")
	}
}

func TestIsRouteDisabled_ExactMatch(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{disabledInboundRoutes: []string{"/health", "/ready"}}
	defer func() { globalConfig = saved }()
	if !isRouteDisabled("/health") {
		t.Error("expected /health to be disabled")
	}
	if !isRouteDisabled("/ready") {
		t.Error("expected /ready to be disabled")
	}
	if isRouteDisabled("/api/data") {
		t.Error("expected /api/data to NOT be disabled")
	}
}

func TestIsRouteDisabled_GlobMatch(t *testing.T) {
	saved := globalConfig
	globalConfig = &config{disabledInboundRoutes: []string{"/health*"}}
	defer func() { globalConfig = saved }()
	if !isRouteDisabled("/health") {
		t.Error("expected /health to match /health*")
	}
	if !isRouteDisabled("/healthz") {
		t.Error("expected /healthz to match /health*")
	}
}

// --- initConfig env var parsing ---

// resetConfigForTest clears the sync.Once so initConfig can be called again
func resetConfigForTest() {
	globalConfig = nil
	configOnce = sync.Once{}
}

func TestInitConfig_DefaultEndpoint(t *testing.T) {
	resetConfigForTest()
	os.Unsetenv("SAILFISH_GRAPHQL_ENDPOINT")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.graphqlEndpoint != defaultGraphQLEndpoint {
		t.Errorf("expected default endpoint, got %s", cfg.graphqlEndpoint)
	}
}

func TestInitConfig_CustomEndpoint(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SAILFISH_GRAPHQL_ENDPOINT", "https://custom.example.com/graphql/")
	defer os.Unsetenv("SAILFISH_GRAPHQL_ENDPOINT")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.graphqlEndpoint != "https://custom.example.com/graphql/" {
		t.Errorf("expected custom endpoint, got %s", cfg.graphqlEndpoint)
	}
}

func TestInitConfig_Debug(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_DEBUG", "true")
	defer os.Unsetenv("SF_DEBUG")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.debug {
		t.Error("expected debug=true when SF_DEBUG=true")
	}
}

func TestInitConfig_ArgLimit(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_FUNCSPAN_ARG_LIMIT_MB", "5")
	defer os.Unsetenv("SF_FUNCSPAN_ARG_LIMIT_MB")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	expected := 5 * 1024 * 1024
	if cfg.argLimitBytes != expected {
		t.Errorf("expected argLimitBytes=%d, got %d", expected, cfg.argLimitBytes)
	}
}

func TestInitConfig_ReturnLimit(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_FUNCSPAN_RETURN_LIMIT_MB", "3")
	defer os.Unsetenv("SF_FUNCSPAN_RETURN_LIMIT_MB")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	expected := 3 * 1024 * 1024
	if cfg.returnLimitBytes != expected {
		t.Errorf("expected returnLimitBytes=%d, got %d", expected, cfg.returnLimitBytes)
	}
}

func TestInitConfig_HeaderCaptureDefaults(t *testing.T) {
	resetConfigForTest()
	os.Unsetenv("SF_NETWORKHOP_CAPTURE_REQUEST_HEADERS")
	os.Unsetenv("SF_NETWORKHOP_CAPTURE_RESPONSE_HEADERS")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.captureRequestHeaders {
		t.Error("expected captureRequestHeaders=false by default")
	}
	if cfg.captureResponseHeaders {
		t.Error("expected captureResponseHeaders=false by default")
	}
}

func TestInitConfig_HeaderCaptureEnabled(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_NETWORKHOP_CAPTURE_REQUEST_HEADERS", "true")
	os.Setenv("SF_NETWORKHOP_CAPTURE_RESPONSE_HEADERS", "true")
	defer os.Unsetenv("SF_NETWORKHOP_CAPTURE_REQUEST_HEADERS")
	defer os.Unsetenv("SF_NETWORKHOP_CAPTURE_RESPONSE_HEADERS")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.captureRequestHeaders {
		t.Error("expected captureRequestHeaders=true")
	}
	if !cfg.captureResponseHeaders {
		t.Error("expected captureResponseHeaders=true")
	}
}

func TestInitConfig_GlobalCaptureArgs(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_FUNCSPAN_CAPTURE_ARGUMENTS", "true")
	defer os.Unsetenv("SF_FUNCSPAN_CAPTURE_ARGUMENTS")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.globalCaptureArgs {
		t.Error("expected globalCaptureArgs=true")
	}
}

func TestInitConfig_GlobalCaptureArgsDefault(t *testing.T) {
	resetConfigForTest()
	os.Unsetenv("SF_FUNCSPAN_CAPTURE_ARGUMENTS")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.globalCaptureArgs {
		t.Error("expected globalCaptureArgs=false by default")
	}
}

func TestInitConfig_ParseJSONStrings(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_FUNCSPAN_PARSE_JSON_STRINGS", "true")
	defer os.Unsetenv("SF_FUNCSPAN_PARSE_JSON_STRINGS")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.parseJSONStrings {
		t.Error("expected parseJSONStrings=true")
	}
}

func TestInitConfig_AutoCaptureChildren(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_FUNCSPAN_AUTOCAPTURE_ALL_CHILD_FUNCTIONS", "false")
	defer os.Unsetenv("SF_FUNCSPAN_AUTOCAPTURE_ALL_CHILD_FUNCTIONS")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.autoCaptureChildren {
		t.Error("expected autoCaptureChildren=false")
	}
}

func TestInitConfig_AutoCaptureChildrenDefault(t *testing.T) {
	resetConfigForTest()
	os.Unsetenv("SF_FUNCSPAN_AUTOCAPTURE_ALL_CHILD_FUNCTIONS")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.autoCaptureChildren {
		t.Error("expected autoCaptureChildren=true by default")
	}
}

func TestInitConfig_LogIgnoreRegex(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_LOG_IGNORE_REGEX", "^health")
	defer os.Unsetenv("SF_LOG_IGNORE_REGEX")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.logIgnoreRegex == nil {
		t.Fatal("expected logIgnoreRegex to be set")
	}
	if !cfg.logIgnoreRegex.MatchString("health check") {
		t.Error("expected regex to match 'health check'")
	}
	if cfg.logIgnoreRegex.MatchString("normal log") {
		t.Error("expected regex to NOT match 'normal log'")
	}
}

func TestInitConfig_LogIgnoreRegexInvalid(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_LOG_IGNORE_REGEX", "[invalid")
	defer os.Unsetenv("SF_LOG_IGNORE_REGEX")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.logIgnoreRegex != nil {
		t.Error("expected logIgnoreRegex to be nil for invalid regex")
	}
}

func TestInitConfig_StackDepthCodeTrace(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SAILFISH_EXCEPTION_STACK_DEPTH_CODE_TRACE_DEPTH", "5")
	defer os.Unsetenv("SAILFISH_EXCEPTION_STACK_DEPTH_CODE_TRACE_DEPTH")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.stackDepthCodeTraceDepth != 5 {
		t.Errorf("expected stackDepthCodeTraceDepth=5, got %d", cfg.stackDepthCodeTraceDepth)
	}
}

func TestInitConfig_StackDepthCodeTraceDefault(t *testing.T) {
	resetConfigForTest()
	os.Unsetenv("SAILFISH_EXCEPTION_STACK_DEPTH_CODE_TRACE_DEPTH")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.stackDepthCodeTraceDepth != -1 {
		t.Errorf("expected stackDepthCodeTraceDepth=-1 (default), got %d", cfg.stackDepthCodeTraceDepth)
	}
}

func TestInitConfig_DisabledInboundRoutes(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_DISABLE_INBOUND_NETWORK_TRACING_ON_ROUTES", "/health, /ready, /metrics")
	defer os.Unsetenv("SF_DISABLE_INBOUND_NETWORK_TRACING_ON_ROUTES")
	defer resetConfigForTest()

	cfg := initConfig(Options{APIKey: "test-key"})
	if len(cfg.disabledInboundRoutes) != 3 {
		t.Errorf("expected 3 disabled routes, got %d", len(cfg.disabledInboundRoutes))
	}
	if cfg.disabledInboundRoutes[0] != "/health" {
		t.Errorf("expected first route /health, got %s", cfg.disabledInboundRoutes[0])
	}
}

func TestInitConfig_BodyCapture(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_NETWORKHOP_CAPTURE_REQUEST_BODY", "true")
	os.Setenv("SF_NETWORKHOP_CAPTURE_RESPONSE_BODY", "true")
	os.Setenv("SF_NETWORKHOP_REQUEST_LIMIT_MB", "2")
	os.Setenv("SF_NETWORKHOP_RESPONSE_LIMIT_MB", "3")
	defer func() {
		os.Unsetenv("SF_NETWORKHOP_CAPTURE_REQUEST_BODY")
		os.Unsetenv("SF_NETWORKHOP_CAPTURE_RESPONSE_BODY")
		os.Unsetenv("SF_NETWORKHOP_REQUEST_LIMIT_MB")
		os.Unsetenv("SF_NETWORKHOP_RESPONSE_LIMIT_MB")
		resetConfigForTest()
	}()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.captureRequestBody {
		t.Error("expected captureRequestBody=true")
	}
	if !cfg.captureResponseBody {
		t.Error("expected captureResponseBody=true")
	}
	if cfg.requestBodyLimitBytes != 2*1024*1024 {
		t.Errorf("expected requestBodyLimitBytes=%d, got %d", 2*1024*1024, cfg.requestBodyLimitBytes)
	}
	if cfg.responseBodyLimitBytes != 3*1024*1024 {
		t.Errorf("expected responseBodyLimitBytes=%d, got %d", 3*1024*1024, cfg.responseBodyLimitBytes)
	}
}

func TestInitConfig_DomainExclusion(t *testing.T) {
	resetConfigForTest()
	defer resetConfigForTest()

	cfg := initConfig(Options{
		APIKey: "test-key",
		DomainsToNotPropagateHeadersTo: []string{
			"api.sailfishqa.com",
			"*.googleapis.com",
			"example.com",
		},
	})
	if len(cfg.excludedDomainsExact) != 2 {
		t.Errorf("expected 2 exact domains, got %d", len(cfg.excludedDomainsExact))
	}
	if _, ok := cfg.excludedDomainsExact["api.sailfishqa.com"]; !ok {
		t.Error("expected 'api.sailfishqa.com' in exact domains")
	}
	if len(cfg.excludedDomainsSuffix) != 1 {
		t.Errorf("expected 1 suffix domain, got %d", len(cfg.excludedDomainsSuffix))
	}
	if cfg.excludedDomainsSuffix[0] != ".googleapis.com" {
		t.Errorf("expected '.googleapis.com', got %s", cfg.excludedDomainsSuffix[0])
	}
}

func TestInitConfig_SamplingEnabled(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SF_FUNCSPAN_ENABLE_SAMPLING", "true")
	os.Setenv("SF_FUNCSPAN_SAMPLE_RATE", "0.5")
	defer func() {
		os.Unsetenv("SF_FUNCSPAN_ENABLE_SAMPLING")
		os.Unsetenv("SF_FUNCSPAN_SAMPLE_RATE")
		resetConfigForTest()
	}()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.funcspanSamplingEnabled {
		t.Error("expected funcspanSamplingEnabled=true")
	}
	if cfg.funcspanSampleRate != 0.5 {
		t.Errorf("expected funcspanSampleRate=0.5, got %f", cfg.funcspanSampleRate)
	}
}

func TestInitConfig_SampleRateAutoEnablesSampling(t *testing.T) {
	resetConfigForTest()
	os.Unsetenv("SF_FUNCSPAN_ENABLE_SAMPLING")
	os.Setenv("SF_FUNCSPAN_SAMPLE_RATE", "0.3")
	defer func() {
		os.Unsetenv("SF_FUNCSPAN_SAMPLE_RATE")
		resetConfigForTest()
	}()

	cfg := initConfig(Options{APIKey: "test-key"})
	if !cfg.funcspanSamplingEnabled {
		t.Error("expected sampling to be auto-enabled when rate < 1.0")
	}
}

func TestInitConfig_GitMetadata(t *testing.T) {
	resetConfigForTest()
	os.Setenv("GIT_ORG", "myorg")
	os.Setenv("GIT_REPO", "myrepo")
	os.Setenv("GIT_PROVIDER", "github")
	os.Setenv("SERVICE_DISPLAY_NAME", "My Service")
	defer func() {
		os.Unsetenv("GIT_ORG")
		os.Unsetenv("GIT_REPO")
		os.Unsetenv("GIT_PROVIDER")
		os.Unsetenv("SERVICE_DISPLAY_NAME")
		resetConfigForTest()
	}()

	cfg := initConfig(Options{APIKey: "test-key"})
	if cfg.gitOrg != "myorg" {
		t.Errorf("expected gitOrg=myorg, got %s", cfg.gitOrg)
	}
	if cfg.gitRepo != "myrepo" {
		t.Errorf("expected gitRepo=myrepo, got %s", cfg.gitRepo)
	}
	if cfg.gitProvider != "github" {
		t.Errorf("expected gitProvider=github, got %s", cfg.gitProvider)
	}
	if cfg.serviceDisplayName != "My Service" {
		t.Errorf("expected serviceDisplayName='My Service', got %s", cfg.serviceDisplayName)
	}
}

func TestInitConfig_ServiceIdentifierEnvVar(t *testing.T) {
	resetConfigForTest()
	os.Setenv("SERVICE_IDENTIFIER", "env-svc")
	defer func() {
		os.Unsetenv("SERVICE_IDENTIFIER")
		resetConfigForTest()
	}()

	cfg := initConfig(Options{APIKey: "test-key", ServiceIdentifier: "opt-svc"})
	if cfg.serviceIdentifier != "env-svc" {
		t.Errorf("expected env var to override option, got %s", cfg.serviceIdentifier)
	}
}

// --- nilIfEmpty / nilIntIfZero helpers ---

func TestNilIfEmpty(t *testing.T) {
	if nilIfEmpty("") != nil {
		t.Error("expected nil for empty string")
	}
	if nilIfEmpty("hello") != "hello" {
		t.Error("expected 'hello'")
	}
}

func TestNilIntIfZero(t *testing.T) {
	if nilIntIfZero(0) != nil {
		t.Error("expected nil for zero")
	}
	if nilIntIfZero(42) != 42 {
		t.Error("expected 42")
	}
}

// --- config logIgnoreRegex integration ---

func TestConfigLogIgnoreRegex_NilSafe(t *testing.T) {
	// Test that nil logIgnoreRegex doesn't cause panic
	var re *regexp.Regexp
	if re != nil && re.MatchString("test") {
		t.Error("should not match with nil regex")
	}
}
