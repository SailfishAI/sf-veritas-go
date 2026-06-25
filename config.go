package sfveritas

import (
	"bufio"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	defaultArgLimitBytes    = 1 * 1024 * 1024 // 1 MB
	defaultReturnLimitBytes = 1 * 1024 * 1024 // 1 MB
	defaultBodyLimitBytes   = 1 * 1024 * 1024 // 1 MB (matches Python/JS)
)

const (
	defaultGraphQLEndpoint  = "https://api-service.sailfish.ai/graphql/"
	nonsessionApplogs       = "nonsession-applogs"
	tracingHeader           = "X-Sf3-Rid"
	parentSessionHeader     = "X-Sf4-Prid"
	funcspanOverrideHeader  = "X-Sf3-FunctionSpanCaptureOverride"
	telemetryOutboundHeader = "X-Sf3-TelemetryOutbound"

	// WS uplink ("backend debugger") — see uplink.go.
	wsNotifyPath               = "/ws/notify/"
	clientKindBackendCollector = "backendCollector"
)

// Options configures the Sailfish telemetry collector.
type Options struct {
	// APIKey is the Sailfish API key (required).
	APIKey string

	// GraphQLEndpoint is the URL to send telemetry to.
	// Default: "https://api-service.sailfish.ai/graphql/"
	// Override via SAILFISH_GRAPHQL_ENDPOINT env var.
	GraphQLEndpoint string

	// ServiceIdentifier is a human-readable name for this service.
	// Override via SERVICE_IDENTIFIER env var.
	ServiceIdentifier string

	// ServiceVersion is the version of the service.
	// Override via SERVICE_VERSION env var.
	ServiceVersion string

	// GitSha is the git commit hash of the running build.
	// Override via GIT_SHA env var.
	GitSha string

	// ServiceAdditionalMetadata is arbitrary key-value metadata for the service.
	ServiceAdditionalMetadata map[string]interface{}

	// DomainsToNotPropagateHeadersTo is a list of domains where tracing headers
	// should not be injected into outbound requests.
	DomainsToNotPropagateHeadersTo []string

	// GitOrg is the git organization/owner (e.g. "myorg").
	// Override via GIT_ORG or VERCEL_GIT_REPO_OWNER env var.
	GitOrg string

	// GitRepo is the git repository name (e.g. "myrepo").
	// Override via GIT_REPO or VERCEL_GIT_REPO_SLUG env var.
	GitRepo string

	// GitProvider is the git hosting provider (e.g. "github", "gitlab", "bitbucket").
	// Override via GIT_PROVIDER env var.
	GitProvider string

	// ServiceDisplayName is a human-readable display name for this service.
	// Override via SERVICE_DISPLAY_NAME env var.
	ServiceDisplayName string

	// Debug enables verbose debug logging.
	// Override via SF_DEBUG=true env var.
	Debug bool
}

// config holds the resolved runtime configuration (singleton).
type config struct {
	apiKey            string
	graphqlEndpoint   string
	serviceUUID       string
	serviceIdentifier string
	serviceVersion    string
	gitSha            string
	additionalMeta    map[string]interface{}
	excludedDomains   []string
	debug             bool

	// Git metadata
	gitOrg             string
	gitRepo            string
	gitProvider        string
	serviceDisplayName string

	// Function span size limits (bytes)
	argLimitBytes    int
	returnLimitBytes int

	// Pre-computed for O(1) domain exclusion lookups
	excludedDomainsExact  map[string]struct{} // exact matches (lowercased)
	excludedDomainsSuffix []string            // wildcard suffixes like ".googleapis.com"

	// Body capture (opt-in)
	captureRequestBody     bool
	captureResponseBody    bool
	requestBodyLimitBytes  int
	responseBodyLimitBytes int

	// Header capture (opt-in, default false for OTEL compliance)
	captureRequestHeaders  bool
	captureResponseHeaders bool

	// Function span sampling
	funcspanSamplingEnabled bool
	funcspanSampleRate      float64

	// Global capture toggles
	globalCaptureArgs   bool // SF_FUNCSPAN_CAPTURE_ARGUMENTS (default false)
	globalCaptureReturn bool // SF_FUNCSPAN_CAPTURE_RETURN_VALUE (default false)
	parseJSONStrings    bool // SF_FUNCSPAN_PARSE_JSON_STRINGS (default false)

	// Child function capture
	autoCaptureChildren bool // SF_FUNCSPAN_AUTOCAPTURE_ALL_CHILD_FUNCTIONS (default true)

	// Log ignore regex
	logIgnoreRegex *regexp.Regexp

	// Route-based network tracing suppression
	disabledInboundRoutes []string

	// Exception stack depth limiting (-1 = full, N = first N+1 frames)
	stackDepthCodeTraceDepth int

	// Environment detection
	isFromLocalService bool

	// WS uplink ("backend debugger")
	uplinkEnabled bool   // SF_UPLINK_ENABLE != "false" (default true)
	uplinkURL     string // SF_UPLINK_URL override
}

