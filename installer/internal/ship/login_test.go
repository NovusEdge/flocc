package ship

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const signIn = "https://claude.ai/oauth/authorize?code=true&state=abc"

func TestLoginRunsTheLoginPhaseOnATerminalOpensTheURLAndPrintsTheSummary(t *testing.T) {
	call := "ssh -t root@h sudo -iu flocc /home/flocc/.cache/flocc-setup/flocc-setup --phase login --email o@example.test"
	sum := Summary{Version: "flocc 0.4.0", Login: "full", Timer: true}
	f := host.NewFake().OnInteractiveOut(call, "Opening browser to sign in…\r\n\x1b[4m"+signIn+"\x1b[0m\r\nPaste code here if prompted > "+
		"\r\n\r\n"+sum.Line()+"\r\n")
	var out strings.Builder
	var opened []string
	err := login(context.Background(), f, "root@h", Dest{SSH: "root@h"}, "o@example.test", &out, func(u string) { opened = append(opened, u) })
	if err != nil || !f.Ran("interactive: "+call) {
		t.Fatalf("%v %v", err, f.Calls)
	}
	if !slices.Equal(opened, []string{signIn}) || !strings.Contains(out.String(), "Paste code here") {
		t.Fatalf("opened %v out %q", opened, out.String())
	}
	if strings.Contains(out.String(), "flocc-summary") || !strings.Contains(out.String(), "flocc 0.4.0\n") ||
		!strings.Contains(out.String(), fmt.Sprintf("  %-14s %s\n", "timer", "on")) {
		t.Fatalf("the summary line should be replaced by the printed summary: %q", out.String())
	}
}

func TestURLWatchJoinsChunksAndOpensEachURLOnce(t *testing.T) {
	var out strings.Builder
	var opened []string
	w := &urlWatch{out: &out, open: func(u string) { opened = append(opened, u) }, seen: map[string]bool{}}
	for _, chunk := range []string{"visit https://claude.ai/oa", "uth/authorize?code=true&state=abc", "\r\nagain " + signIn + "\n"} {
		w.Write([]byte(chunk))
	}
	if !slices.Equal(opened, []string{signIn}) {
		t.Fatalf("%v", opened)
	}
}

func TestLoginScrubsTheDaytonaToken(t *testing.T) {
	dest := Dest{SSH: "tok@gw", Token: "tok"}
	f := host.NewFake().OnInteractive("ssh -t tok@gw "+loginRemote(""), errors.New("ssh tok@gw failed"))
	err := login(context.Background(), f, "daytona:box", dest, "", &strings.Builder{}, func(string) {})
	if err == nil || strings.Contains(err.Error(), "tok@") {
		t.Fatalf("%v", err)
	}
}

func TestLoginSummaryNamesTheTypedTargetNotTheTokenedDestination(t *testing.T) {
	dest := Dest{SSH: "tok123@gw", Token: "tok123"}
	sum := Summary{Version: "flocc 0.4.0", Login: "none", LoginPending: true}
	f := host.NewFake().OnInteractiveOut("ssh -t tok123@gw "+loginRemote(""), sum.Line()+"\r\n")
	var out strings.Builder
	if err := login(context.Background(), f, "daytona:box", dest, "", &out, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "flocc login -H daytona:box") || strings.Contains(out.String(), "tok123") || strings.Contains(out.String(), "<host>") {
		t.Fatalf("%q", out.String())
	}
}

func TestLoginSummaryNamesTheHostWhenGivenOne(t *testing.T) {
	o := LoginOptions{Target: "daytona:box", Name: "mybox"}
	if o.label() != "mybox" || (LoginOptions{Target: "root@h"}).label() != "root@h" {
		t.Fatalf("%q", o.label())
	}
	dest := Dest{SSH: "tok123@gw", Token: "tok123"}
	sum := Summary{Version: "flocc 0.4.0", Login: "none", LoginPending: true}
	f := host.NewFake().OnInteractiveOut("ssh -t tok123@gw "+loginRemote(""), sum.Line()+"\r\n")
	var out strings.Builder
	if err := login(context.Background(), f, o.label(), dest, "", &out, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "flocc login -H mybox\n") || strings.Contains(out.String(), "daytona:") || strings.Contains(out.String(), "tok123") {
		t.Fatalf("%q", out.String())
	}
}

func TestLoginRefusesAnUnsafeTargetAndEmail(t *testing.T) {
	var u usageError
	for _, o := range []LoginOptions{{Target: "h;id"}, {Target: "-oProxyCommand=x"}, {Target: "root@h", Email: "a@b;id"}} {
		if err := Login(context.Background(), o); !errors.As(err, &u) {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestValidEmail(t *testing.T) {
	if !ValidEmail("o@example.test") || ValidEmail("a@b;id") || ValidEmail("$(id)@x") {
		t.Fatal("wrong verdict")
	}
}
