package steps

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
)

const ServiceUser = "linear-agents"

var privileged = []string{"sudo", "wheel", "admin"}

type User struct{}

func (User) Name() string { return "Service user" }

func privilegedGroup(ctx context.Context, e *Env) string {
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", ServiceUser)) {
		if slices.Contains(privileged, g) {
			return g
		}
	}
	return ""
}

func (User) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.Ok(ctx, "id", "-u", ServiceUser) {
		return false, "no " + ServiceUser + " user", nil
	}
	st, err := os.Stat(e.Path("/home/" + ServiceUser))
	if err != nil {
		return false, "no home directory", nil
	}
	if p := st.Mode().Perm(); p != 0o700 {
		return false, fmt.Sprintf("home is mode %o", p), nil
	}
	if g := privilegedGroup(ctx, e); g != "" {
		return false, "in group " + g, nil
	}
	if e.Mode == Systemd && e.Output(ctx, "loginctl", "show-user", ServiceUser, "-p", "Linger", "--value") != "yes" {
		return false, "lingering off", nil
	}
	return true, "exists, home 700, no sudo", nil
}

func (User) Apply(ctx context.Context, e *Env) error {
	if !e.Ok(ctx, "id", "-u", ServiceUser) {
		if _, err := e.Must(ctx, "useradd", "--create-home", "--shell", "/bin/bash", ServiceUser); err != nil {
			return err
		}
	}
	if _, err := e.Must(ctx, "chmod", "700", "/home/"+ServiceUser); err != nil {
		return err
	}
	if g := privilegedGroup(ctx, e); g != "" {
		return fmt.Errorf("%s is in group %s, which gives agents sudo; remove it with gpasswd -d %s %s and run again", ServiceUser, g, ServiceUser, g)
	}
	if e.Mode == Systemd {
		if _, err := e.Must(ctx, "loginctl", "enable-linger", ServiceUser); err != nil {
			return err
		}
	}
	return nil
}
