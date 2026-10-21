package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const serviceStage = "/home/" + steps.ServiceUser + "/.cache/flocc-setup"

// The service phase's saved answers: outside ~/.cache, which may be cleared, and outside the
// stage, whose *.toml files removeStagedAnswers deletes.
func savedAnswersPath(home string) string {
	return filepath.Join(home, ".config/flocc/answers.toml")
}

// removeStagedAnswers deletes the *.toml files in the service user's stage. 0.3.0 copied the
// --answers file there, secrets included, and deleted it only after a successful run; a failed
// run leaves it behind, and the 0.4.0 stage copy never touches it.
func removeStagedAnswers(stage string) error {
	files, err := filepath.Glob(filepath.Join(stage, "*.toml"))
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range files {
		errs = append(errs, os.Remove(f))
	}
	return errors.Join(errs...)
}

const loopWarning = `PID 1 is not systemd, so this host runs in loop mode:
- flocc run in tmux session flocc-loop ticks every 3 minutes, in place of the timer;
- processes a timed-out check daemonized are not killed when its tick ends;
- after the host stops, run flocc ship -H <host> again to restart tailscaled and the loop.
Continue?`

func main() { os.Exit(run(os.Args[1:])) }

func checkPhase(phase string, euid int) error {
	switch {
	case phase == "root" && euid != 0:
		return errors.New("the root phase runs as root; flocc ship runs it with sudo")
	case (phase == "service" || phase == "login") && euid == 0:
		return errors.New("the " + phase + " phase runs as " + steps.ServiceUser + ", not root")
	case phase != "root" && phase != "service" && phase != "login":
		return fmt.Errorf("unknown phase %q", phase)
	}
	return nil
}

func handoffArgs(fromStdin bool) []string {
	args := []string{"--phase", "service"}
	if fromStdin {
		args = append(args, "--answers", "-")
	}
	return args
}

// colorArgs hands the colour request, and with it the desktop's width, to the service phase. Its
// sudo -i drops the environment the root phase got them through.
func colorArgs(color bool, cols int) []string {
	if !color {
		return nil
	}
	args := []string{"--color"}
	if cols > 0 {
		args = append(args, "--cols", strconv.Itoa(cols))
	}
	return args
}

// columns is the width the desktop reported: the flag in the service phase, COLUMNS in the root
// phase; zero when neither names a usable one.
func columns(flagValue int, getenv func(string) string) int {
	if flagValue > 0 {
		return flagValue
	}
	if n, err := strconv.Atoi(getenv(ui.ColumnsEnv)); err == nil && n > 0 {
		return n
	}
	return 0
}

// rootStage is where flocc ship stages the files. The root phase chowns the stage, so it must
// never be any other directory.
const rootStage = "/tmp/flocc-setup"

var stageFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func checkStage(dir string) error {
	if dir != rootStage {
		return fmt.Errorf("the root phase runs from %s, not %s; flocc ship stages it there", rootStage, dir)
	}
	return nil
}

// stageNames lists what the service user copies. Root names every file itself because the
// script must hold no shell expansion: sudo -i hands it to the login shell, which expands
// `$` before the inner sh runs.
func stageNames(stage string) ([]string, error) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range entries {
		n := ent.Name()
		known := n == "flocc-setup" || n == "flocc.bundle" || n == "plugins.json" || n == "identity.json" || strings.HasSuffix(n, ".tar")
		if ent.Type().IsRegular() && known && stageFile.MatchString(n) {
			names = append(names, n)
		}
	}
	if !slices.Contains(names, "flocc-setup") {
		return nil, fmt.Errorf("%s has no flocc-setup binary", stage)
	}
	return names, nil
}

// copyScript is what the service user runs to copy the stage into its own home. The names
// must already match stageFile, so none needs quoting.
func copyScript(dir string, names []string) string {
	return "mkdir -p ~/.cache/flocc-setup && cd " + dir + " && cp -- " + strings.Join(names, " ") + " ~/.cache/flocc-setup/"
}

// handoff lets the service user read the stage and copy it itself. Root never writes inside
// the service home: an agent could plant a symlink there and have root write through it. doc
// is the answers document as root read it, piped on; nil leaves the service phase the terminal.
func handoff(ctx context.Context, r host.Runner, stage string, args []string, doc io.Reader) error {
	names, err := stageNames(stage)
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
	return r.Interactive(ctx, host.Cmd{Name: "sudo", Args: append([]string{"-iu", steps.ServiceUser, serviceStage + "/flocc-setup"}, args...), Stdin: doc})
}

// readAnswers takes the whole document before anything runs: commands started later must find
// stdin at EOF, and the service phase needs the same bytes.
func readAnswers(r io.Reader) ([]byte, *answers.Answers, error) {
	doc, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	a, err := answers.Parse(doc)
	return doc, a, err
}

