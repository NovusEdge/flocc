package ship

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/plugins"
)

type planned struct {
	List        plugins.List
	ArchiveFrom map[string]string // marketplace name → its local clone
	Warnings    []string
}

func planPlugins(configDir string, private func(repo string) (bool, error)) (planned, error) {
	p := planned{ArchiveFrom: map[string]string{}}
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	var known map[string]struct {
		Source struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
		} `json:"source"`
		InstallLocation string `json:"installLocation"`
	}
	for path, v := range map[string]any{"settings.json": &settings, "plugins/known_marketplaces.json": &known} {
		b, err := os.ReadFile(filepath.Join(configDir, path))
		if err != nil {
			return p, err
		}
		if err := json.Unmarshal(b, v); err != nil {
			return p, fmt.Errorf("%s: %w", path, err)
		}
	}
	var names []string
	for name, on := range settings.EnabledPlugins {
		if on {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	added := map[string]bool{}
	for _, name := range names {
		_, mp, _ := strings.Cut(name, "@")
		k, ok := known[mp]
		if !ok || k.Source.Source != "github" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: marketplace %q is not a known GitHub marketplace; skipped", name, mp))
			continue
		}
		p.List.Plugins = append(p.List.Plugins, name)
		if added[mp] {
			continue
		}
		added[mp] = true
		priv, err := private(k.Source.Repo)
		if err != nil {
			return p, fmt.Errorf("%s visibility: %w", k.Source.Repo, err)
		}
		m := plugins.Marketplace{Name: mp, GitHub: k.Source.Repo}
		if priv {
			m = plugins.Marketplace{Name: mp, Archive: "mp-" + mp + ".tar"}
			p.ArchiveFrom[mp] = k.InstallLocation
		}
		p.List.Marketplaces = append(p.List.Marketplaces, m)
	}
	return p, nil
}
