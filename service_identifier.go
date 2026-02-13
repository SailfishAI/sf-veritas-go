package sfveritas

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"
)

// infraType represents the detected infrastructure type.
type infraType string

const (
	infraDocker     infraType = "docker"
	infraKubernetes infraType = "kubernetes"
	infraBareMetal  infraType = "bare_metal"
)

// detectInfra detects the runtime infrastructure environment.
func detectInfra() (infraType, map[string]interface{}) {
	details := map[string]interface{}{
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
		"numCPU":  runtime.NumCPU(),
		"goVer":   runtime.Version(),
	}

	// Check for Kubernetes
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		details["podName"] = os.Getenv("HOSTNAME")
		details["namespace"] = os.Getenv("POD_NAMESPACE")
		return infraKubernetes, details
	}

	// Check for Docker
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return infraDocker, details
	}

	return infraBareMetal, details
}

// sendServiceIdentification sends the IdentifyServiceDetails mutation.
func sendServiceIdentification(callerFile string, callerLine int) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	infraT, infraDetails := detectInfra()

	vars := map[string]interface{}{
		"apiKey":                       cfg.apiKey,
		"serviceUuid":                  cfg.serviceUUID,
		"timestampMs":                  strconv.FormatInt(time.Now().UnixMilli(), 10),
		"serviceIdentifier":            cfg.serviceIdentifier,
		"serviceVersion":               cfg.serviceVersion,
		"serviceAdditionalMetadata":    cfg.additionalMeta,
		"library":                      LibraryType,
		"version":                      Version,
		"infrastructureType":           string(infraT),
		"infrastructureDetails":        infraDetails,
		"setupInterceptorsFilePath":    callerFile,
		"setupInterceptorsLineNumber":  callerLine,
		"gitSha":                       cfg.gitSha,
		"gitOrg":                       nilIfEmpty(cfg.gitOrg),
		"gitRepo":                      nilIfEmpty(cfg.gitRepo),
		"gitProvider":                  nilIfEmpty(cfg.gitProvider),
		"serviceDisplayName":           nilIfEmpty(cfg.serviceDisplayName),
	}

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Sending IdentifyServiceDetails: serviceUUID=%s identifier=%s gitOrg=%s gitRepo=%s gitProvider=%s displayName=%s\n",
			cfg.serviceUUID, cfg.serviceIdentifier, cfg.gitOrg, cfg.gitRepo, cfg.gitProvider, cfg.serviceDisplayName)
	}

	nonBlockingPost("IdentifyServiceDetails", mutationIdentifyServiceDetails, vars)
}

// sendDomainsToExclude sends the DomainsToNotPassHeaderTo mutation.
func sendDomainsToExclude(domains []string) {
	cfg := getConfig()
	if cfg == nil || len(domains) == 0 {
		return
	}

	vars := mergeVariables(map[string]interface{}{
		"domains": domains,
		"gitSha":  cfg.gitSha,
	})

	nonBlockingPost("DomainsToNotPassHeaderTo", mutationDomainsToNotPassHeaderTo, vars)
}
