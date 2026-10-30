package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/deploy"
)

const sudoersFile = "/etc/sudoers.d/flocc-agent"

// agentDirs are what flocc reads through flocc-work: the worktrees, the clones they share objects
// with, and each issue's session files.
var agentDirs = []string{"worktrees", "src", "state"}

func inGroup(ctx context.Context, e *Env, user, group string) bool {
	return slices.Contains(strings.Fields(e.Output(ctx, "id", "-nG", user)), group)
}

// AgentAccount is flocc-agent: it runs the sessions and the repos' code, and can read nothing of
// flocc's. flocc reads flocc-agent's worktrees and state through the group flocc-work. Any group
// in PrivilegedGroups, docker included, would give the agent root and with it flocc's secrets.
type AgentAccount struct{}

func (AgentAccount) Name() string { return "Agent user" }

// jscpd:ignore-start
func (AgentAccount) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.Ok(ctx, "id", "-u", AgentUser) {
		return false, "no " + AgentUser + " user", nil
	}
	// jscpd:ignore-end
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", AgentUser)) {
		if slices.Contains(PrivilegedGroups, g) || g == ServiceUser || g == WorkGroup {
			return false, AgentUser + " is in group " + g, nil
		}
	}
	if !inGroup(ctx, e, ServiceUser, WorkGroup) {
		return false, ServiceUser + " is not in " + WorkGroup, nil
	}
	st, err := os.Stat(e.Path(AgentHome))
	if err != nil || st.Mode().Perm() != 0o710 {
		return false, AgentHome + " is not mode 710", nil
	}
	for _, d := range agentDirs {
		st, err := os.Stat(e.Path(filepath.Join(AgentHome, d)))
		if err != nil || st.Mode().Perm() != 0o750 || st.Mode()&fs.ModeSetgid == 0 {
			return false, AgentHome + "/" + d + " is not mode 2750", nil
		}
	}
	if e.Mode == Systemd && e.Output(ctx, "loginctl", "show-user", AgentUser, "-p", "Linger", "--value") != "yes" {
		return false, "lingering off for " + AgentUser, nil
	}
	return true, AgentUser + ", home 710, " + ServiceUser + " reads through " + WorkGroup, nil
}

func (AgentAccount) Apply(ctx context.Context, e *Env) error {
	if _, err := e.Must(ctx, "groupadd", "-f", WorkGroup); err != nil {
		return err
	}
	if !e.Ok(ctx, "id", "-u", AgentUser) {
		if _, err := e.Must(ctx, "useradd", "--create-home", "--shell", "/bin/bash", AgentUser); err != nil {
			return err
		}
	}
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", AgentUser)) {
		if slices.Contains(PrivilegedGroups, g) || g == ServiceUser || g == WorkGroup {
			return fmt.Errorf("%s is in group %s, which reaches past its sessions; remove it with gpasswd -d %s %s and run again", AgentUser, g, AgentUser, g)
		}
	}
	added := !inGroup(ctx, e, ServiceUser, WorkGroup)
	cmds := [][]string{
		{"usermod", "-aG", WorkGroup, ServiceUser},
		{"chgrp", WorkGroup, AgentHome},
		{"chmod", "710", AgentHome},
	}
	for _, d := range agentDirs {
		dir := filepath.Join(AgentHome, d)
		// install -d leaves an existing directory's owner and mode as they were.
		cmds = append(cmds,
			[]string{"install", "-d", "-o", AgentUser, "-g", WorkGroup, "-m", "2750", dir},
			[]string{"chown", AgentUser + ":" + WorkGroup, dir},
			[]string{"chmod", "2750", dir})
	}
	if e.Mode == Systemd {
		cmds = append(cmds, []string{"loginctl", "enable-linger", AgentUser})
	}
	for _, c := range cmds {
		if _, err := e.Must(ctx, c[0], c[1:]...); err != nil {
			return err
		}
	}
	if added && e.Mode == Systemd {
		// A running user manager keeps the groups it started with; until it restarts, the ticks
		// it starts cannot read the worktrees.
		uid := e.Output(ctx, "id", "-u", ServiceUser)
		if _, err := e.Must(ctx, "systemctl", "try-restart", "user@"+uid+".service"); err != nil {
			return err
		}
	}
	if added && e.Mode == Loop {
		// Same for the tmux server the loop's ticks run in; the service phase starts a fresh one.
		// Nothing to kill when none runs, so a failure here is not one.
		e.Ok(ctx, "runuser", "-u", ServiceUser, "--", "tmux", "-L", "flocc", "kill-server")
	}
	return nil
}

// AgentAccess installs the wrapper, the one sudoers rule that reaches it, Claude Code's managed
// settings (the guard hook and the dialog denies, which flocc-agent cannot turn off), and
// /opt/flocc, where the service publishes the plugin and the flocc build flocc-agent runs.
type AgentAccess struct{}

