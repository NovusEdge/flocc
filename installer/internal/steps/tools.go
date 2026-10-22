package steps

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// Tools sets the host file's mise tools as the service user's global versions, so a repo's
// ./setup finds them through mise's shims without pinning them itself. NF-211, NF-212 and NF-260
// failed on "No version is set for shim: go" with go installed under mise and no global version.
type Tools struct{}

const miseRel = ".local/bin/mise"

func (Tools) Name() string { return "Tools" }

// unset lists "name version" for each tool whose global version is not the host file's. mise
// current prints nothing, and exits 0, for a tool with no version set, and prints the resolved
// version for a prefix: "1.27" reads back as "1.27.1".
func (Tools) unset(ctx context.Context, e *Env) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(e.Answers.Tools)) {
		want := e.Answers.Tools[name]
		r, err := e.Run.Run(ctx, host.Cmd{Name: e.Real(miseRel), Args: []string{"current", name}, Dir: e.Home})
		got := strings.TrimSpace(r.Stdout)
		if err != nil || r.Code != 0 || (got != want && !strings.HasPrefix(got, want+".")) {
			out = append(out, name+" "+want)
		}
	}
	return out
}

func (t Tools) Check(ctx context.Context, e *Env) (bool, string, error) {
	if len(e.Answers.Tools) == 0 {
		return true, "none in the host file", nil
	}
	if _, err := os.Stat(e.HomePath(miseRel)); err != nil {
		return false, "mise is not installed for the service user", nil
	}
	if u := t.unset(ctx, e); len(u) > 0 {
		return false, "not set: " + strings.Join(u, ", "), nil
	}
	return true, Plural(len(e.Answers.Tools), "tool"), nil
}

func (t Tools) Apply(ctx context.Context, e *Env) error {
	if _, err := os.Stat(e.HomePath(miseRel)); err != nil {
		return errors.New("mise is not installed for the service user; install it with curl https://mise.run | sh as that user")
	}
	for _, nv := range t.unset(ctx, e) {
		name, version, _ := strings.Cut(nv, " ")
		c := host.Cmd{Name: e.Real(miseRel), Args: []string{"use", "-g", fmt.Sprintf("%s@%s", name, version)}, Dir: e.Home}
		if err := e.SpinCmd(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
