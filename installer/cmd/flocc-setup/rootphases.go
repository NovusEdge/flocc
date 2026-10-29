package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// rootPhases run as root inside one sudo pass that the desktop starts over ssh.
var rootPhases = []string{"create-root", "old-user-remove"}

// rootPhase finds a root phase in args before the flag set parses them: these phases take flags
// the ship phases do not.
func rootPhase(args []string) (string, bool) {
	for i, a := range args {
		v, ok := "", false
		switch {
		case a == "--phase" || a == "-phase":
			if i+1 < len(args) {
				v, ok = args[i+1], true
			}
		case strings.HasPrefix(a, "--phase="), strings.HasPrefix(a, "-phase="):
			_, v, ok = strings.Cut(a, "=")
		}
		if ok {
			return v, slices.Contains(rootPhases, v)
		}
	}
	return "", false
}

func createRootReport(ctx context.Context, env *steps.Env) (steps.RootReport, error) {
	list := steps.CreateRootSteps()
	err := steps.RunAll(ctx, env, list)
	report := steps.RootReport{Steps: steps.ReportSteps(ctx, env, list)}
	// Under a cancelled context every check reads "not running", which is done, and a command error
	// is the cancellation's doing: after a Ctrl-C the report holds the interruption and no steps.
	if ctx.Err() != nil {
		return steps.RootReport{}, fmt.Errorf("interrupted: %w", ctx.Err())
	}
	return report, err
}

func runRootPhase(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("flocc-setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	phase := fs.String("phase", "", "the root phase")
	user := fs.String("user", "", "old-user-remove: the user")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(code int, err error) int {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return code
	}
	if *phase == "old-user-remove" && !slices.Contains(steps.OldUsers, *user) {
		return fail(2, fmt.Errorf("--user %q is not a user an earlier flocc made", *user))
	}
	if os.Geteuid() != 0 {
		return fail(2, fmt.Errorf("the %s phase runs as root", *phase))
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(1, err)
	}
	stage := filepath.Dir(exe)
	sudoUID, err := strconv.Atoi(os.Getenv("SUDO_UID"))
	if err != nil {
		return fail(2, errors.New("run the root pass through sudo; SUDO_UID names the stage's owner"))
	}
	if err := steps.CheckRootStage(stage, sudoUID, "flocc-setup"); err != nil {
		return fail(2, err)
	}
	logPath := filepath.Join(stage, "setup.log")
	logw, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fail(1, err)
	}
	defer logw.Close()
	osRelease, _ := os.ReadFile("/etc/os-release")
	distro, _ := host.ParseOSRelease(string(osRelease))
	env := &steps.Env{Run: host.Exec{Log: logw}, Stage: stage, Mode: steps.Systemd, Distro: distro,
		Answers: &answers.Answers{}, SkipInteractive: true, UID: os.Getuid(), Sleep: time.Sleep,
		UI: &ui.Scripted{A: &answers.Answers{}, Out: stdout}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// sudo ORs 022 into the owner's umask; a 077 owner would make the 0644 files these phases write
	// 0600, which the desktop then cannot read without sudo.
	syscall.Umask(0o022)
	finish := func(r steps.RootReport, err error) int {
		if err != nil {
			r.Error = err.Error()
			fmt.Fprintf(stderr, "\n%s\n\nFull log: %s\n", err, logPath)
		}
		fmt.Fprintf(stdout, "\n%s\n", r.Line())
		if err != nil {
			return 1
		}
		return 0
	}
	switch *phase {
	case "create-root":
		return finish(createRootReport(ctx, env))
	default:
		return finish(steps.RemoveOldUser(ctx, env, *user, sudoUID, time.Now()))
	}
}
