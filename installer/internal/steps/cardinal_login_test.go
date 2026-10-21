package steps

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestCardinalLoginStopsLogsInConsentsAndStarts(t *testing.T) {
	f := host.NewFake().
		OnSeq(userctlPrefix+"is-active --quiet cardinal.service", host.Result{Code: 0}).
		On(userctlPrefix+"stop cardinal.service", 0, "").
		OnSeq("/home/cardinal/.local/bin/claude auth status",
			host.Result{Stdout: `{"loggedIn":false}`},
			host.Result{Stdout: `{"loggedIn":true,"authMethod":"claude.ai"}`}).
		On(userctlPrefix+"reset-failed cardinal.service", 0, "").
		On(userctlPrefix+"start cardinal.service", 0, "").
		On("tmux -L cardinal has-session -t =cardinal", 0, "")
	e := userEnv(t, f)
	var consentDir string
	err := CardinalLogin(context.Background(), e, CardinalLoginOptions{User: "cardinal", Email: "pool@x.test",
		Consent: func(_ context.Context, c host.Cmd) error { consentDir = c.Dir; return nil }})
	if err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	login := slices.Index(f.Calls, "interactive: /home/cardinal/.local/bin/claude auth login --claudeai --email pool@x.test")
	if slices.Index(f.Calls, userctlPrefix+"stop cardinal.service") > login || login < 0 || slices.Index(f.Calls, userctlPrefix+"start cardinal.service") < login {
		t.Fatalf("calls %v", f.Calls)
	}
	if consentDir != "/home/cardinal" {
		t.Fatalf("consent ran in %q", consentDir)
	}
}

func TestCardinalLoginRefusesATokenOnlyLogin(t *testing.T) {
	f := host.NewFake().On("/home/cardinal/.local/bin/claude auth status", 0, `{"loggedIn":true,"authMethod":"oauth_token"}`)
	e := userEnv(t, f)
	err := CardinalLogin(context.Background(), e, CardinalLoginOptions{User: "cardinal", Consent: func(context.Context, host.Cmd) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "token-only") {
		t.Fatalf("err = %v", err)
	}
	if f.Ran(userctlPrefix + "start cardinal.service") {
		t.Fatal("started without a full login")
	}
}
