// Package identity carries the owner's git author from the desktop to the host.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// FromRepo reads the author git would use for commits in dir.
func FromRepo(ctx context.Context, r host.Runner, dir string) (Identity, error) {
	var id Identity
	for key, dst := range map[string]*string{"user.name": &id.Name, "user.email": &id.Email} {
		res, err := r.Run(ctx, host.Cmd{Name: "git", Args: []string{"-C", dir, "config", key}})
		if err != nil {
			return id, err
		}
		*dst = strings.TrimSpace(res.Stdout)
	}
	if id.Name == "" || id.Email == "" {
		return id, fmt.Errorf("git user.name and user.email are not both set in %s; agents commit as you, so set them first", dir)
	}
	return id, nil
}

// Load returns the zero Identity when the stage has none.
func Load(path string) (Identity, error) {
	var id Identity
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return id, nil
	}
	if err != nil {
		return id, err
	}
	return id, json.Unmarshal(b, &id)
}

func (id Identity) Write(path string) error {
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