var (
	globalConfig *config
	configOnce   sync.Once
)

func initConfig(opts Options) *config {
	configOnce.Do(func() {
		endpoint := opts.GraphQLEndpoint
		if env := os.Getenv("SAILFISH_GRAPHQL_ENDPOINT"); env != "" {
			endpoint = env
		}
		if endpoint == "" {
			endpoint = defaultGraphQLEndpoint
		}

		debug := opts.Debug
		if os.Getenv("SF_DEBUG") == "true" {
			debug = true
		}

		svcID := opts.ServiceIdentifier
		if env := os.Getenv("SERVICE_IDENTIFIER"); env != "" {
			svcID = env
		}

		svcVersion := opts.ServiceVersion
		if env := os.Getenv("SERVICE_VERSION"); env != "" {
			svcVersion = env
		}

		gitSha := opts.GitSha
		if env := os.Getenv("GIT_SHA"); env != "" {
			gitSha = env
		}

		svcUUID := os.Getenv("SFT_SERVICE_UUID")
		if svcUUID == "" {
			svcUUID = fastUUID()
			os.Setenv("SFT_SERVICE_UUID", svcUUID)
		}

		// Function span size limits (0 disables capture regardless of capture toggle)
		argLimit := defaultArgLimitBytes
		if v := os.Getenv("SF_FUNCSPAN_ARG_LIMIT_MB"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				argLimit = n * 1024 * 1024
			}
		}
		returnLimit := defaultReturnLimitBytes
		if v := os.Getenv("SF_FUNCSPAN_RETURN_LIMIT_MB"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				returnLimit = n * 1024 * 1024
			}
		}

		// Pre-compute domain exclusion sets for O(1) lookup
		exactDomains := make(map[string]struct{})
		var suffixDomains []string
		for _, d := range opts.DomainsToNotPropagateHeadersTo {
			d = strings.ToLower(d)
			if strings.HasPrefix(d, "*.") {
				suffixDomains = append(suffixDomains, d[1:]) // e.g. ".googleapis.com"
			} else {
				exactDomains[d] = struct{}{}
			}
		}

		// Header capture config (opt-in, default false for OTEL compliance)
		captureReqHeaders := os.Getenv("SF_NETWORKHOP_CAPTURE_REQUEST_HEADERS") == "true"
		captureRespHeaders := os.Getenv("SF_NETWORKHOP_CAPTURE_RESPONSE_HEADERS") == "true"

		// Global function span capture toggles (default false — opt-in, matching JS/TS)
		globalCaptureArgs := os.Getenv("SF_FUNCSPAN_CAPTURE_ARGUMENTS") == "true"     // default false
		globalCaptureReturn := os.Getenv("SF_FUNCSPAN_CAPTURE_RETURN_VALUE") == "true" // default false
		parseJSONStrings := os.Getenv("SF_FUNCSPAN_PARSE_JSON_STRINGS") == "true"       // default false
		autoCaptureChildren := os.Getenv("SF_FUNCSPAN_AUTOCAPTURE_ALL_CHILD_FUNCTIONS") != "false" // default true

		// Body capture config (env var names match Python/JS: SF_NETWORKHOP_*)
		captureReqBody := os.Getenv("SF_NETWORKHOP_CAPTURE_REQUEST_BODY") == "true"
		captureRespBody := os.Getenv("SF_NETWORKHOP_CAPTURE_RESPONSE_BODY") == "true"
		reqBodyLimit := defaultBodyLimitBytes
		if v := os.Getenv("SF_NETWORKHOP_REQUEST_LIMIT_MB"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				reqBodyLimit = n * 1024 * 1024
			}
		}
		respBodyLimit := defaultBodyLimitBytes
		if v := os.Getenv("SF_NETWORKHOP_RESPONSE_LIMIT_MB"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				respBodyLimit = n * 1024 * 1024
			}
		}

		// Function span sampling
		samplingEnabled := os.Getenv("SF_FUNCSPAN_ENABLE_SAMPLING") == "true"
		sampleRate := 1.0
		if v := os.Getenv("SF_FUNCSPAN_SAMPLE_RATE"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
				sampleRate = f
				if f < 1.0 {
					samplingEnabled = true
				}
			}
		}

		// Log ignore regex
		var logIgnore *regexp.Regexp
		if v := os.Getenv("SF_LOG_IGNORE_REGEX"); v != "" {
			if re, err := regexp.Compile(v); err == nil {
				logIgnore = re
			}
		}

		// Exception stack depth code trace depth (-1 = full, N = first N+1 frames)
		stackDepthCodeTrace := -1
		if v := os.Getenv("SAILFISH_EXCEPTION_STACK_DEPTH_CODE_TRACE_DEPTH"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				stackDepthCodeTrace = n
			}
		}

		// Route-based inbound network tracing suppression
		var disabledRoutes []string
		if v := os.Getenv("SF_DISABLE_INBOUND_NETWORK_TRACING_ON_ROUTES"); v != "" {
			for _, p := range strings.Split(v, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					disabledRoutes = append(disabledRoutes, p)
				}
			}
		}

		// Git metadata
		gitOrg := opts.GitOrg
		if env := os.Getenv("GIT_ORG"); env != "" {
			gitOrg = env
		} else if env := os.Getenv("VERCEL_GIT_REPO_OWNER"); env != "" {
			gitOrg = env
		}

		gitRepo := opts.GitRepo
		if env := os.Getenv("GIT_REPO"); env != "" {
			gitRepo = env
		} else if env := os.Getenv("VERCEL_GIT_REPO_SLUG"); env != "" {
			gitRepo = env
		}

		gitProvider := opts.GitProvider
		if env := os.Getenv("GIT_PROVIDER"); env != "" {
			gitProvider = env
		}

		serviceDisplayName := opts.ServiceDisplayName
		if env := os.Getenv("SERVICE_DISPLAY_NAME"); env != "" {
			serviceDisplayName = env
		}

		// Auto-detect git info from .git/config if not set via env/options
		if gitOrg == "" || gitRepo == "" || gitProvider == "" {
			detectedOrg, detectedRepo, detectedProvider := detectGitInfo()
			if gitOrg == "" {
				gitOrg = detectedOrg
			}
			if gitRepo == "" {
				gitRepo = detectedRepo
			}
			if gitProvider == "" {
				gitProvider = detectedProvider
			}
		}

		// Environment detection for isFromLocalService
		isLocal := detectIsLocal()

		globalConfig = &config{
			apiKey:                opts.APIKey,
			graphqlEndpoint:       endpoint,
			serviceUUID:           svcUUID,
			serviceIdentifier:     svcID,
			serviceVersion:        svcVersion,
			gitSha:                gitSha,
			gitOrg:                gitOrg,
			gitRepo:               gitRepo,
			gitProvider:           gitProvider,
			serviceDisplayName:    serviceDisplayName,
			additionalMeta:        opts.ServiceAdditionalMetadata,
			excludedDomains:       opts.DomainsToNotPropagateHeadersTo,
			debug:                 debug,
			argLimitBytes:         argLimit,
			returnLimitBytes:      returnLimit,
			excludedDomainsExact:  exactDomains,
			excludedDomainsSuffix: suffixDomains,
			captureRequestBody:     captureReqBody,
			captureResponseBody:    captureRespBody,
			requestBodyLimitBytes:  reqBodyLimit,
			responseBodyLimitBytes: respBodyLimit,
			captureRequestHeaders:  captureReqHeaders,
			captureResponseHeaders: captureRespHeaders,
			funcspanSamplingEnabled: samplingEnabled,
			funcspanSampleRate:      sampleRate,
			globalCaptureArgs:       globalCaptureArgs,
			globalCaptureReturn:     globalCaptureReturn,
			parseJSONStrings:        parseJSONStrings,
			autoCaptureChildren:     autoCaptureChildren,
			logIgnoreRegex:          logIgnore,
			disabledInboundRoutes:    disabledRoutes,
			stackDepthCodeTraceDepth: stackDepthCodeTrace,
			isFromLocalService:       isLocal,
			uplinkEnabled:            os.Getenv("SF_UPLINK_ENABLE") != "false",
			uplinkURL:                os.Getenv("SF_UPLINK_URL"),
		}
	})
	return globalConfig
}

