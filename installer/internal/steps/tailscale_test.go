package steps

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	tsKey      = "tskey-auth-k1-MINTED"
	daytonaUp  = "tailscale up --ssh --hostname=box --advertise-tags=tag:flocc-host --auth-key=file:/dev/stdin"
	daytonaEnv = "sh -c command -v tailscale"
)

func keyedEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Answers.Hostname = "box"
	scriptedSecrets(e, &answers.Secrets{TailscaleAuthKey: tsKey})
	return e
}

func TestTailscaleJoinsTaggedWithTheScriptedKeyOnStdin(t *testing.T) {
	f := host.NewFake().On(daytonaEnv, 0, "/usr/bin/tailscale").
		On("tailscale status --json", 0, `{"BackendState":"NeedsLogin"}`).On(daytonaUp, 0, "")
	if err := (Tailscale{}).Apply(context.Background(), keyedEnv(t, f)); err != nil {
		t.Fatal(err)
	}
	if got := f.Stdins[daytonaUp]; got != tsKey {
		t.Errorf("stdin %q; calls %v", got, f.Calls)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, tsKey) || strings.HasPrefix(c, "interactive: ") {
			t.Errorf("call %q", c)
		}
	}
}

func TestTailscaleKeyIsScrubbedFromAFailure(t *testing.T) {
	f := host.NewFake().On(daytonaEnv, 0, "/usr/bin/tailscale").
		On("tailscale status --json", 0, `{"BackendState":"NeedsLogin"}`).On(daytonaUp, 1, "invalid key "+tsKey)
	err := Tailscale{}.Apply(context.Background(), keyedEnv(t, f))
	if err == nil || strings.Contains(err.Error(), tsKey) {
		t.Fatal(err)
	}
}

func TestTailscaleKeyDoesNotRejoinARunningNode(t *testing.T) {
	f := host.NewFake().On(daytonaEnv, 0, "/usr/bin/tailscale").
		On("tailscale status --json", 0, `{"BackendState":"Running"}`).On("tailscale set --ssh", 0, "")
	if err := (Tailscale{}).Apply(context.Background(), keyedEnv(t, f)); err != nil {
		t.Fatal(err)
	}
	if f.Ran(daytonaUp) {
		t.Errorf("joined again: %v", f.Calls)
	}
}
