// Package hosts is the desktop's list of flocc hosts: one <name>.toml per host in Dir.
package hosts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Host is one host file. It sets exactly one of SSH and Daytona.
type Host struct {
	Name    string `toml:"-"`
	SSH     string `toml:"ssh,omitempty"`
	Daytona string `toml:"daytona,omitempty"`
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

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("host name %q: start with a letter or digit, then use letters, digits, '.', '_' or '-'", name)
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
	h.Name = name
	switch {
	case (h.SSH == "") == (h.Daytona == ""):
		return Host{}, fmt.Errorf("%s.toml: set exactly one of ssh and daytona", name)
	case strings.HasPrefix(h.SSH, "-"):
		// ssh would read it as an option (-oProxyCommand=... runs a local command).
		return Host{}, fmt.Errorf("%s.toml: an ssh destination cannot start with '-'", name)
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := toml.Marshal(h)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, h.Name+".toml")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return err
	}
	// WriteFile applies the mode only when it creates the file.
	return os.Chmod(p, 0o600)
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
		if strings.HasPrefix(t, "-") {
			lines = append(lines, fmt.Sprintf("dropped %q: an ssh destination cannot start with '-'", t))
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
