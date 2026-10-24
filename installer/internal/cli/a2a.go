package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
)

const a2aPort = 7443

// peerAddrs maps each tailnet node's lower-cased name to its IPv4, read from this desktop. A host's
// /etc/hosts maps its own name to 127.0.1.1 while its A2A server binds only the tailnet address, so
// a peer URL must carry the address. It is nil when tailscale status fails; a2aFiles then falls
// back to the name.
func peerAddrs(ctx context.Context, d *deps) map[string]string {
	out, err := host.Must(ctx, d.run, host.Cmd{Name: "tailscale", Args: []string{"status", "--json"}})
	if err != nil {
		return nil
	}
	devs, err := tailnet.Devices([]byte(out))
	if err != nil {
		return nil
	}
	ips := map[string]string{}
	for _, dev := range devs {
		if dev.IP != "" {
			ips[strings.ToLower(dev.Name)] = dev.IP
		}
	}
	return ips
}

// tokenID is the form accept lists a token in, so a host's file does not hold the owner's token in
// a form a session on the host could send.
func tokenID(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// a2aFiles builds each ssh host's a2a.toml and the desktop's. Daytona sandboxes are not on the
// tailnet, so they get none and no host lists them. A repo on two hosts is refused: a message to
// an issue no host has claimed goes to the one host serving its repo.
func a2aFiles(st keys.Store, all []hosts.Host, ips map[string]string) (map[string]answers.A2AFile, answers.A2AFile, error) {
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
		addr := saved.HostPart(h.SSH)
		if ip := ips[strings.ToLower(addr)]; ip != "" {
			addr = ip
		}
		peers[h.Name] = answers.A2APeer{URL: fmt.Sprintf("http://%s:%d", addr, a2aPort), Repos: repos}
	}
	accept := map[string]string{tokenID(owner): "owner"}
	for name, tok := range tokens {
		accept[tokenID(tok)] = "host:" + name
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
