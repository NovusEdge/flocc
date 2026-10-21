// Package saved is the desktop's record of each cardinal install, ~/.config/flocc/cardinal/<user>.toml,
// and the rules for reaching and reading an install that the fleet view shares with flocc cardinal.
package saved

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

// Saved holds no secret: the key is cardinal's public half.
type Saved struct {
	Machine   string   `toml:"machine"`
	User      string   `toml:"user"`
	Hosts     []string `toml:"hosts"`
	Key       string   `toml:"key,omitempty"`
	RenewHour int      `toml:"renew_hour"`
	Removed   bool     `toml:"removed,omitempty"`
}

const DefaultUser = "cardinal"

var userName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func ValidUser(u string) bool { return userName.MatchString(u) }

func Dir(home string) string { return filepath.Join(home, ".config", "flocc", "cardinal") }

func path(dir, user string) string { return filepath.Join(dir, user+".toml") }

// Load returns os.ErrNotExist when no install of user is saved.
func Load(dir, user string) (Saved, error) {
	if !ValidUser(user) {
		return Saved{}, fmt.Errorf("%q is not a user name", user)
	}
	b, err := os.ReadFile(path(dir, user))
	if err != nil {
		return Saved{}, err
	}
	s := Saved{RenewHour: 4}
	if err := toml.Unmarshal(b, &s); err != nil {
		return Saved{}, fmt.Errorf("%s: %w", path(dir, user), err)
	}
	if s.User != user {
		return Saved{}, fmt.Errorf("%s names user %q", path(dir, user), s.User)
	}
	return s, nil
}

func (s Saved) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := toml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path(dir, s.User), b, 0o600)
}

func Delete(dir, user string) error {
	if err := os.Remove(path(dir, user)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List returns every saved install sorted by user; a file that does not load is named in the error.
func List(dir string) ([]Saved, error) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	sort.Strings(paths)
	var out []Saved
	var errs []error
	for _, p := range paths {
		s, err := Load(dir, strings.TrimSuffix(filepath.Base(p), ".toml"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, s)
	}
	return out, errors.Join(errs...)
}

// HostPart is machine without a user part: the owner's destination may be owner@host, and the
// cardinal user's is <user>@host.
func HostPart(machine string) string {
	if i := strings.LastIndex(machine, "@"); i >= 0 {
		return machine[i+1:]
	}
	return machine
}

// Pinned go on every ssh to the cardinal user: an agent forwarded during attach --write would let
// cardinal log in anywhere the owner's key works, whatever a later ~/.ssh/config says.
var Pinned = []string{"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes"}

func (s Saved) Dest() string { return s.User + "@" + HostPart(s.Machine) }

// maxField bounds each string cardinal's binaries print; cardinal can rewrite them.
const maxField = 200

// Sanitize makes text from cardinal's binaries safe to print: no control characters, so no escape
// sequence reaches the terminal, and no field longer than maxField runes.
func Sanitize(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxField {
			b.WriteString("…")
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// SanitizeBlock keeps newlines, for multi-line fields such as the pane and the journal.
func SanitizeBlock(s string, lines int) string {
	parts := strings.Split(s, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	for i, p := range parts {
		parts[i] = Sanitize(p)
	}
	return strings.Join(parts, "\n")
}
