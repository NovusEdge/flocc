package host

import (
	"fmt"
	"strings"
)

type Manager string

const (
	Apt    Manager = "apt"
	Pacman Manager = "pacman"
	Dnf    Manager = "dnf"
)

type Distro struct {
	ID      string
	Like    []string
	Manager Manager
}

func ParseOSRelease(text string) (Distro, error) {
	kv := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			kv[k] = strings.Trim(v, `"'`)
		}
	}
	d := Distro{ID: kv["ID"], Like: strings.Fields(kv["ID_LIKE"])}
	for _, id := range append([]string{d.ID}, d.Like...) {
		switch id {
		case "debian", "ubuntu":
			d.Manager = Apt
		case "arch":
			d.Manager = Pacman
		case "fedora", "rhel", "centos":
			d.Manager = Dnf
		}
		if d.Manager != "" {
			return d, nil
		}
	}
	return d, fmt.Errorf("unsupported distribution %q (ID_LIKE %q): needs apt, pacman or dnf", d.ID, kv["ID_LIKE"])
}

// NeedsGHRepo: RHEL and its rebuilds have no gh package; Fedora does.
func (d Distro) NeedsGHRepo() bool { return d.Manager == Dnf && d.ID != "fedora" }
