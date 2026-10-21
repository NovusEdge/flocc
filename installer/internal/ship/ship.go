// Package ship stages the installer, bundles and plugin list and runs it on a host.
package ship

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func ArchFor(unameM string) (string, error) {
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
	// UI shows the staging steps; the host's own run is a handoff on it.
	UI ui.Session
}

type shipOpts struct {
	UI ui.Session
	// Target is the name the owner typed, for the login command the summary shows. Never
	// Dest.SSH, which holds the Daytona token.
	Target      string
	Interactive bool
	TTY         bool
	Answers     []byte
	// Out receives an interactive host's output, less the summary line.
	Out io.Writer
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

// connect resolves target to an ssh destination and a runner whose attached commands go through
// term, the session that owns the terminal. It keeps no command log: a log on stderr would draw
// under the session.
func connect(ctx context.Context, target string, term host.Terminal) (host.Runner, Dest, error) {
	dest, err := Resolve(ctx, target)
	if err != nil {
		return nil, Dest{}, err
	}
	return host.Exec{Term: term}, dest, nil
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

// stage runs f as one step named name: f's string is the Done detail, its error fails the step.
func stage(s ui.Session, name string, f func(st *ui.Step) (string, error)) error {
	st := s.Step(name)
	detail, err := f(st)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done(detail)
	return nil
}

// stageFiles writes the host binary for arch, the bundles, identity.json and plugins.json into dir.
func stageFiles(ctx context.Context, s ui.Session, r host.Runner, root, dir, arch, configDir string, private func(string) (bool, error)) error {
	id, err := identity.FromRepo(ctx, r, root)
	if err != nil {
		return err
	}
	if err := id.Write(filepath.Join(dir, "identity.json")); err != nil {
		return err
	}
	// Built here, not read from build/, so a stale installer never ships next to a newer bundle.
	err = stage(s, "Build flocc-setup", func(st *ui.Step) (string, error) {
		st.Detail("go build linux/" + arch)
		_, err := host.Must(ctx, r, host.Cmd{Name: "go", Dir: root,
			Args: []string{"build", "-o", filepath.Join(dir, "flocc-setup"), "./installer/cmd/flocc-setup"},
			Env:  []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch}})
		return "linux/" + arch, err
	})
	if err != nil {
		return err
	}
	err = stage(s, "Bundle", func(st *ui.Step) (string, error) {
		st.Detail("git bundle HEAD")
		_, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", root, "bundle", "create", filepath.Join(dir, "flocc.bundle"), "HEAD"}})
		return "", err
	})
	if err != nil {
		return err
	}
	return stage(s, "Plugins", func(st *ui.Step) (string, error) {
		allow, err := readAllowlist(filepath.Join(root, "deploy", "agent-plugins.txt"))
		if err != nil {
			return "", err
		}
		plan, err := planPlugins(configDir, allow, private)
		if err != nil {
			return "", err
		}
		for _, w := range plan.Warnings {
			s.Note("ship", w)
		}
		n := 0
		for i, m := range plan.List.Marketplaces {
			from, ok := plan.ArchiveFrom[m.Name]
			if !ok {
				continue
			}
			st.Detail("git archive " + m.Name)
			st.Progress(n, len(plan.ArchiveFrom))
			n++
			if _, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", from, "archive", "--format=tar", "-o", filepath.Join(dir, m.Archive), "HEAD"}}); err != nil {
				return "", err
			}
			sha, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", from, "rev-parse", "HEAD"}})
			if err != nil {
				return "", err
			}
			plan.List.Marketplaces[i].Commit = strings.TrimSpace(sha)
		}
		return fmt.Sprintf("%d plugins", len(plan.List.Plugins)), plan.List.Write(filepath.Join(dir, "plugins.json"))
	})
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
	if answers != nil {
		if o.Interactive {
			return Summary{}, usageError("--interactive asks for every answer on the host; drop the answers")
		}
		var err error
		if doc, err = io.ReadAll(answers); err == nil {
			// Here, so a typo fails before anything reaches the host.
			_, err = answersPkg.Parse(doc)
		}
		if err != nil {
			return Summary{}, err
		}
	}
	r, dest, err := connect(ctx, o.Target, o.UI)
	if err != nil {
		return Summary{}, err
	}
	root, err := repoRoot(ctx, host.Exec{})
	if err != nil {
		return Summary{}, err
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	return ship(ctx, r, root, dest, shipOpts{Target: o.Target, Interactive: o.Interactive, TTY: o.TTY, Answers: doc, Out: o.Out, UI: o.UI})
}

func ship(ctx context.Context, r host.Runner, root string, dest Dest, o shipOpts) (Summary, error) {
	if o.Interactive {
		tap := newSummaryTap(o.Out)
		err := scrubErr(shipTo(ctx, r, root, dest, o, func() io.Writer { return tap }), dest.Token)
		tap.Flush()
		s, _ := tap.Summary()
		s.Target = o.Target
		return s, err
	}
	// Made when the host run starts, so its group heading follows the staging steps.
	var rp *ui.Replayer
	err := scrubErr(shipTo(ctx, r, root, dest, o, func() io.Writer {
		rp = ui.Replay(o.UI, "Host "+o.Target, o.Target+"/")
		return rp
	}), dest.Token)
	var s Summary
	if rp != nil {
		err = errors.Join(err, rp.Close())
		if text, ok := rp.Data("summary"); ok {
			json.Unmarshal([]byte(text), &s) // a host that sent something else has no summary to show
		}
	}
	s.Target = o.Target
	return s, err
}

func shipTo(ctx context.Context, r host.Runner, root string, dest Dest, o shipOpts, hostOut func() io.Writer) error {
	var arch string
	err := stage(o.UI, "Connect", func(st *ui.Step) (string, error) {
		st.Detail("ssh")
		if err := Connect(ctx, r, dest, o.Target, o.TTY); err != nil {
			return "", err
		}
		st.Detail("uname -m")
		unameM, err := host.Must(ctx, r, sshCmd(dest, false, "uname -m"))
		if err != nil {
			return "", err
		}
		if arch, err = ArchFor(unameM); err != nil {
			return "", err
		}
		if !o.Interactive {
			st.Detail("sudo -n true")
			if err := checkSudo(ctx, r, dest); err != nil {
				return "", err
			}
		}
		return "linux/" + arch, nil
	})
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "flocc-ship-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := stageFiles(ctx, o.UI, r, root, dir, arch, claudeConfigDir(), ghPrivate); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	err = stage(o.UI, "Copy", func(st *ui.Step) (string, error) {
		// A finished run leaves the stage root:flocc 750, so only sudo can clear it.
		st.Detail("reset /tmp/flocc-setup")
		var err error
		if o.Interactive {
			err = r.Interactive(ctx, withWhy(sshCmd(dest, true, resetStage), "reset the stage on "+o.Target))
		} else {
			_, err = host.Must(ctx, r, sshNoTTY(dest, resetStageNoPrompt))
		}
		if err != nil {
			return "", err
		}
		st.Detail(fmt.Sprintf("scp %d files", len(files)))
		scp := append(append([]string{"-q"}, dest.Opts...), files...)
		_, err = host.Must(ctx, r, host.Cmd{Name: "scp", Args: append(scp, dest.SSH+":/tmp/flocc-setup/")})
		return fmt.Sprintf("%d files", len(files)), err
	})
	if err != nil {
		return err
	}
	if o.Interactive {
		c := sshCmd(dest, true, remoteCmd(true))
		c.Stdout = hostOut()
		c.Why = "host setup on " + o.Target
		return r.Interactive(ctx, c)
	}
	// Stdin and Stdout both set and no Attach: the run is direct, and the host's events are
	// drawn here.
	c := sshNoTTY(dest, remoteCmd(false))
	c.Stdin = bytes.NewReader(o.Answers)
	c.Stdout = hostOut()
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
	return remote + "sudo -n /tmp/flocc-setup/flocc-setup --answers - --events"
}
