package steps

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// OldWorktrees removes ~/worktrees from a one-user host once nothing is claimed. Its worktrees are
// git worktrees of the service's clones, which the service now only fetches into.
type OldWorktrees struct{}

func (OldWorktrees) Name() string { return "Old worktrees" }

func (OldWorktrees) Check(ctx context.Context, e *Env) (bool, string, error) {
	if _, err := os.Stat(e.HomePath(worktreesRel)); errors.Is(err, fs.ErrNotExist) {
		return true, "none in " + e.Real(worktreesRel), nil
	}
	return false, e.Real(worktreesRel) + " is from the one-user layout", nil
}

func (OldWorktrees) Apply(ctx context.Context, e *Env) error {
	st, err := readStatus(ctx, e)
	if err != nil {
		return fmt.Errorf("flocc status: %w", err)
	}
	if len(st.Claims) > 0 {
		var keys []string
		for _, c := range st.Claims {
			keys = append(keys, c.Issue)
		}
		return fmt.Errorf("%s still claimed; release them with flocc release, then run flocc ship again", strings.Join(keys, ", "))
	}
	if err := os.RemoveAll(e.HomePath(worktreesRel)); err != nil {
		return err
	}
	clones, _ := filepath.Glob(e.HomePath("src/*"))
	for _, c := range clones {
		real := e.Real(filepath.Join("src", filepath.Base(c)))
		e.Run.Run(ctx, host.Cmd{Name: "git", Args: []string{"-C", real, "worktree", "prune"}})
	}
	return nil
}