func getConfig() *config {
	return globalConfig
}

// detectIsLocal determines whether this service is running locally or in the cloud.
func detectIsLocal() bool {
	// Explicit override
	if v := os.Getenv("SF_IS_LOCAL"); v != "" {
		return v == "true"
	}

	// Cloud markers → not local
	cloudEnvVars := []string{
		"KUBERNETES_SERVICE_HOST",
		"AWS_EXECUTION_ENV",
		"AWS_LAMBDA_FUNCTION_NAME",
		"GOOGLE_CLOUD_PROJECT",
		"AZURE_FUNCTIONS_ENVIRONMENT",
		"VERCEL",
		"NETLIFY",
		"FLY_APP_NAME",
		"RENDER_SERVICE_ID",
		"RAILWAY_ENVIRONMENT",
	}
	for _, env := range cloudEnvVars {
		if os.Getenv(env) != "" {
			return false
		}
	}

	// Default: assume local
	return true
}

// detectGitInfo attempts to detect git org, repo, and provider from the git remote URL.
// It first tries `git remote get-url origin`, then falls back to parsing .git/config.
func detectGitInfo() (org, repo, provider string) {
	var remoteURL string

	// Try git command first
	if out, err := exec.Command("git", "remote", "get-url", "origin").Output(); err == nil {
		remoteURL = strings.TrimSpace(string(out))
	} else {
		// Fallback: parse .git/config
		remoteURL = parseGitConfigRemoteURL()
	}

	if remoteURL == "" {
		return "", "", ""
	}

	return parseGitRemoteURL(remoteURL)
}

