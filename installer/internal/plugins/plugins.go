// Package plugins is the plugin list ship writes and the Claude step installs.
package plugins

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

type Marketplace struct {
	Name   string `json:"name"`
	GitHub string `json:"github,omitempty"` // owner/repo, for public marketplaces
	// A tar of a private marketplace's tree, in the stage dir. Not a git bundle: Claude Code's
	// marketplace clones are shallow, and a bundle of a shallow clone cannot be fetched.
	Archive string `json:"archive,omitempty"`
	Commit  string `json:"commit,omitempty"` // the archived commit, so the host can tell it is stale
}

type List struct {
	Marketplaces []Marketplace `json:"marketplaces"`
	Plugins      []string      `json:"plugins"` // name@marketplace
}

func Load(path string) (List, error) {
	var l List
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	return l, json.Unmarshal(b, &l)
}

func (l List) Write(path string) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
