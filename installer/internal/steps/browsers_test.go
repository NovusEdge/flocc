package steps

import (
	"context"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const dryRun = "npx -y playwright@1.64.0 install-deps --dry-run chromium"

func browsersEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Distro.Manager = host.Apt
	e.Answers.Playwright = "1.64.0"
	return e
}

func TestBrowsersIsDoneWhenNoVersionIsAskedFor(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, err := (Browsers{}).Check(context.Background(), e); !ok || err != nil || detail != "none asked for" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestBrowsersSkipsHostsWithoutApt(t *testing.T) {
	e := browsersEnv(t, host.NewFake())
	e.Distro.Manager = host.Pacman
	if ok, detail, _ := (Browsers{}).Check(context.Background(), e); !ok || detail != "apt hosts only" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestBrowsersCountsWhatTheDryRunListsAsMissing(t *testing.T) {
	f := host.NewFake().On(dryRun, 0, "Missing system dependencies (2):\n  fontconfig\n  libnss3\n")
	ok, detail, err := (Browsers{}).Check(context.Background(), browsersEnv(t, f))
	if ok || err != nil || detail != "2 missing packages" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestBrowsersIsDoneWhenNothingIsMissing(t *testing.T) {
	f := host.NewFake().On(dryRun, 0, "")
	if ok, _, _ := (Browsers{}).Check(context.Background(), browsersEnv(t, f)); !ok {
		t.Fatal("not done with nothing missing")
	}
}

// RunAll checks every step before Packages installs node, so a missing npx is not done, not an error.
func TestBrowsersCheckWithoutNpxIsNotDone(t *testing.T) {
	ok, detail, err := (Browsers{}).Check(context.Background(), browsersEnv(t, host.NewFake()))
	if ok || err != nil || detail == "" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestBrowsersApplyInstallsTheDeps(t *testing.T) {
	f := host.NewFake().On("npx -y playwright@1.64.0 install-deps chromium", 0, "")
	if err := (Browsers{}).Apply(context.Background(), browsersEnv(t, f)); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("npx -y playwright@1.64.0 install-deps chromium") {
		t.Fatalf("calls %q", f.Calls)
	}
}
