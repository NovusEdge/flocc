// Package hosts is the desktop's list of flocc hosts: one <name>.toml per host in Dir.
package hosts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/pelletier/go-toml/v2"
)

// Host is one host file: how to reach the host, and the answers ship sends it, so the file is the
// only place they live. It sets exactly one of SSH and Daytona and never holds a secret: those
// come from the desktop's key store at ship time.
type Host struct {
	Name    string `toml:"-"`
	SSH     string `toml:"ssh,omitempty"`
	Daytona string `toml:"daytona,omitempty"`
	answers.Answers
}

func (h Host) Target() string {
	if h.Daytona != "" {
		return "daytona:" + h.Daytona
	}
	return h.SSH
}

func Dir(home string) string { return filepath.Join(home, ".config", "flocc", "hosts") }

// A host's name is also its file name.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var targetRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]*$`)

// ValidTarget reports whether t is safe as an ssh destination: no leading dash, which ssh would
// read as an option, and no shell metacharacters, since the target reaches remote command lines.
func ValidTarget(t string) bool { return targetRE.MatchString(t) }

func checkName(name string) error {
	switch {
	case !validName.MatchString(name):
		return fmt.Errorf("host name %q: start with a letter or digit, then use letters, digits, '.', '_' or '-'", name)
	case name == "all":
		return errors.New("host name all: -H all means every host, so no host can be called all")
	}
	return nil
}

func check(h Host) error {
	switch {
	case (h.SSH == "") == (h.Daytona == ""):
		return fmt.Errorf("%s.toml: set exactly one of ssh and daytona", h.Name)
	case !ValidTarget(h.Target()):
		return fmt.Errorf("%s.toml: %q is not an ssh destination or daytona sandbox", h.Name, h.Target())
	}
	return nil
}

func Load(dir, name string) (Host, error) {
	if err := checkName(name); err != nil {
		return Host{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".toml"))
	if err != nil {
		return Host{}, err
	}
	var h Host
	if err := toml.Unmarshal(b, &h); err != nil {
		return Host{}, fmt.Errorf("%s.toml: %w", name, err)
	}
	h.Name, h.Secrets = name, nil
	if err := check(h); err != nil {
		return Host{}, err
	}
	return h, nil
}

// List returns the hosts in dir sorted by name. A file that does not load is left out and named in
// the error, so one bad file hides no other host.
func List(dir string) ([]Host, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	var list []Host
	var errs []error
	for _, p := range paths {
		h, err := Load(dir, strings.TrimSuffix(filepath.Base(p), ".toml"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		list = append(list, h)
	}
	return list, errors.Join(errs...)
}

func Save(dir string, h Host) error {
	if err := checkName(h.Name); err != nil {
		return err
	}
	if err := check(h); err != nil {
		return err
	}
	h.Secrets = nil
	b, err := toml.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Written beside it and renamed: List reads *.toml, so it never sees a half-written host.
	// CreateTemp makes the file mode 600.
	tmp, err := os.CreateTemp(dir, ".host-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, h.Name+".toml"))
}

// ConvertOld turns the pre-flocc hosts file, one ssh destination per line, into host files in
// dir, then renames it to <oldFile>.converted so it is converted once. A host is named by its
// destination without user@ and from the first dot on. It returns the notice for the user, or ""
// when there is no old file.
func ConvertOld(oldFile, dir string) (string, error) {
	b, err := os.ReadFile(oldFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if !ValidTarget(t) {
			lines = append(lines, fmt.Sprintf("dropped %q: not an ssh destination", t))
			continue
		}
		name, _, _ := strings.Cut(t[strings.LastIndex(t, "@")+1:], ".")
		if checkName(name) != nil {
			lines = append(lines, fmt.Sprintf("skipped %q: %q cannot be a host name", t, name))
			continue
		}
		p := filepath.Join(dir, name+".toml")
		if _, err := os.Stat(p); err == nil {
			lines = append(lines, fmt.Sprintf("skipped %q: %s exists", t, p))
			continue
		}
		if err := Save(dir, Host{Name: name, SSH: t}); err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("%s -> %s", t, p))
	}
	if err := os.Rename(oldFile, oldFile+".converted"); err != nil {
		return "", err
	}
	return fmt.Sprintf("converted %s (kept as %s.converted) into %s:\n  %s", oldFile, oldFile, dir, strings.Join(lines, "\n  ")), nil
}
