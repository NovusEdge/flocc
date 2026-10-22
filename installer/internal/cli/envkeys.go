package cli

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/keys"
)

// envKeys takes the keys of slots that the environment or the .env file holds, and offers to push
// the ones that replaced a stored key to every host. A key it cannot take is left to the prompt
// that follows.
func envKeys(ctx context.Context, d *deps, slots []keys.Slot) error {
	root, err := d.repoRoot()
	if err != nil {
		root = ""
	}
	vals := keys.FromEnv(root, d.getenv, notes{d})
	if len(vals) == 0 {
		return nil
	}
	all := listHosts(d)
	changed, err := keys.Take(ctx, keysEnv(d), mapped(all), vals, slots)
	if len(changed) == 0 || len(all) == 0 {
		return err
	}
	var titles, names []string
	for _, s := range changed {
		titles = append(titles, s.Title())
	}
	for _, h := range all {
		names = append(names, h.Name)
	}
	push, perr := d.prompt.Confirm("Push the changed "+strings.Join(titles, ", ")+" to "+strings.Join(names, ", ")+"?", true)
	if perr == nil && push {
		pushToHosts(ctx, d, all, changed, "run flocc ship -H <host> to write the new keys")
	}
	if err == nil {
		err = perr
	}
	return err
}

// seedEnv makes .env from .env.example in a flocc checkout that has none, so the owner finds the
// file to fill in.
func seedEnv(d *deps, root string) {
	path := filepath.Join(root, ".env")
	made, err := keys.SeedEnv(root)
	switch {
	case err != nil:
		d.ui.Note(".env", "could not make "+path+" from .env.example: "+err.Error())
	case made:
		d.ui.Note(".env", "made "+path+" from .env.example, mode 600; put the keys you have there and flocc takes them before it asks")
	}
}
