package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/ship"
	"github.com/NovusEdge/linear-agents/installer/internal/steps"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

const serviceStage = "/home/" + steps.ServiceUser + "/.cache/la-setup"

const loopWarning = `PID 1 is not systemd, so this host runs in loop mode:
- linear-agents run in tmux session la-loop ticks every 3 minutes, in place of the timer;
- processes a timed-out check daemonized are not killed when its tick ends;
- after the host stops, run just ship again to restart tailscaled and the loop.
Continue?`

func main() { os.Exit(run(os.Args[1:])) }

func checkPhase(phase string, euid int) error {
	switch {
	case phase == "root" && euid != 0:
		return errors.New("the root phase runs as root; just ship runs it with sudo")
	case phase == "service" && euid == 0:
		return errors.New("the service phase runs as " + steps.ServiceUser + ", not root")
	case phase != "root" && phase != "service":
		return fmt.Errorf("unknown phase %q", phase)
	}
	return nil
}

func handoffArgs(answersIn string, skip bool, dest string) []string {
	args := []string{"--phase", "service"}
	if answersIn != "" {
		args = append(args, "--answers", filepath.Join(dest, filepath.Base(answersIn)))
	}
	if skip {
		args = append(args, "--skip-interactive")
	}
	return args
}

// rootStage is where just ship stages the files. The root phase chowns the stage, so it must
// never be any other directory.
const rootStage = "/tmp/la-setup"

var stageFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func checkStage(dir string) error {
	if dir != rootStage {
		return fmt.Errorf("the root phase runs from %s, not %s; just ship stages it there", rootStage, dir)
	}
	return nil
}

// stageNames lists what the service user copies. Root names every file itself because the
// script must hold no shell expansion: sudo -i hands it to the login shell, which expands
// `$` before the inner sh runs.
func stageNames(stage, answersIn string) ([]string, error) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range entries {
		n := ent.Name()
		known := n == "la-setup" || n == "linear-agents.bundle" || n == "plugins.json" || n == "identity.json" ||
			strings.HasSuffix(n, ".tar") || answersIn != "" && n == filepath.Base(answersIn)
		if ent.Type().IsRegular() && known && stageFile.MatchString(n) {
			names = append(names, n)
		}
	}
	if !slices.Contains(names, "la-setup") {
		return nil, fmt.Errorf("%s has no la-setup binary", stage)
	}
	return names, nil
}

// copyScript is what the service user runs to copy the stage into its own home. The names
// must already match stageFile, so none needs quoting.
func copyScript(dir string, names []string) string {
	return "mkdir -p ~/.cache/la-setup && cd " + dir + " && cp -- " + strings.Join(names, " ") + " ~/.cache/la-setup/"
}

// handoff lets the service user read the stage and copy it itself. Root never writes inside
// the service home: an agent could plant a symlink there and have root write through it.
func handoff(ctx context.Context, r host.Runner, stage, answersIn string, args []string) error {
	if answersIn != "" && !stageFile.MatchString(filepath.Base(answersIn)) {
		return fmt.Errorf("answers file name %q: start with a letter or digit, then use letters, digits, '.', '_' or '-'", filepath.Base(answersIn))
	}
	if answersIn != "" {
		if _, err := os.Stat(filepath.Join(stage, filepath.Base(answersIn))); err != nil {
			return fmt.Errorf("answers file %s is not in the stage: %w", filepath.Base(answersIn), err)
		}
	}
	names, err := stageNames(stage, answersIn)
	if err != nil {
		return err
	}
	for _, c := range []host.Cmd{
		{Name: "chown", Args: []string{"-R", "root:" + steps.ServiceUser, stage}},
		{Name: "chmod", Args: []string{"-R", "g+rX,o-rwx", stage}},
		{Name: "sudo", Args: []string{"-iu", steps.ServiceUser, "sh", "-c", copyScript(rootStage, names)}},
	} {
		if _, err := host.Must(ctx, r, c); err != nil {
			return err
		}
	}
	err = r.Interactive(ctx, host.Cmd{Name: "sudo", Args: append([]string{"-iu", steps.ServiceUser, serviceStage + "/la-setup"}, args...)})
	if err == nil && answersIn != "" {
		dropAnswersCopy(filepath.Join(stage, filepath.Base(answersIn)))
	}
	return err
}

