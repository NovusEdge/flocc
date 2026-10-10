package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func bundleHead(ctx context.Context, e *Env, bundle string) (string, error) {
	out, err := e.Must(ctx, "git", "bundle", "list-heads", bundle)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if sha, ref, ok := strings.Cut(strings.TrimSpace(line), " "); ok && ref == "HEAD" {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%s has no HEAD", bundle)
}

// syncFromBundle checks out the bundle's HEAD in dir. checkout refuses to overwrite local
// edits, so a hand-edited host checkout stops the run instead of losing them.
func syncFromBundle(ctx context.Context, e *Env, bundle, dir string) error {
	if _, err := os.Stat(filepath.Join(e.Root, dir, ".git")); err != nil {
		if _, err := e.Must(ctx, "git", "init", "-q", dir); err != nil {
			return err
		}
	}
	if _, err := e.Must(ctx, "git", "-C", dir, "fetch", "-q", bundle, "HEAD"); err != nil {
		return err
	}
	_, err := e.Must(ctx, "git", "-C", dir, "checkout", "-q", "--detach", "FETCH_HEAD")
	return err
}
