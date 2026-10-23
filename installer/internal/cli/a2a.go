package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
)

const a2aPort = 7443

// a2aFiles builds each ssh host's a2a.toml and the desktop's. Daytona sandboxes are not on the
// tailnet, so they get none and no host lists them. A repo on two hosts is refused: a message to
// an issue no host has claimed goes to the one host serving its repo.
func a2aFiles(st keys.Store, all []hosts.Host) (map[string]answers.A2AFile, answers.A2AFile, error) {
	owner, err := keys.EnsureToken(st, keys.A2AOwner)
	if err != nil {
		return nil, answers.A2AFile{}, err
	}
	peers := map[string]answers.A2APeer{}
	tokens := map[string]string{}
	servedBy := map[string]string{}
	for _, h := range all {
		if h.SSH == "" {
			continue
		}
		var repos []string
		for _, r := range h.Repos {
			if other, ok := servedBy[r.Name]; ok {
				return nil, answers.A2AFile{}, fmt.Errorf("repo %s is on hosts %s and %s; A2A routes an unstarted issue's messages to the one host serving its repo, so map it on one", r.Name, other, h.Name)
			}
			servedBy[r.Name] = h.Name
			repos = append(repos, r.Name)
		}
		tok, err := keys.EnsureToken(st, keys.A2AHost(h.Name))
		if err != nil {
			return nil, answers.A2AFile{}, err
		}
		tokens[h.Name] = tok
		peers[h.Name] = answers.A2APeer{URL: fmt.Sprintf("http://%s:%d", saved.HostPart(h.SSH), a2aPort), Repos: repos}
	}
	accept := map[string]string{owner: "owner"}
	for name, tok := range tokens {
		accept[tok] = "host:" + name
	}
	per := map[string]answers.A2AFile{}
	for name, tok := range tokens {
		per[name] = answers.A2AFile{Name: name, Token: tok, Accept: accept, Hosts: peers}
	}
	return per, answers.A2AFile{Name: "owner", Token: owner, Hosts: peers}, nil
}

// writeDesktopA2A writes the file the desktop's flocc-a2a MCP server reads.
func writeDesktopA2A(home string, f answers.A2AFile) error {
	b, err := toml.Marshal(f)
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "flocc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "a2a.toml"), b, 0o600)
}
