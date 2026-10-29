package keys

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Taken is a key read from the environment or the .env file, and which of the two it came from.
type Taken struct{ Value, From string }

const (
	fromEnv     = "the environment"
	fromFile    = ".env"
	floccModule = "module github.com/NovusEdge/flocc"
)

var envNames = map[Slot]string{
	Linear:    "FLOCC_LINEAR_API_KEY",
	Agent:     "FLOCC_GITHUB_AGENT_TOKEN",
	Publish:   "FLOCC_GITHUB_PUBLISH_TOKEN",
	Tailscale: "FLOCC_TAILSCALE_OAUTH_SECRET",
}

const tailscaleClientID = "FLOCC_TAILSCALE_OAUTH_CLIENT_ID"

// FromEnv reads each key's variable from the process environment, falling back to the .env file at
// root when root is a flocc checkout. An empty value counts as unset. The file's mode is tightened
// first, with a note on notify.
func FromEnv(root string, getenv func(string) string, notify io.Writer) map[Slot]Taken {
	var file map[string]string
	if isFloccCheckout(root) {
		path := filepath.Join(root, ".env")
		if st, err := os.Stat(path); err == nil {
			restrictMode(path, st.Mode().Perm(), notify)
			if b, err := os.ReadFile(path); err == nil {
				file = parseEnv(b)
			}
		}
	}
	lookup := func(name string) (Taken, bool) {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return Taken{v, fromEnv}, true
		}
		v, ok := file[name]
		return Taken{v, fromFile}, ok && v != ""
	}
	got := map[Slot]Taken{}
	for s, name := range envNames {
		if t, ok := lookup(name); ok {
			got[s] = t
		}
	}
	if secret, ok := got[Tailscale]; ok {
		if id, ok := lookup(tailscaleClientID); ok {
			secret.Value = id.Value + ":" + secret.Value
			if id.From == fromEnv {
				secret.From = fromEnv
			}
			got[Tailscale] = secret
		}
	}
	return got
}

// parseEnv reads KEY=VALUE lines. A line that is no pair is skipped without a word: it may hold a
// secret.
func parseEnv(b []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, v, ok := strings.Cut(line, "=")
		name, v = strings.TrimSpace(name), strings.TrimSpace(v)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		if n := len(v); n >= 2 && (v[0] == '"' || v[0] == '\'') && v[n-1] == v[0] {
			v = v[1 : n-1]
		}
		m[name] = v
	}
	return m
}

func isFloccCheckout(root string) bool {
	if root == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == floccModule {
			return true
		}
	}
	return false
}

// SeedEnv copies .env.example to .env in a flocc checkout that has no .env, and reports whether it
// did. The file is created mode 600 and an existing .env is never touched.
func SeedEnv(root string) (bool, error) {
	if !isFloccCheckout(root) {
		return false, nil
	}
	b, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".env"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err == nil, err
}

// restrictMode sets a file that holds keys to 600 when group or others can read it.
func restrictMode(path string, perm os.FileMode, notify io.Writer) {
	if perm&0o077 == 0 {
		return
	}
	if err := os.Chmod(path, 0o600); err != nil {
		fmt.Fprintf(notify, "%s is mode %o and could not be set to 600: %v\n", path, perm, err)
	} else {
		fmt.Fprintf(notify, "%s was mode %o; set it to 600.\n", path, perm)
	}
}
