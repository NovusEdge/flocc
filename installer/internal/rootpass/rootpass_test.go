package rootpass

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

var opts = []string{"-o", "ControlMaster=auto"}

func TestBuildRefusesACheckoutOlderThanThisFlocc(t *testing.T) {
	defer func(f func() string) { selfRevision = f }(selfRevision)
	selfRevision = func() string { return "abc1234def" }
	check := "git merge-base --is-ancestor abc1234def HEAD"
	f := host.NewFake().On(check, 1, "").On("git rev-parse --short HEAD", 0, "9f8e7d6\n")
	_, err := Build(context.Background(), f, ui.New(&ui.Recorder{}), "/src", "amd64", "flocc-setup")
	if err == nil || !strings.Contains(err.Error(), "built from abc1234") || !strings.Contains(err.Error(), "/src is at 9f8e7d6") {
		t.Fatal(err)
	}
	if slices.ContainsFunc(f.Calls, func(c string) bool { return strings.HasPrefix(c, "go build") }) {
		t.Fatalf("built anyway: %v", f.Calls)
	}
	f = host.NewFake().On(check, 0, "")
	dir, err := Build(context.Background(), f, ui.New(&ui.Recorder{}), "/src", "amd64", "flocc-setup")
	if dir != "" {
		defer os.RemoveAll(dir)
	}
	if !slices.ContainsFunc(f.Calls, func(c string) bool { return strings.HasPrefix(c, "go build") }) {
		t.Fatalf("%v %v", err, f.Calls)
	}
}

func pass(prefix string) Pass {
	return Pass{Machine: "laptop", Opts: opts, Prefix: prefix, Files: []string{"/bin/x/flocc-setup"},
		Calls: [][]string{{"--phase", "create-root"}}, Why: "root pass: sudo on laptop may ask for your password"}
}

func TestRunStagesUnderThePrefixAndSudoesEachCall(t *testing.T) {
	dir := "/tmp/flocc-create.Ab12Cd"
	f := host.NewFake().
		On(SSH(opts, false, "laptop", "mktemp -d /tmp/flocc-create.XXXXXX").String(), 0, dir+"\n").
		On("scp -q -o ControlMaster=auto /bin/x/flocc-setup laptop:"+dir+"/", 0, "")
	if _, err := Run(context.Background(), f, pass("/tmp/flocc-create")); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	if len(f.Interactives) != 1 {
		t.Fatalf("calls %v", f.Calls)
	}
	c := f.Interactives[0]
	want := "chmod 755 " + dir + "/flocc-setup && sudo " + dir + "/flocc-setup --phase create-root && rm -rf " + dir
	if c.Args[len(c.Args)-1] != want || !slices.Contains(c.Args, "-t") {
		t.Fatalf("args %q", c.Args)
	}
	if c.Why != pass("").Why || strings.Contains(c.Why, "flocc-setup") {
		t.Fatalf("why %q", c.Why)
	}
}

func TestRunHandsOffTheTerminalWithoutStdin(t *testing.T) {
	dir := "/tmp/flocc-create.Ab12Cd"
	f := host.NewFake().
		On(SSH(opts, false, "laptop", "mktemp -d /tmp/flocc-create.XXXXXX").String(), 0, dir+"\n").
		On("scp -q -o ControlMaster=auto /bin/x/flocc-setup laptop:"+dir+"/", 0, "")
	if _, err := Run(context.Background(), f, pass("/tmp/flocc-create")); err != nil {
		t.Fatal(err)
	}
	if c := f.Interactives[0]; c.Stdin != nil || c.Stdout == nil {
		t.Fatalf("stdin %v, stdout %v", c.Stdin, c.Stdout)
	}
}

func TestRunRefusesAStageOutsideThePrefix(t *testing.T) {
	f := host.NewFake().
		On(SSH(opts, false, "laptop", "mktemp -d /tmp/flocc-create.XXXXXX").String(), 0, "/tmp/flocc-cardinal.Ab12Cd\n")
	_, err := Run(context.Background(), f, pass("/tmp/flocc-create"))
	if err == nil || !strings.Contains(err.Error(), "mktemp on") {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "scp") {
			t.Fatalf("staged into a stage it should have refused: %s", c)
		}
	}
}

func TestRunNamesTheReportErrorAndTheLog(t *testing.T) {
	dir := "/tmp/flocc-create.Ab12Cd"
	line := SSH(opts, true, "laptop", "chmod 755 "+dir+"/flocc-setup && sudo "+dir+"/flocc-setup --phase create-root && rm -rf "+dir).String()
	f := host.NewFake().
		On(SSH(opts, false, "laptop", "mktemp -d /tmp/flocc-create.XXXXXX").String(), 0, dir+"\n").
		On("scp -q -o ControlMaster=auto /bin/x/flocc-setup laptop:"+dir+"/", 0, "").
		OnInteractiveOut(line, steps.RootReport{Error: "Firewalld: boom"}.Line()+"\n").
		OnInteractive(line, errors.New("exit status 1"))
	reports, err := Run(context.Background(), f, pass("/tmp/flocc-create"))
	if len(reports) != 1 || err == nil || !strings.Contains(err.Error(), "Firewalld: boom") || !strings.Contains(err.Error(), dir+"/setup.log") {
		t.Fatalf("%v %v", reports, err)
	}
}
