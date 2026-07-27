package adapter

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

//go:embed catalog.json
var embeddedCatalog []byte

type Catalog struct {
	Version   int        `json:"version"`
	UpdatedAt string     `json:"updatedAt"`
	Adapters  []Manifest `json:"adapters"`
}

type Manifest struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"displayName"`
	Phase1        bool     `json:"phase1"`
	SupportLevel  string   `json:"supportLevel"`
	Surfaces      []string `json:"surfaces"`
	Platforms     []string `json:"platforms"`
	Dialect       *string  `json:"dialect"`
	Events        []string `json:"events"`
	RoadmapStatus string   `json:"roadmapStatus,omitempty"`
	BlockedBy     string   `json:"blockedBy,omitempty"`
}

func LoadCatalog() (Catalog, error) {
	return ParseCatalog(embeddedCatalog)
}

func ParseCatalog(value []byte) (Catalog, error) {
	var catalog Catalog
	if err := json.Unmarshal(value, &catalog); err != nil {
		return Catalog{}, err
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (catalog Catalog) Validate() error {
	if catalog.Version != 1 {
		return fmt.Errorf("unsupported adapter catalog version %d", catalog.Version)
	}
	ids := make(map[string]bool)
	allowedSupport := map[string]bool{
		"verified": true, "pilot": true, "assisted": true, "unsupported": true,
	}
	allowedDialects := map[string]bool{
		"codex-json-hooks":       true,
		"claude-json-hooks":      true,
		"qoderwork-json-hooks":   true,
		"kimi-plugin-hooks":      true,
		"opencode-plugin-events": true,
		"trae-json-hooks":        true,
		"vendor-plugin-pilot":    true,
		"assisted-mcp-skill":     true,
	}
	for _, manifest := range catalog.Adapters {
		if manifest.ID == "" || manifest.DisplayName == "" {
			return errors.New("adapter id and displayName are required")
		}
		if ids[manifest.ID] {
			return fmt.Errorf("duplicate adapter id %q", manifest.ID)
		}
		ids[manifest.ID] = true
		if !allowedSupport[manifest.SupportLevel] {
			return fmt.Errorf("unsupported support level %q", manifest.SupportLevel)
		}
		if manifest.SupportLevel == "unsupported" {
			if manifest.Phase1 || manifest.Dialect != nil || len(manifest.Events) != 0 {
				return fmt.Errorf("unsupported adapter %q declares runtime support", manifest.ID)
			}
			continue
		}
		if manifest.Dialect == nil || !allowedDialects[*manifest.Dialect] {
			return fmt.Errorf("adapter %q has invalid dialect", manifest.ID)
		}
		if len(manifest.Surfaces) == 0 || len(manifest.Platforms) == 0 {
			return fmt.Errorf("adapter %q has no surfaces or platforms", manifest.ID)
		}
	}
	return nil
}

func (catalog Catalog) Find(id string) (Manifest, bool) {
	for _, manifest := range catalog.Adapters {
		if manifest.ID == id {
			return manifest, true
		}
	}
	return Manifest{}, false
}
