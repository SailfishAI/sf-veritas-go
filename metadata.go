package sfveritas

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// Identify associates a user ID and optional traits with the current session.
// Traits can be provided as a map or as a pre-serialized JSON string.
//
// Usage:
//
//	sfveritas.Identify(ctx, "user-123", sfveritas.WithTraits(map[string]interface{}{"plan": "pro"}))
//	sfveritas.Identify(ctx, "user-123", sfveritas.WithTraitsJSON(`{"plan":"pro"}`))
//	sfveritas.Identify(ctx, "user-123", sfveritas.WithOverride(true))
func Identify(ctx context.Context, userID string, opts ...IdentifyOption) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	o := identifyOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	addOrUpdateMetadata(ctx, userID, o.traits, o.traitsJSON, o.override)
}

// IdentifyOption configures Identify behavior.
type IdentifyOption func(*identifyOpts)

type identifyOpts struct {
	traits    map[string]interface{}
	traitsJSON string
	override  bool
}

// WithTraits provides a map of user traits to associate with the session.
func WithTraits(traits map[string]interface{}) IdentifyOption {
	return func(o *identifyOpts) { o.traits = traits }
}

// WithTraitsJSON provides pre-serialized JSON traits string.
func WithTraitsJSON(traitsJSON string) IdentifyOption {
	return func(o *identifyOpts) { o.traitsJSON = traitsJSON }
}

// WithOverride sets whether existing traits should be overwritten.
func WithOverride(override bool) IdentifyOption {
	return func(o *identifyOpts) { o.override = override }
}

func addOrUpdateMetadata(ctx context.Context, userID string, traits map[string]interface{}, traitsJSON string, override bool) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	_, sessionID := GetOrSetTraceID(ctx)

	// Serialize traits to JSON if not provided as string
	var excludedFields []string
	if traitsJSON == "" {
		if traits == nil {
			traits = map[string]interface{}{}
		}
		// Marshal each field individually to detect serialization failures
		cleanTraits := make(map[string]interface{}, len(traits))
		for k, v := range traits {
			if _, err := json.Marshal(v); err != nil {
				excludedFields = append(excludedFields, k)
			} else {
				cleanTraits[k] = v
			}
		}
		b, err := json.Marshal(cleanTraits)
		if err != nil {
			traitsJSON = "{}"
		} else {
			traitsJSON = string(b)
		}
	}
	if excludedFields == nil {
		excludedFields = []string{}
	}

	vars := mergeVariables(map[string]interface{}{
		"sessionId":      sessionID,
		"userId":         userID,
		"traitsJson":     traitsJSON,
		"excludedFields": excludedFields,
		"library":        LibraryType,
		"version":        Version,
		"override":       override,
	})

	nonBlockingPost("CollectMetadata", mutationCollectMetadata, vars)

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] Identify: userId=%s override=%v traitsJson=%s\n",
			userID, override, traitsJSON)
	}
}

// UpdateServiceDetails sends updated service metadata to the backend.
// Use this to dynamically update service identifier, version, or metadata
// after initial setup.
func UpdateServiceDetails(serviceIdentifier, serviceVersion string, metadata map[string]interface{}, gitSha string) {
	cfg := getConfig()
	if cfg == nil {
		return
	}

	var metaJSON interface{}
	if metadata != nil {
		metaJSON = metadata
	}

	vars := mergeVariables(map[string]interface{}{
		"serviceIdentifier":         nilIfEmpty(serviceIdentifier),
		"serviceVersion":            nilIfEmpty(serviceVersion),
		"serviceAdditionalMetadata": metaJSON,
		"gitSha":                    nilIfEmpty(gitSha),
		"gitOrg":                    nilIfEmpty(cfg.gitOrg),
		"gitRepo":                   nilIfEmpty(cfg.gitRepo),
		"gitProvider":               nilIfEmpty(cfg.gitProvider),
		"serviceDisplayName":        nilIfEmpty(cfg.serviceDisplayName),
	})

	nonBlockingPost("UpdateServiceDetails", mutationUpdateServiceDetails, vars)

	if cfg.debug {
		fmt.Fprintf(os.Stderr, "[sfveritas] UpdateServiceDetails: identifier=%s version=%s gitSha=%s\n",
			serviceIdentifier, serviceVersion, gitSha)
	}
}