func run(args []string) int {
	if _, ok := cardinalPhase(args); ok {
		return runCardinal(args, os.Stdin, os.Stdout, os.Stderr)
	}
	fs := flag.NewFlagSet("flocc-setup", flag.ContinueOnError)
	phase := fs.String("phase", "root", "root, service or login; the root phase hands off to the service phase itself")
	answersIn := fs.String("answers", "", `"-" reads the answers document, secrets included, from stdin and runs without a terminal`)
	email := fs.String("email", "", "login phase: the claude.ai account to sign in as")
	// The desktop asks through the environment (ui.ColorEnv), which an older flocc-setup ignores;
	// the flag only carries the request from the root phase to the service phase, whose sudo -i
	// drops the environment.
	color := fs.Bool("color", false, "colour the step lines of a run with --answers")
	colsFlag := fs.Int("cols", 0, "with --color: the desktop terminal's width, for the live step line")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *answersIn != "" && *answersIn != "-" {
		fmt.Fprintln(os.Stderr, "flocc-setup: answers travel on stdin; pass --answers - and pipe the document in")
		return 2
	}
	fromStdin := *answersIn == "-"
	wantColor := *color || os.Getenv(ui.ColorEnv) == "1"
	cols := columns(*colsFlag, os.Getenv)
	var doc []byte
	in := &answers.Answers{}
	if fromStdin {
		var err error
		if doc, in, err = readAnswers(os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "flocc-setup:", err)
			return 1
		}
	}
	secrets := in.Secrets.Values()
	stdout, stderr := host.Redact(os.Stdout, secrets...), host.Redact(os.Stderr, secrets...)
	if err := checkPhase(*phase, os.Geteuid()); err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 1
	}
	stage := filepath.Dir(exe)
	if *phase == "root" {
		if err := checkStage(stage); err != nil {
			fmt.Fprintln(stderr, "flocc-setup:", err)
			return 2
		}
	}
	logPath := filepath.Join(stage, "setup.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 1
	}
	defer logf.Close()
	runner := host.Exec{Log: host.Redact(logf, secrets...)}

	pid1, _ := os.ReadFile("/proc/1/comm")
	osRelease, _ := os.ReadFile("/etc/os-release")
	distro, err := host.ParseOSRelease(string(osRelease))
	if err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 1
	}
	a := &answers.Answers{}
	env := &steps.Env{Run: runner, Stage: stage, Mode: steps.DetectMode(string(pid1)), Distro: distro,
		Answers: a, SkipInteractive: fromStdin, UID: os.Getuid(), Sleep: time.Sleep}
	if *phase != "root" {
		env.Home, _ = os.UserHomeDir()
	}
	if *phase == "service" {
		if stage == serviceStage {
			if err := removeStagedAnswers(stage); err != nil {
				fmt.Fprintln(stderr, "flocc-setup: could not remove a leftover answers file from", stage+":", err)
			}
		}
		env.AnswersPath = savedAnswersPath(env.Home)
		if saved, err := answers.Load(env.AnswersPath); err == nil {
			if err := a.OverlaySaved(saved); err != nil {
				fmt.Fprintln(stderr, "flocc-setup: the saved ntfy topic in", env.AnswersPath, "is invalid and will be asked again:", err)
			}
		}
	}
	if err := a.Overlay(in); err != nil {
		fmt.Fprintln(stderr, "flocc-setup: answers:", err)
		return 1
	}
	// Overlay keeps a saved table when the shipped one is absent; a ship is the whole host file,
	// so a table removed there must go from the host too.
	if fromStdin {
		a.Permissions = in.Permissions
	}
	var u ui.UI = ui.NewHuh()
	if fromStdin {
		u = &ui.Scripted{A: a, Out: stdout, Color: wantColor, Cols: cols}
	}
	env.UI = u
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *phase == "login" {
		err := steps.Login(ctx, env, steps.LoginOptions{Email: *email})
		if errors.Is(err, steps.ErrStopped) {
			fmt.Fprintln(stdout, "Stopped. Run flocc login -H <host> again to finish.")
			return 1
		}
		return finish(ctx, env, err, true, stdout, stderr, logPath)
	}
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
		var piped io.Reader
		if fromStdin {
			piped = bytes.NewReader(doc)
		}
		args := handoffArgs(fromStdin)
		args = append(args, colorArgs(wantColor, cols)...)
		err = handoff(ctx, runner, stage, args, piped)
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode() // the service phase already reported its error
		}
	}
	return finish(ctx, env, err, *phase == "service", stdout, stderr, logPath)
}

// finish ends the service or login phase's output with the summary line when it got as far as
// doctor, and prints the error otherwise. The desktop takes that line out of the stream and
// prints the summary itself, so run by hand on the host the output ends in raw JSON.
func finish(ctx context.Context, env *steps.Env, err error, summary bool, stdout, stderr io.Writer, logPath string) int {
	var failed *steps.DoctorFailed
	switch {
	case errors.Is(err, steps.ErrStopped):
		fmt.Fprintln(stdout, "Stopped. Run flocc ship -H <host> again to continue.")
		return 0
	case err != nil && !(summary && errors.As(err, &failed)):
		fmt.Fprintf(stderr, "\n%s\n\nFull log: %s\n", err, logPath)
		return 1
	case !summary:
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "\n%s\n", err)
	}
	s := steps.Summarize(ctx, env).Redact(env.KnownSecrets()...)
	// A blank line first: the marker must start a line, and a prompt may have left a partial one.
	fmt.Fprintf(stdout, "\n%s\n", s.Line())
	if s.Failing() {
		return 1
	}
	return 0
}
