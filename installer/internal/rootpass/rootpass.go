// Package rootpass is the desktop's side of a root pass: build flocc-setup, stage it in a directory
// the owner's mktemp made, and run it through sudo in one ssh -t. flocc host create and setup's
// old-users removal both use it.
package rootpass

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/safe"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func SSH(opts []string, tty bool, dest, remote string) host.Cmd {
	args := append([]string{}, opts...)
	if tty {
		args = append(args, "-t")
	} else {
		// A RequestTTY in the owner's ssh config would otherwise give the pass a pty, which echoes
		// its stdin and never passes EOF.
		args = append(args, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
	}
	return host.Cmd{Name: "ssh", Args: append(args, dest, remote)}
}

// selfRevision is the commit this flocc was built from; empty without VCS stamps, as under go test.
var selfRevision = func() string { return ship.SelfBuild() }

// checkoutHasSelf stops a build from a checkout that lacks the commit this flocc came from: its
// flocc-setup could miss phases this flocc calls, which shows only after the sudo password.
func checkoutHasSelf(ctx context.Context, r host.Runner, root string) error {
	rev := selfRevision()
	if rev == "" {
		return nil
	}
	res, err := r.Run(ctx, host.Cmd{Name: "git", Dir: root, Args: []string{"merge-base", "--is-ancestor", rev, "HEAD"}})
	// 1 is "not an ancestor", 128 "no such commit here"; anything else leaves the build to go on.
	if err != nil || (res.Code != 1 && res.Code != 128) {
		return nil
	}
	head, _ := host.Must(ctx, r, host.Cmd{Name: "git", Dir: root, Args: []string{"rev-parse", "--short", "HEAD"}})
	return fmt.Errorf("this flocc was built from %s, but the checkout at %s is at %s, which lacks it; run this from a checkout that has %s, or go install ./installer/cmd/flocc in this one",
		rev[:min(7, len(rev))], root, strings.TrimSpace(head), rev[:min(7, len(rev))])
}

// Build makes ./installer/cmd/<name> for linux/arch from the checkout at root, one step per
// binary, into a new temporary directory the caller removes.
func Build(ctx context.Context, r host.Runner, s ui.Session, root, arch string, names ...string) (string, error) {
	if err := checkoutHasSelf(ctx, r, root); err != nil {
		s.Step("Build " + strings.Join(names, ", ")).Fail(err)
		return "", err
	}
	dir, err := os.MkdirTemp("", "flocc-rootpass-")
	if err != nil {
		return "", err
	}
	for _, b := range names {
		st := s.Step("Build " + b)
		if _, err := host.Must(ctx, r, host.Cmd{Name: "go", Dir: root,
			Args: []string{"build", "-o", filepath.Join(dir, b), "./installer/cmd/" + b},
			Env:  []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch}}); err != nil {
			os.RemoveAll(dir)
			st.Fail(err)
			return "", err
		}
		st.Done("linux/" + arch)
	}
	return dir, nil
}

type Pass struct {
	Machine string     // the owner's ssh destination on the machine
	Opts    []string   // ssh options: the shared master's
	Prefix  string     // the stage's mktemp prefix: "/tmp/flocc-create" or "/tmp/flocc-remove"
	Files   []string   // local files to stage; one must be named flocc-setup
	Calls   [][]string // flocc-setup's arguments, one sudo each, in order; shell-safe words only
	Why     string     // the handoff banner
}

// Run stages p.Files from a stage only the owner's mktemp could have made, and runs every call on
// one line in one ssh -t: sudo's ticket for that terminal covers every call after the first, so the
// owner types one password. A failed call stops the ones after it. The stage goes after a success
// and stays, with its log, after a failure. It returns the reports in call order, and on failure
// also an error naming the last report error and the stage's log.
func Run(ctx context.Context, r host.Runner, p Pass) ([]steps.RootReport, error) {
	dir, err := host.Must(ctx, r, SSH(p.Opts, false, p.Machine, "mktemp -d "+p.Prefix+".XXXXXX"))
	dir = strings.TrimSpace(dir)
	if err != nil {
		return nil, err
	}
	if !steps.ValidRootStage(dir) || !strings.HasPrefix(dir, p.Prefix+".") {
		return nil, fmt.Errorf("mktemp on %s returned %q", p.Machine, dir)
	}
	scp := host.Cmd{Name: "scp", Args: append(append(append([]string{"-q"}, p.Opts...), p.Files...), p.Machine+":"+dir+"/")}
	if _, err := host.Must(ctx, r, scp); err != nil {
		return nil, err
	}
	remote := "chmod 755 " + dir + "/flocc-setup"
	for _, call := range p.Calls {
		remote += " && sudo " + dir + "/flocc-setup " + strings.Join(call, " ")
	}
	remote += " && rm -rf " + dir
	var reports []steps.RootReport
	// Safe on os.Stdout only because c has no Stdin: under a ui session that makes Interactive a
	// handoff, so the tap writes while the session has released the terminal.
	tap := Tap(os.Stdout, &reports)
	c := SSH(p.Opts, true, p.Machine, remote)
	c.Why = p.Why
	c.Stdout = tap
	err = r.Interactive(ctx, c)
	tap.Flush()
	if err != nil {
		var why string
		for _, rep := range reports {
			if rep.Error != "" {
				why = safe.Sanitize(rep.Error) + "; "
			}
		}
		return reports, fmt.Errorf("the root pass failed: %sits log is %s/setup.log on %s: %w", why, dir, p.Machine, err)
	}
	return reports, nil
}

// Events are a pass's steps and notes as rows. Every string came from the far end, so each is
// sanitized.
func Events(r steps.RootReport) []ui.Event {
	var evs []ui.Event
	for _, s := range r.Steps {
		e := ui.Event{Name: safe.Sanitize(s.Name), Detail: safe.Sanitize(s.Detail), State: ui.Done}
		switch {
		case s.Pending:
			e.State = ui.Pending
		case !s.Done:
			e.State = ui.Failed
		}
		evs = append(evs, e)
	}
	for _, n := range r.Notes {
		evs = append(evs, ui.Event{Name: "note", Detail: safe.Sanitize(n), State: ui.Skipped})
	}
	return evs
}

// Tap passes a pass's output through as it comes, prompts included, and keeps each
// root report line in rs for Events.
func Tap(out io.Writer, rs *[]steps.RootReport) *ship.LineTap {
	return &ship.LineTap{Out: out, Marker: steps.RootMarker, Take: func(line string) bool {
		got, ok := steps.ParseRootLine(line)
		if ok {
			*rs = append(*rs, got)
		}
		return ok
	}}
}
