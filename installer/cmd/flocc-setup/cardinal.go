package main

import (
	"context"
	"encoding/json"
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
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

var (
	cardinalRootPhases = []string{"cardinal-root", "cardinal-purge", "cardinal-host", "cardinal-host-remove"}
	cardinalUserPhases = []string{"cardinal", "cardinal-login", "cardinal-prestart", "cardinal-renew", "cardinal-status"}
	// stageFiles are what each machine root phase reads from its stage.
	stageFiles = map[string][]string{"cardinal-root": {"flocc-setup", "desktop.pub"}, "cardinal-purge": {"flocc-setup"}}
)

// cardinalPhase finds a cardinal phase in args before the flag set parses them: the cardinal
// phases take flags the ship phases do not.
func cardinalPhase(args []string) (string, bool) {
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
			return v, slices.Contains(cardinalRootPhases, v) || slices.Contains(cardinalUserPhases, v)
		}
	}
	return "", false
}

func checkCardinalPhase(phase string, euid int) error {
	switch {
	case slices.Contains(cardinalRootPhases, phase) && euid != 0:
		return fmt.Errorf("the %s phase runs as root", phase)
	case slices.Contains(cardinalUserPhases, phase) && euid == 0:
		return fmt.Errorf("the %s phase runs as the cardinal user, not root", phase)
	}
	return nil
}

func runCardinal(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("flocc-setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	phase := fs.String("phase", "", "the cardinal phase")
	user := fs.String("user", "", "cardinal-root, cardinal-purge: the user")
	adopt := fs.Bool("adopt", false, "cardinal-root: take over an existing user")
	answersIn := fs.String("answers", "", `"-" reads the pass's JSON document from stdin`)
	email := fs.String("email", "", "cardinal-login: the claude.ai account")
	quick := fs.Bool("quick", false, "cardinal-status: local state only")
	minAge := fs.Duration("min-age", steps.RenewAge, "cardinal-renew: the session age that renews; for the live test")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(code int, err error) int {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return code
	}
	files, staged := stageFiles[*phase]
	if (*user != "" || staged) && !saved.ValidUser(*user) {
		return fail(2, fmt.Errorf("--user %q is not a user name", *user))
	}
	if err := checkCardinalPhase(*phase, os.Geteuid()); err != nil {
		return fail(2, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(1, err)
	}
	stage := filepath.Dir(exe)
	if staged {
		sudoUID, err := strconv.Atoi(os.Getenv("SUDO_UID"))
		if err != nil {
			return fail(2, errors.New("run the root pass through sudo; SUDO_UID names the stage's owner"))
		}
		if err := steps.CheckCardinalStage(stage, sudoUID, files...); err != nil {
			return fail(2, err)
		}
	}
	// Status runs on every fleet redraw; its log would only grow.
	logw := io.Discard
	logPath := filepath.Join(stage, "setup.log")
	if *phase != "cardinal-status" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fail(1, err)
		}
		defer f.Close()
		logw = f
	}
	osRelease, _ := os.ReadFile("/etc/os-release")
	distro, _ := host.ParseOSRelease(string(osRelease))
	env := &steps.Env{Run: host.Exec{Log: logw}, Stage: stage, Mode: steps.Systemd, Distro: distro,
		Answers: &answers.Answers{}, SkipInteractive: true, UID: os.Getuid(), Sleep: time.Sleep,
		UI: &ui.Scripted{A: &answers.Answers{}, Out: stdout}}
	if slices.Contains(cardinalUserPhases, *phase) {
		env.Home, _ = os.UserHomeDir()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	finish := func(r steps.CardinalReport, err error) int {
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
	wantStdin := func() error {
		if *answersIn != "-" {
			return errors.New("the document travels on stdin; pass --answers -")
		}
		return nil
	}

	switch *phase {
	case "cardinal-root":
		list := steps.CardinalRootSteps(*user, *adopt)
		err := steps.RunAll(ctx, env, list)
		return finish(steps.CardinalReport{Steps: steps.ReportSteps(ctx, env, list)}, err)
	case "cardinal-purge":
		return finish(steps.CardinalReport{}, steps.CardinalPurge(ctx, env, *user))
	case "cardinal-host", "cardinal-host-remove":
		var d steps.HostDoc
		if err := wantStdin(); err != nil {
			return fail(2, err)
		}
		if err := json.NewDecoder(stdin).Decode(&d); err != nil {
			return fail(1, err)
		}
		do := steps.CardinalHost
		if *phase == "cardinal-host-remove" {
			do = steps.CardinalHostRemove
		}
		return finish(do(ctx, env, d))
	case "cardinal":
		var d steps.CardinalDoc
		if err := wantStdin(); err != nil {
			return fail(2, err)
		}
		if err := json.NewDecoder(stdin).Decode(&d); err != nil {
			return fail(1, err)
		}
		var notes []string
		list := steps.CardinalUserSteps(d, &notes)
		err := steps.RunAll(ctx, env, list)
		return finish(steps.CardinalReport{Key: steps.CardinalPubKey(env), Login: steps.CardinalLoginState(ctx, env),
			Steps: steps.ReportSteps(ctx, env, list), Notes: notes}, err)
	case "cardinal-login":
		env.UI, env.SkipInteractive = ui.NewHuh(), false
		err := steps.CardinalLogin(ctx, env, steps.CardinalLoginOptions{User: filepath.Base(env.Home), Email: *email})
		if errors.Is(err, steps.ErrStopped) {
			fmt.Fprintln(stdout, "Stopped. Run flocc cardinal login again to finish.")
			return 1
		}
		return finish(steps.CardinalReport{Login: steps.CardinalLoginState(ctx, env)}, err)
	case "cardinal-prestart":
		if err := steps.CardinalPrestart(env); err != nil {
			return fail(1, err)
		}
		return 0
	case "cardinal-renew":
		renewed, err := steps.CardinalRenew(ctx, env, *minAge, time.Now())
		if err != nil {
			return fail(1, err)
		}
		if renewed {
			fmt.Fprintln(stdout, "renewed cardinal's session")
		}
		return 0
	case "cardinal-status":
		s := steps.CollectCardinalStatus(ctx, env, filepath.Base(env.Home), *quick, time.Now())
		if !*quick && s.Unit != "active" {
			s.Journal = steps.CardinalJournal(ctx, env)
		}
		if !*quick {
			s.Pane = steps.CardinalPane(ctx, env, s.User)
		}
		b, _ := json.Marshal(s)
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	return fail(2, fmt.Errorf("unknown phase %q", *phase))
}
