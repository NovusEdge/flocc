// Package ship stages the installer, bundles and plugin list and runs it on a host.
package ship

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	answersPkg "github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/identity"
)

func archFor(unameM string) (string, error) {
	switch strings.TrimSpace(unameM) {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported architecture %q", strings.TrimSpace(unameM))
}

const (
	resetStage = "sudo rm -rf /tmp/flocc-setup && mkdir -p /tmp/flocc-setup"
	// Without a terminal sudo must fail rather than wait for a password.
	resetStageNoPrompt = "sudo -n rm -rf /tmp/flocc-setup && mkdir -p /tmp/flocc-setup"
)

// Options are what Ship needs besides the stage and the answers.
type Options struct {
	// Target is the ssh target or daytona:<sandbox>, as the owner names the host.
	Target string
	// Interactive runs the host's forms on this terminal over ssh -t. Otherwise every answer,
	// secrets included, comes from the answers reader on stdin and nothing prompts.
	Interactive bool
	// TTY is stdin being a terminal, where Connect can show a Tailscale approval prompt.
	TTY bool
	// Out receives the host's output, less the summary line; nil is stdout.
	Out io.Writer
}

type shipOpts struct {
	// Target is the name the owner typed, for the login command the summary shows. Never
	// Dest.SSH, which holds the Daytona token.
	Target      string
	Interactive bool
	TTY         bool
	Answers     []byte
	Out         io.Writer
}

// usageError is a refusal of the arguments, which exits 2.
type usageError string

func (u usageError) Error() string { return string(u) }

// ValidTarget is hosts.ValidTarget: ship and the host files take the same targets.
func ValidTarget(t string) bool { return hosts.ValidTarget(t) }

func badTarget(t string) error {
	return usageError(fmt.Sprintf("%q is not an ssh target or daytona:<sandbox>", t))
}

func sshCmd(d Dest, tty bool, remote string) host.Cmd {
	args := append([]string{}, d.Opts...)
	if tty {
		args = append(args, "-t")
	}
	return host.Cmd{Name: "ssh", Args: append(args, d.SSH, remote)}
}

// sshNoTTY is sshCmd(d, false, …) with -T. A RequestTTY setting in the owner's ssh config would
// otherwise give the run a pty, which echoes the answers document back and never passes EOF.
func sshNoTTY(d Dest, remote string) host.Cmd {
	c := sshCmd(d, false, remote)
	c.Args = slices.Insert(c.Args, len(d.Opts), "-T")
	return c
}

// connect resolves target to an ssh destination and a runner whose log hides its token and
// secrets.
func connect(ctx context.Context, target string, secrets ...string) (host.Runner, Dest, error) {
	dest, err := Resolve(ctx, target)
	if err != nil {
		return nil, Dest{}, err
	}
	return host.Exec{Log: host.Redact(os.Stderr, append(secrets, dest.Token)...)}, dest, nil
}

func repoRoot(ctx context.Context, r host.Runner) (string, error) {
	out, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"rev-parse", "--show-toplevel"}})
	return strings.TrimSpace(out), err
}

func ghPrivate(repo string) (bool, error) {
	out, err := exec.Command("gh", "repo", "view", repo, "--json", "visibility", "-q", ".visibility").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return false, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return false, err
	}
	// Internal repos need the same credentials as private ones, so only PUBLIC ships by reference.
	return strings.TrimSpace(string(out)) != "PUBLIC", nil
}

func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// stageFiles writes the host binary for arch, the bundles, identity.json and plugins.json into dir.
func stageFiles(ctx context.Context, r host.Runner, root, dir, arch, configDir string, private func(string) (bool, error)) error {
	id, err := identity.FromRepo(ctx, r, root)
	if err != nil {
		return err
	}
	if err := id.Write(filepath.Join(dir, "identity.json")); err != nil {
		return err
	}
	// Built here, not read from build/, so a stale installer never ships next to a newer bundle.
	if _, err := host.Must(ctx, r, host.Cmd{Name: "go", Dir: root,
		Args: []string{"build", "-o", filepath.Join(dir, "flocc-setup"), "./installer/cmd/flocc-setup"},
		Env:  []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch}}); err != nil {
		return err
	}
	if _, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", root, "bundle", "create", filepath.Join(dir, "flocc.bundle"), "HEAD"}}); err != nil {
		return err
	}
	allow, err := readAllowlist(filepath.Join(root, "deploy", "agent-plugins.txt"))
	if err != nil {
		return err
	}
	plan, err := planPlugins(configDir, allow, private)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		fmt.Fprintln(os.Stderr, "ship:", w)
	}
	for i, m := range plan.List.Marketplaces {
		from, ok := plan.ArchiveFrom[m.Name]
		if !ok {
			continue
		}
		if _, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", from, "archive", "--format=tar", "-o", filepath.Join(dir, m.Archive), "HEAD"}}); err != nil {
			return err
		}
		sha, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", from, "rev-parse", "HEAD"}})
		if err != nil {
			return err
		}
		plan.List.Marketplaces[i].Commit = strings.TrimSpace(sha)
	}
	return plan.List.Write(filepath.Join(dir, "plugins.json"))
}

