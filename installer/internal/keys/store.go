// Package keys holds the owner's keys on the desktop and the flow that makes and checks them.
package keys

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

type Slot string

const (
	Linear  Slot = "linear_key"
	Agent   Slot = "agent_token"
	Publish Slot = "publish_token"
	Ntfy    Slot = "ntfy_topic"

	// Tailscale holds "id:secret" of an OAuth client. It is optional, so it is not in Slots: ship
	// and the answers document would refuse a desktop that never made one.
	Tailscale Slot = "tailscale_oauth"
)

// Slots is the required keys in fill order: the publish token's page opens only after the agent
// token passes.
var Slots = []Slot{Linear, Agent, Publish, Ntfy}

// fillOrder is Slots with the optional slot, which Fill asks for only when the caller names it.
var fillOrder = []Slot{Linear, Agent, Publish, Tailscale, Ntfy}

func (s Slot) Title() string {
	switch s {
	case Linear:
		return "Linear API key"
	case Agent:
		return "GitHub agent token (read-only)"
	case Publish:
		return "GitHub publish token"
	case Tailscale:
		return "Tailscale OAuth client"
	}
	return "ntfy topic"
}

type Store interface {
	Get(Slot) (string, error)
	Set(Slot, string) error
	Where() string
}

const service = "flocc"

var (
	// keyringGet is a var so a test can make the keyring hang.
	keyringGet = keyring.Get
	probeWait  = 2 * time.Second
	probeLimit = 30 * time.Second
	opLimit    = time.Minute

	errKeyringTimeout = errors.New("the OS keyring did not answer; unlock it and run again")
)

// Open picks the store. An existing secrets.toml wins, so keys never split between two stores;
// otherwise the OS keyring when it answers; otherwise a new secrets.toml.
func Open(home string, notify io.Writer) Store {
	path := filepath.Join(home, ".config", "flocc", "secrets.toml")
	if st, err := os.Stat(path); err == nil {
		restrictMode(path, st.Mode().Perm(), notify)
		return fileStore{path: path}
	}
	err := probe(notify)
	if err == nil {
		return ringStore{}
	}
	fmt.Fprintf(notify, "No OS keyring answered (%v); keys go in %s, mode 600.\n", err, path)
	return fileStore{path: path}
}

// probe reads a name nobody stores. A locked Secret Service shows an unlock prompt and blocks
// until it is answered, so the owner is told after probeWait and the probe gives up at probeLimit.
func probe(notify io.Writer) error {
	done := make(chan error, 1)
	get := keyringGet // read here: the goroutine may outlive a test that swaps the var back
	go func() {
		_, err := get(service, "probe")
		if errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(probeWait):
		fmt.Fprintln(notify, "Waiting for the OS keyring; unlock it if it asks.")
	}
	select {
	case err := <-done:
		return err
	case <-time.After(probeLimit - probeWait):
		return errKeyringTimeout
	}
}

type ringStore struct{}

func (ringStore) Where() string { return "the OS keyring" }

func (ringStore) Get(s Slot) (string, error) {
	v, err := limited(func() (string, error) { return keyringGet(service, string(s)) })
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func (ringStore) Set(s Slot, v string) error {
	_, err := limited(func() (string, error) { return "", keyring.Set(service, string(s), v) })
	return err
}

func limited(f func() (string, error)) (string, error) {
	type result struct {
		v   string
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := f()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-time.After(opLimit):
		return "", errKeyringTimeout
	}
}

type fileStore struct{ path string }

func (f fileStore) Where() string { return f.path }

func (f fileStore) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, toml.Unmarshal(b, &m)
}

func (f fileStore) Get(s Slot) (string, error) {
	m, err := f.load()
	return m[string(s)], err
}

func (f fileStore) Set(s Slot, v string) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	m[string(s)] = v
	b, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	// Written beside it and renamed, so a failed write never leaves half the keys.
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".secrets-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

// CheckPrefix refuses a paste that is plainly the wrong kind of key, before any network call.
// The two GitHub tokens share a prefix; the live check tells them apart.
func CheckPrefix(s Slot, v string) error {
	switch {
	case strings.HasPrefix(v, "lin_api_") && s != Linear:
		return fmt.Errorf("that is a Linear API key; this is the %s", s.Title())
	case strings.HasPrefix(v, "github_pat_") && s == Linear:
		return errors.New("that is a GitHub token; this is the Linear API key")
	case strings.HasPrefix(v, "ghp_") || strings.HasPrefix(v, "gho_"):
		return errors.New("that is a classic GitHub token; make a fine-grained one")
	case s == Linear && !strings.HasPrefix(v, "lin_api_"):
		return errors.New("a Linear personal API key starts with lin_api_")
	case (s == Agent || s == Publish) && !strings.HasPrefix(v, "github_pat_"):
		return errors.New("a fine-grained GitHub token starts with github_pat_")
	case s == Tailscale && !validClient(v):
		return errors.New("paste the client secret, which starts with " + clientPrefix)
	}
	return nil
}

const clientPrefix = "tskey-client-"

// SplitClient cuts a Tailscale paste into the OAuth client's ID and secret. The secret alone is
// enough: the token endpoint ignores the ID, and tailscale up's own OAuth path sends a placeholder.
// id:secret, the form flocc asked for first, still works.
func SplitClient(v string) (id, secret string, ok bool) {
	if strings.HasPrefix(v, clientPrefix) {
		return "flocc", v, len(v) > len(clientPrefix)
	}
	id, secret, ok = strings.Cut(v, ":")
	return id, secret, ok && id != "" && secret != ""
}

// knownPrefixes are the public parts of the keys flocc takes, which say what kind of key it is.
var knownPrefixes = []string{"lin_api_", "github_pat_", clientPrefix, "ghp_", "gho_"}

// Masked describes a pasted key without what makes it usable: its kind's public prefix, its last
// four characters, and its length. A paste with no known prefix may be some other secret, such as
// a password, so only its length is shown.
func Masked(v string) string {
	for _, p := range knownPrefixes {
		if strings.HasPrefix(v, p) {
			if len(v)-len(p) < 12 {
				return fmt.Sprintf("%s…, %d characters", p, len(v))
			}
			return fmt.Sprintf("%s…%s, %d characters", p, v[len(v)-4:], len(v))
		}
	}
	return fmt.Sprintf("%d characters, no known prefix", len(v))
}

func validClient(v string) bool { _, _, ok := SplitClient(v); return ok }
