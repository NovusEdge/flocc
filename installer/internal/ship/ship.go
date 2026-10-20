// Package ship stages the installer, bundles and plugin list and runs it on a host.
package ship

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
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

const resetStage = "sudo rm -rf /tmp/flocc-setup && mkdir -p /tmp/flocc-setup"

func sshCmd(d Dest, tty bool, remote string) host.Cmd {
	args := append([]string{}, d.Opts...)
	if tty {
		args = append(args, "-t")
	}
	return host.Cmd{Name: "ssh", Args: append(args, d.SSH, remote)}
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
func stageFiles(ctx context.Context, r host.Runner, root, dir, arch, answersIn, configDir string, private func(string) (bool, error)) error {
	id, err := identity.FromRepo(ctx, r, root)
	if err != nil {
		return err
	}
	if err := id.Write(filepath.Join(dir, "identity.json")); err != nil {
		return err
	}
	bin, err := os.ReadFile(filepath.Join(root, "build", "flocc-setup-linux-"+arch))
	if err != nil {
		return fmt.Errorf("%w; run just build-setup", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flocc-setup"), bin, 0o755); err != nil {
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
	if err := plan.List.Write(filepath.Join(dir, "plugins.json")); err != nil {
		return err
	}
	if answersIn != "" {
		b, err := os.ReadFile(answersIn)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(answersIn)), b, 0o600)
	}
	return nil
}

func Main(args []string) int {
	fs := flag.NewFlagSet("flocc-setup ship", flag.ContinueOnError)
	skip := fs.Bool("skip-interactive", false, "pass --skip-interactive to the host")
	answersIn := fs.String("answers", "", "answers file to copy and pass to the host")
	stageOnly := fs.String("stage-only", "", "write the stage for linux/amd64 into this directory and stop")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	r := host.Exec{Log: os.Stderr}
	rootOut, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"rev-parse", "--show-toplevel"}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ship:", err)
		return 1
	}
	root := strings.TrimSpace(rootOut)
	if *stageOnly != "" {
		if err := os.MkdirAll(*stageOnly, 0o755); err == nil {
			err = stageFiles(ctx, r, root, *stageOnly, "amd64", *answersIn, claudeConfigDir(), ghPrivate)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ship:", err)
			return 1
		}
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: flocc-setup ship [--skip-interactive] [--answers FILE] <ssh-target|daytona:SANDBOX>")
		return 2
	}
	home, _ := os.UserHomeDir()
	dest, err := resolveTarget(ctx, fs.Arg(0), filepath.Join(home, ".config/daytona/config.json"), &http.Client{Timeout: 30 * time.Second}, realWaiter(os.Stderr))
	if err == nil {
		err = ship(ctx, host.Exec{Log: redact{os.Stderr, dest.Token}}, root, dest, *answersIn, *skip)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode() // the host already reported its error
		}
		fmt.Fprintln(os.Stderr, "ship:", err)
		return 1
	}
	return 0
}

// ship returns errors with the SSH token scrubbed: CmdError text carries the whole command line.
func ship(ctx context.Context, r host.Runner, root string, dest Dest, answersIn string, skip bool) error {
	err := shipTo(ctx, r, root, dest, answersIn, skip)
	var exit *exec.ExitError
	if err == nil || dest.Token == "" || errors.As(err, &exit) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), dest.Token, "<token>"))
}

func shipTo(ctx context.Context, r host.Runner, root string, dest Dest, answersIn string, skip bool) error {
	unameM, err := host.Must(ctx, r, sshCmd(dest, false, "uname -m"))
	if err != nil {
		return err
	}
	arch, err := archFor(unameM)
	if err != nil {
		return err
	}
	if err := refuseLegacy(ctx, r, dest); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "flocc-ship-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := stageFiles(ctx, r, root, dir, arch, answersIn, claudeConfigDir(), ghPrivate); err != nil {
		return err
	}
	// A finished run leaves the stage root:flocc 750, so only sudo can clear it, and its
	// password prompt needs the terminal.
	if err := r.Interactive(ctx, sshCmd(dest, true, resetStage)); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	scp := append(append([]string{"-q"}, dest.Opts...), files...)
	if _, err := host.Must(ctx, r, host.Cmd{Name: "scp", Args: append(scp, dest.SSH+":/tmp/flocc-setup/")}); err != nil {
		return err
	}
	return r.Interactive(ctx, sshCmd(dest, true, remoteCmd(answersIn, skip)))
}

func remoteCmd(answersIn string, skip bool) string {
	// scp through the Daytona gateway drops the exec bit.
	remote := "chmod 755 /tmp/flocc-setup/flocc-setup && sudo /tmp/flocc-setup/flocc-setup"
	if answersIn != "" {
		remote += " --answers /tmp/flocc-setup/" + filepath.Base(answersIn)
	}
	if skip {
		remote += " --skip-interactive"
	}
	return remote
}
