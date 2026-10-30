package steps

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestAgentAccountCreatesTheUserGroupAndDirectories(t *testing.T) {
	f := host.NewFake()
	f.OnSeq("id -u flocc-agent", host.Result{Code: 1}, host.Result{Stdout: "1001\n"})
	f.On("id -nG flocc", 0, "flocc\n")
	f.On("id -nG flocc-agent", 0, "flocc-agent\n")
	f.On("id -u flocc", 0, "1000\n")
	f.OnName("groupadd", 0, "").OnName("useradd", 0, "").OnName("usermod", 0, "")
	f.OnName("chgrp", 0, "").OnName("chmod", 0, "").OnName("install", 0, "").OnName("chown", 0, "")
	f.OnName("loginctl", 0, "").OnName("systemctl", 0, "")
	e := testEnv(t, f)
	if err := (AgentAccount{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"groupadd -f flocc-work",
		"useradd --create-home --shell /bin/bash flocc-agent",
		"usermod -aG flocc-work flocc",
		"chmod 710 /home/flocc-agent",
		"install -d -o flocc-agent -g flocc-work -m 2750 /home/flocc-agent/worktrees",
		"chmod 2750 /home/flocc-agent/state",
		"loginctl enable-linger flocc-agent",
		"systemctl try-restart user@1000.service",
	} {
		if !f.Ran(want) {
			t.Errorf("did not run %q; ran %v", want, f.Calls)
		}
	}
}

func TestAgentAccountInLoopModeRestartsTheTmuxServerOnlyWhenTheGroupIsNew(t *testing.T) {
	for _, c := range []struct {
		serviceGroups string
		kill          bool
	}{{"flocc\n", true}, {"flocc flocc-work\n", false}} {
		f := host.NewFake()
		f.On("id -u flocc-agent", 0, "1001\n").On("id -nG flocc", 0, c.serviceGroups).On("id -nG flocc-agent", 0, "flocc-agent\n")
		f.OnName("groupadd", 0, "").OnName("usermod", 0, "").OnName("chgrp", 0, "").OnName("chmod", 0, "")
		f.OnName("install", 0, "").OnName("chown", 0, "").OnName("runuser", 0, "")
		e := testEnv(t, f)
		e.Mode = Loop
		if err := (AgentAccount{}).Apply(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if got := f.Ran("runuser -u flocc -- tmux -L flocc kill-server"); got != c.kill {
			t.Errorf("groups %q: killed=%v; ran %v", c.serviceGroups, got, f.Calls)
		}
		if f.Ran("systemctl") {
			t.Errorf("loop mode ran systemctl: %v", f.Calls)
		}
	}
}

func TestAgentAccountRefusesAnAgentInAPrivilegedGroup(t *testing.T) {
	f := host.NewFake()
	f.On("id -u flocc-agent", 0, "1001\n").On("id -nG flocc", 0, "flocc flocc-work\n")
	f.On("id -nG flocc-agent", 0, "flocc-agent wheel\n")
	done, detail, _ := (AgentAccount{}).Check(context.Background(), testEnv(t, f))
	if done || detail != "flocc-agent is in group wheel" {
		t.Fatalf("%v %q", done, detail)
	}
}

func TestAgentAccessInstallsTheWrapperAndAValidatedRule(t *testing.T) {
	f := host.NewFake().OnName("visudo", 0, "").OnName("install", 0, "")
	e := testEnv(t, f)
	if err := (AgentAccess{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	want, _ := deploy.Files.ReadFile("flocc-agent-run")
	got, _ := os.ReadFile(e.Path(AgentRun))
	if string(got) != string(want) {
		t.Fatal("wrapper not written")
	}
	if st, _ := os.Stat(e.Path(sudoersFile)); st == nil || st.Mode().Perm() != 0o440 {
		t.Fatalf("sudoers file: %v", st)
	}
	if !f.Ran("visudo -cf " + sudoersFile + ".new") {
		t.Fatalf("rule not validated: %v", f.Calls)
	}
	want, _ = deploy.Files.ReadFile("managed-settings.json")
	got, _ = os.ReadFile(e.Path(ManagedSettingsFile))
	if string(got) != string(want) {
		t.Fatal("managed settings not written")
	}
	if st, _ := os.Stat(e.Path(ManagedSettingsFile)); st == nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("managed settings: %v", st)
	}
}

func TestAgentAccountRefusesTheDockerGroup(t *testing.T) {
	f := host.NewFake()
	f.On("id -u flocc-agent", 0, "1001\n").On("id -nG flocc", 0, "flocc flocc-work\n")
	f.On("id -nG flocc-agent", 0, "flocc-agent docker\n")
	done, detail, _ := (AgentAccount{}).Check(context.Background(), testEnv(t, f))
	if done || detail != "flocc-agent is in group docker" {
		t.Fatalf("%v %q", done, detail)
	}
}

func TestAgentAccessLeavesNoRuleWhenVisudoRejectsIt(t *testing.T) {
	f := host.NewFake().OnName("visudo", 1, "")
	e := testEnv(t, f)
	if err := (AgentAccess{}).Apply(context.Background(), e); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(e.Path(sudoersFile)); err == nil {
		t.Fatal("rejected rule installed")
	}
	if _, err := os.Stat(e.Path(sudoersFile + ".new")); err == nil {
		t.Fatal("rejected rule left behind")
	}
}

func TestClaudeHomeMovesTheLoginToFloccAgent(t *testing.T) {
	f := host.NewFake().OnName("mv", 0, "").OnName("chown", 0, "").OnName("rm", 0, "")
	e := testEnv(t, f)
	os.MkdirAll(e.Path("/home/flocc/.claude"), 0o700)
	os.WriteFile(e.Path("/home/flocc/.claude.json"), []byte("{}"), 0o600)
	os.MkdirAll(e.Path(AgentHome), 0o710)
	if done, _, _ := (ClaudeHome{}).Check(context.Background(), e); done {
		t.Fatal("done before the move")
	}
	if err := (ClaudeHome{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mv -- /home/flocc/.claude /home/flocc-agent/.claude",
		"chown -R -h flocc-agent:flocc-agent /home/flocc-agent/.claude",
		"mv -- /home/flocc/.claude.json /home/flocc-agent/.claude.json",
		"rm -rf -- /home/flocc-agent/.claude/plugins/installed_plugins.json /home/flocc-agent/.claude/plugins/known_marketplaces.json /home/flocc-agent/.claude/plugins/cache /home/flocc-agent/.claude/plugins/marketplaces",
	} {
		if !f.Ran(want) {
			t.Errorf("did not run %q; ran %v", want, f.Calls)
		}
	}
}

func TestClaudeHomeRefusesALink(t *testing.T) {
	e := testEnv(t, host.NewFake())
	os.MkdirAll(e.Path("/home/flocc"), 0o700)
	os.Symlink("/etc", e.Path("/home/flocc/.claude"))
	if err := (ClaudeHome{}).Apply(context.Background(), e); err == nil {
		t.Fatal("followed a link")
	}
}

func TestTheUnitsAreSplitBetweenTheUsers(t *testing.T) {
	if !slices.Equal(deploy.AgentUnits, []string{"flocc-tmux.service"}) || slices.Contains(deploy.Units, "flocc-tmux.service") {
		t.Fatal(deploy.Units, deploy.AgentUnits)
	}
	b, _ := deploy.Files.ReadFile("flocc-tmux.service")
	if !strings.Contains(string(b), "\nUMask=0027\n") {
		t.Fatal("tmux unit lacks UMask=0027")
	}
}
