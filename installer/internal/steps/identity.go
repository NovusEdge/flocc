package steps

import (
	"context"
	"path/filepath"

	"github.com/NovusEdge/flocc/installer/internal/identity"
)

// Identity gives the service user the owner's git author, so agent commits are the owner's.
type Identity struct{}

func (Identity) Name() string { return "Git identity" }

func (Identity) Check(ctx context.Context, e *Env) (bool, string, error) {
	want, err := identity.Load(filepath.Join(e.Stage, "identity.json"))
	if err != nil {
		return false, "", err
	}
	if want == (identity.Identity{}) {
		return true, "none shipped", nil
	}
	got := identity.Identity{
		Name:  e.Output(ctx, "git", "config", "--global", "user.name"),
		Email: e.Output(ctx, "git", "config", "--global", "user.email"),
	}
	if got != want {
		return false, "committing as " + got.Name + " <" + got.Email + ">, want " + want.Name + " <" + want.Email + ">", nil
	}
	return true, want.Name + " <" + want.Email + ">", nil
}

func (Identity) Apply(ctx context.Context, e *Env) error {
	want, err := identity.Load(filepath.Join(e.Stage, "identity.json"))
	if err != nil {
		return err
	}
	for key, val := range map[string]string{"user.name": want.Name, "user.email": want.Email} {
		if _, err := e.Must(ctx, "git", "config", "--global", key, val); err != nil {
			return err
		}
	}
	return nil
}
