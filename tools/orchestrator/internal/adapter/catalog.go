package adapter

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed compatibility_catalog.json
var compatibilityCatalogJSON []byte

// compatibility_catalog.json is the frozen published-stable window.
// It is not a live "latest" lookup and is not refreshed on each run.
type catalogFile struct {
	FrozenAt  string                     `json:"frozen_at"`
	Providers map[string]catalogProvider `json:"providers"`
}

type catalogProvider struct {
	Baseline string           `json:"baseline"`
	Ceiling  string           `json:"ceiling"`
	Protocol string           `json:"protocol"`
	Versions []catalogRelease `json:"versions"`
}

type catalogRelease struct {
	Version     string `json:"version"`
	PublishedAt string `json:"published_at"`
}

type providerPolicy struct {
	baseline     parsedVersion
	ceiling      parsedVersion
	baselineText string
	ceilingText  string
	protocol     string
	order        []string
	published    map[string]struct{}
}

type loadedCatalog struct {
	frozenAt   string
	byProvider map[Provider]*providerPolicy
}

var (
	catalogOnce   sync.Once
	catalogLoaded loadedCatalog
	catalogErr    error
)

func loadCatalog() (*loadedCatalog, error) {
	catalogOnce.Do(parseCompatibilityCatalog)
	if catalogErr != nil {
		return nil, catalogErr
	}
	return &catalogLoaded, nil
}

func parseCompatibilityCatalog() {
	var file catalogFile
	if err := json.Unmarshal(compatibilityCatalogJSON, &file); err != nil {
		catalogErr = err
		return
	}
	catalogLoaded.frozenAt = file.FrozenAt
	catalogLoaded.byProvider = map[Provider]*providerPolicy{}
	for name, spec := range file.Providers {
		policy, err := newProviderPolicy(spec)
		if err != nil {
			catalogErr = err
			return
		}
		catalogLoaded.byProvider[Provider(name)] = policy
	}
}

func newProviderPolicy(spec catalogProvider) (*providerPolicy, error) {
	baseline, err := parseCLIVersion(spec.Baseline)
	if err != nil {
		return nil, err
	}
	ceiling, err := parseCLIVersion(spec.Ceiling)
	if err != nil {
		return nil, err
	}
	policy := &providerPolicy{
		baseline: baseline, ceiling: ceiling,
		baselineText: spec.Baseline, ceilingText: spec.Ceiling,
		protocol: spec.Protocol, published: map[string]struct{}{},
	}
	for _, release := range spec.Versions {
		policy.order = append(policy.order, release.Version)
		policy.published[release.Version] = struct{}{}
	}
	return policy, nil
}

func PublishedVersions(provider Provider) []string {
	catalog, err := loadCatalog()
	if err != nil {
		return nil
	}
	policy := catalog.byProvider[provider]
	if policy == nil {
		return nil
	}
	return append([]string(nil), policy.order...)
}