func (AgentAccess) Name() string { return "Agent access" }

var accessFiles = []struct {
	path, src string
	mode      os.FileMode
}{
	{AgentRun, "flocc-agent-run", 0o755},
	{sudoersFile, "flocc-agent.sudoers", 0o440},
	{ManagedSettingsFile, "managed-settings.json", 0o644},
}

func (AgentAccess) Check(ctx context.Context, e *Env) (bool, string, error) {
	for _, f := range accessFiles {
		want, _ := deploy.Files.ReadFile(f.src)
		got, err := os.ReadFile(e.Path(f.path))
		if err != nil || !bytes.Equal(got, want) {
			return false, f.path + " not installed or out of date", nil
		}
		if st, err := os.Stat(e.Path(f.path)); err != nil || st.Mode().Perm() != f.mode {
			return false, fmt.Sprintf("%s is not mode %o", f.path, f.mode), nil
		}
	}
	if st, err := os.Stat(e.Path(OptDir)); err != nil || !st.IsDir() {
		return false, "no " + OptDir, nil
	}
	return true, "wrapper, sudoers rule, " + OptDir, nil
}

func (AgentAccess) Apply(ctx context.Context, e *Env) error {
	run, _ := deploy.Files.ReadFile("flocc-agent-run")
	if err := os.MkdirAll(e.Path(filepath.Dir(AgentRun)), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(e.Path(AgentRun), run, 0o755); err != nil {
		return err
	}
	rule, _ := deploy.Files.ReadFile("flocc-agent.sudoers")
	if err := os.MkdirAll(e.Path(filepath.Dir(sudoersFile)), 0o750); err != nil {
		return err
	}
	// sudo skips files in sudoers.d whose names hold a dot, so the candidate is inert until the
	// rename. A rule sudo cannot parse breaks every sudo on the machine, the owner's included.
	tmp := sudoersFile + ".new"
	if err := os.WriteFile(e.Path(tmp), rule, 0o440); err != nil {
		return err
	}
	if _, err := e.Must(ctx, "visudo", "-cf", tmp); err != nil {
		return errors.Join(err, os.Remove(e.Path(tmp)))
	}
	if err := os.Rename(e.Path(tmp), e.Path(sudoersFile)); err != nil {
		return err
	}
	managed, _ := deploy.Files.ReadFile("managed-settings.json")
	if err := os.MkdirAll(e.Path(filepath.Dir(ManagedSettingsFile)), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(e.Path(ManagedSettingsFile), managed, 0o644); err != nil {
		return err
	}
	_, err := e.Must(ctx, "install", "-d", "-o", ServiceUser, "-g", ServiceUser, "-m", "755", OptDir)
	return err
}

// ClaudeHome moves a one-user host's Claude login and Remote Control consent to flocc-agent,
// whose sessions use them now.
type ClaudeHome struct{}

func (ClaudeHome) Name() string { return "Claude login owner" }

var claudeHomeFiles = []string{".claude", ".claude.json"}

func (ClaudeHome) Check(ctx context.Context, e *Env) (bool, string, error) {
	for _, f := range claudeHomeFiles {
		if _, err := os.Lstat(e.Path(filepath.Join("/home", ServiceUser, f))); err == nil {
			return false, "/home/" + ServiceUser + "/" + f + " is still " + ServiceUser + "'s", nil
		}
	}
	return true, "the login is " + AgentUser + "'s", nil
}

func (ClaudeHome) Apply(ctx context.Context, e *Env) error {
	for _, f := range claudeHomeFiles {
		from, to := filepath.Join("/home", ServiceUser, f), filepath.Join(AgentHome, f)
		st, err := os.Lstat(e.Path(from))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		// Root acts inside the service home here; a link could point anywhere.
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; remove it by hand and run again", from)
		}
		if _, err := os.Lstat(e.Path(to)); err == nil {
			// flocc-agent already has its own; the service's copy is a stale login.
			if _, err := e.Must(ctx, "rm", "-rf", "--", from); err != nil {
				return err
			}
			continue
		}
		for _, c := range [][]string{{"mv", "--", from, to}, {"chown", "-R", "-h", AgentUser + ":" + AgentUser, to}} {
			if _, err := e.Must(ctx, c[0], c[1:]...); err != nil {
				return err
			}
		}
		if f == ".claude" {
			// These hold absolute /home/flocc paths that flocc-agent cannot read, yet the
			// Claude step would count the plugins and marketplaces as installed.
			rm := []string{"rm", "-rf", "--"}
			for _, p := range stalePluginPaths {
				rm = append(rm, filepath.Join(to, p))
			}
			if _, err := e.Must(ctx, rm[0], rm[1:]...); err != nil {
				return err
			}
		}
	}
	return nil
}

var stalePluginPaths = []string{
	"plugins/installed_plugins.json", "plugins/known_marketplaces.json", "plugins/cache", "plugins/marketplaces",
}