// dropAnswersCopy removes a --answers file, which carries tokens, once the run no longer needs
// it. answers.toml is the saved state of the service phase and stays.
func dropAnswersCopy(path string) {
	if filepath.Base(path) != "answers.toml" {
		os.Remove(path)
	}
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "host" {
		return ship.HostMain(args[1:])
	}
	if len(args) > 0 && args[0] == "fleet" {
		return ship.FleetMain(args[1:], os.Stdout)
	}
	if len(args) > 0 && args[0] == "ship" {
		return ship.Main(args[1:])
	}
	fs := flag.NewFlagSet("la-setup", flag.ContinueOnError)
	phase := fs.String("phase", "root", "root or service; the root phase hands off to the service phase itself")
	answersIn := fs.String("answers", "", "answers file supplying every form value, for runs without a terminal")
	skip := fs.Bool("skip-interactive", false, "skip the Claude and Tailscale logins and read answers from --answers")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := checkPhase(*phase, os.Geteuid()); err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 1
	}
	stage := filepath.Dir(exe)
	if *phase == "root" {
		if err := checkStage(stage); err != nil {
			fmt.Fprintln(os.Stderr, "la-setup:", err)
			return 2
		}
	}
	logPath := filepath.Join(stage, "setup.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 1
	}
	defer logf.Close()
	runner := host.Exec{Log: logf}

	pid1, _ := os.ReadFile("/proc/1/comm")
	osRelease, _ := os.ReadFile("/etc/os-release")
	distro, err := host.ParseOSRelease(string(osRelease))
	if err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 1
	}
	a := &answers.Answers{}
	env := &steps.Env{Run: runner, Stage: stage, Mode: steps.DetectMode(string(pid1)), Distro: distro,
		Answers: a, SkipInteractive: *skip, UID: os.Getuid(), Sleep: time.Sleep}
	if *phase == "service" {
		env.Home, _ = os.UserHomeDir()
		env.AnswersPath = filepath.Join(stage, "answers.toml")
		if saved, err := answers.Load(env.AnswersPath); err == nil {
			if err := a.Overlay(saved); err != nil {
				fmt.Fprintln(os.Stderr, "la-setup:", env.AnswersPath+":", err)
				return 1
			}
		}
	}
	if *answersIn != "" {
		in, err := answers.Load(*answersIn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "la-setup:", err)
			return 1
		}
		if err := a.Overlay(in); err != nil {
			fmt.Fprintln(os.Stderr, "la-setup:", *answersIn+":", err)
			return 1
		}
	}
	var u ui.UI = ui.NewHuh()
	if *skip {
		u = &ui.Scripted{A: a, Out: os.Stdout}
	}
	env.UI = u
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	list := steps.ServiceSteps()
	if *phase == "root" {
		if env.Mode == steps.Loop {
			if ok, err := u.Confirm(loopWarning, false); err != nil || !ok {
				return 0
			}
		}
		list = steps.RootSteps()
	}
	err = steps.RunAll(ctx, env, list)
	if err == nil && *phase == "root" {
		err = handoff(ctx, runner, stage, *answersIn, handoffArgs(*answersIn, *skip, serviceStage))
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode() // the service phase already reported its error
		}
	}
	switch {
	case errors.Is(err, steps.ErrStopped):
		fmt.Println("Stopped. Run just ship again to continue.")
		return 0
	case err != nil:
		fmt.Fprintf(os.Stderr, "\n%s\n\nFull log: %s\n", err, logPath)
		return 1
	}
	if *phase == "service" {
		if *answersIn != "" {
			dropAnswersCopy(*answersIn)
		}
		fmt.Println("Done.")
	}
	return 0
}