// Ship stages the installer on the host named by o.Target and runs it, with the answers document
// (TOML, secrets included) on stdin unless o.Interactive. It returns the summary the host ended
// with, which is zero when the host never got that far; it does not print it. Errors come back
// with the SSH token scrubbed: CmdError text carries the whole command line.
func Ship(ctx context.Context, o Options, answers io.Reader) (Summary, error) {
	if !ValidTarget(o.Target) {
		return Summary{}, badTarget(o.Target)
	}
	var doc []byte
	var secrets []string
	if answers != nil {
		if o.Interactive {
			return Summary{}, usageError("--interactive asks for every answer on the host; drop the answers")
		}
		var err error
		var a *answersPkg.Answers
		if doc, err = io.ReadAll(answers); err == nil {
			// Here, so a typo fails before anything reaches the host.
			a, err = answersPkg.Parse(doc)
		}
		if err != nil {
			return Summary{}, err
		}
		// No command line carries them, but the log must not either.
		secrets = a.Secrets.Values()
	}
	r, dest, err := connect(ctx, o.Target, secrets...)
	if err != nil {
		return Summary{}, err
	}
	root, err := repoRoot(ctx, host.Exec{Log: os.Stderr})
	if err != nil {
		return Summary{}, err
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	return ship(ctx, r, root, dest, shipOpts{Target: o.Target, Interactive: o.Interactive, TTY: o.TTY, Answers: doc, Out: o.Out})
}

func ship(ctx context.Context, r host.Runner, root string, dest Dest, o shipOpts) (Summary, error) {
	tap := &summaryTap{out: o.Out}
	err := scrubErr(shipTo(ctx, r, root, dest, o, tap), dest.Token)
	tap.Flush()
	s, _ := tap.Summary()
	s.Target = o.Target
	return s, err
}

func shipTo(ctx context.Context, r host.Runner, root string, dest Dest, o shipOpts, tap io.Writer) error {
	if err := Connect(ctx, r, dest, o.Target, o.TTY); err != nil {
		return err
	}
	unameM, err := host.Must(ctx, r, sshCmd(dest, false, "uname -m"))
	if err != nil {
		return err
	}
	arch, err := archFor(unameM)
	if err != nil {
		return err
	}
	if !o.Interactive {
		if err := checkSudo(ctx, r, dest); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "flocc-ship-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := stageFiles(ctx, r, root, dir, arch, claudeConfigDir(), ghPrivate); err != nil {
		return err
	}
	// A finished run leaves the stage root:flocc 750, so only sudo can clear it.
	if o.Interactive {
		err = r.Interactive(ctx, sshCmd(dest, true, resetStage))
	} else {
		_, err = host.Must(ctx, r, sshNoTTY(dest, resetStageNoPrompt))
	}
	if err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	scp := append(append([]string{"-q"}, dest.Opts...), files...)
	if _, err := host.Must(ctx, r, host.Cmd{Name: "scp", Args: append(scp, dest.SSH+":/tmp/flocc-setup/")}); err != nil {
		return err
	}
	c := sshCmd(dest, true, remoteCmd(true))
	if !o.Interactive {
		c = sshNoTTY(dest, remoteCmd(false))
		c.Stdin = bytes.NewReader(o.Answers)
	}
	c.Stdout = tap
	return r.Interactive(ctx, c)
}

// checkSudo runs before anything is staged: without a terminal a sudo password prompt fails the
// run halfway.
func checkSudo(ctx context.Context, r host.Runner, dest Dest) error {
	res, err := r.Run(ctx, sshNoTTY(dest, "sudo -n true"))
	switch {
	case err != nil:
		return err
	case res.Code == 0:
		return nil
	case res.Code == 127:
		return errors.New("sudo is not installed on the host; install sudo and ship again")
	}
	user, err := host.Must(ctx, r, sshNoTTY(dest, "id -un"))
	if err != nil {
		return err
	}
	return fmt.Errorf("sudo asks for a password: give %s passwordless sudo, or use root", strings.TrimSpace(user))
}

func remoteCmd(interactive bool) string {
	// scp through the Daytona gateway drops the exec bit.
	remote := "chmod 755 /tmp/flocc-setup/flocc-setup && "
	if interactive {
		return remote + "sudo /tmp/flocc-setup/flocc-setup"
	}
	return remote + "sudo -n /tmp/flocc-setup/flocc-setup --answers -"
}