// parseGitConfigRemoteURL reads the origin remote URL from .git/config.
func parseGitConfigRemoteURL() string {
	f, err := os.Open(".git/config")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inOrigin := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == `[remote "origin"]` {
			inOrigin = true
			continue
		}
		if inOrigin {
			if strings.HasPrefix(line, "[") {
				break // next section
			}
			if strings.HasPrefix(line, "url = ") {
				return strings.TrimPrefix(line, "url = ")
			}
		}
	}
	return ""
}

// parseGitRemoteURL extracts org, repo, and provider from a git remote URL.
// Supports HTTPS (https://github.com/org/repo.git) and SSH (git@github.com:org/repo.git).
func parseGitRemoteURL(remoteURL string) (org, repo, provider string) {
	// Detect provider from URL
	switch {
	case strings.Contains(remoteURL, "github.com"):
		provider = "github"
	case strings.Contains(remoteURL, "gitlab.com"):
		provider = "gitlab"
	case strings.Contains(remoteURL, "bitbucket.org"):
		provider = "bitbucket"
	}

	// Parse HTTPS: https://github.com/org/repo.git
	if strings.HasPrefix(remoteURL, "https://") || strings.HasPrefix(remoteURL, "http://") {
		parts := strings.Split(remoteURL, "/")
		if len(parts) >= 5 {
			org = parts[3]
			repo = strings.TrimSuffix(parts[4], ".git")
		}
		return
	}

	// Parse SSH: git@github.com:org/repo.git
	if idx := strings.Index(remoteURL, ":"); idx > 0 {
		path := remoteURL[idx+1:]
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			org = parts[0]
			repo = parts[1]
		}
	}
	return
}

// isRouteDisabled checks if a URL path matches any disabled inbound route pattern.
func isRouteDisabled(urlPath string) bool {
	cfg := getConfig()
	if cfg == nil || len(cfg.disabledInboundRoutes) == 0 {
		return false
	}
	for _, pattern := range cfg.disabledInboundRoutes {
		if matched, _ := path.Match(pattern, urlPath); matched {
			return true
		}
	}
	return false
}
