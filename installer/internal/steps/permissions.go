package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Permissions writes the host file's [permissions] table to its own file. It is not part of
// config.toml because Config rewrites that only when it fails to load, so an edited table would
// never reach a working host.
type Permissions struct{}

const permissionsFile = ".config/flocc/permissions.toml"

func (Permissions) Name() string { return "Permissions" }

func (Permissions) want(e *Env) ([]byte, error) {
	if e.Answers.Permissions == nil {
		return nil, nil
	}
	return toml.Marshal(e.Answers.Permissions)
}

func (p Permissions) Check(ctx context.Context, e *Env) (bool, string, error) {
	want, err := p.want(e)
	if err != nil {
		return false, "", err
	}
	got, err := os.ReadFile(e.HomePath(permissionsFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, "", err
	}
	switch {
	case want == nil && got == nil:
		return true, "none in the host file", nil
	case want == nil:
		return false, "the host file has no [permissions]; the host's copy goes", nil
	case bytes.Equal(got, want):
		perms := e.Answers.Permissions
		return true, fmt.Sprintf("%s, %s", Plural(len(perms.Allow), "allow rule"), Plural(len(perms.Environment), "environment line")), nil
	}
	return false, "differs from the host file", nil
}

func (p Permissions) Apply(ctx context.Context, e *Env) error {
	want, err := p.want(e)
	if err != nil {
		return err
	}
	path := e.HomePath(permissionsFile)
	if want == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(e.HomePath(".config/flocc"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, want, 0o600)
}
