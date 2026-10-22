package ship

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestSourceRunUsesTheCachedDestAndScrubs(t *testing.T) {
	const token = "SECRETTOKEN"
	minted := 0
	src := newSource("", t.TempDir(), t.TempDir())
	src.cache = newDestCache(func(context.Context, FleetHost) (Dest, error) {
		minted++
		return Dest{SSH: token + "@ssh.app.daytona.io", Token: token}, nil
	})
	args := []string{"peek", "NF-240", "it's here"}
	want := host.Cmd{Name: "ssh", Args: []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-T",
		token + "@ssh.app.daytona.io", asServiceUser(FloccLine(args))}}
	f := &lockedFake{Fake: host.NewFake()}
	f.Responses[want.String()] = host.Result{Stdout: "out " + token, Stderr: "err " + token, Code: 255}
	src.run = f
	h := FleetHost{Name: "sb", Target: "daytona:sb", State: "started", Daytona: true}

	res, err := src.Run(context.Background(), h, args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 255 || res.Stdout != "out <token>" || res.Stderr != "err <token>" {
		t.Errorf("result %+v", res)
	}
	if len(f.Calls) != 1 || f.Calls[0] != want.String() {
		t.Errorf("ran %q, want %q", f.Calls, want.String())
	}
	// 255 is ssh itself failing, so the cached token may be revoked.
	if _, err := src.Run(context.Background(), h, args); err != nil {
		t.Fatal(err)
	}
	if minted != 2 {
		t.Errorf("minted %d tokens over two runs after an exit 255, want 2", minted)
	}
}

func TestSourceRunKeepsTheCachedDestAfterAnOrdinaryExit(t *testing.T) {
	minted := 0
	src := newSource("", t.TempDir(), t.TempDir())
	src.cache = newDestCache(func(context.Context, FleetHost) (Dest, error) {
		minted++
		return Dest{SSH: "t@h", Token: "t"}, nil
	})
	f := &lockedFake{Fake: host.NewFake()}
	src.run = f
	h := FleetHost{Name: "sb", Target: "daytona:sb", State: "started", Daytona: true}
	for range 2 {
		res, err := src.Run(context.Background(), h, []string{"doctor", "--json"})
		if err != nil || res.Code != 127 {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if minted != 1 {
		t.Errorf("minted %d, want 1", minted)
	}
	if !strings.Contains(f.Calls[0], "ConnectTimeout=10") {
		t.Errorf("%q", f.Calls[0])
	}
}
