package steps

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const flocc = "/home/flocc/.local/bin/flocc status --json"

func TestOldWorktreesGoOnceNothingIsClaimed(t *testing.T) {
	f := host.NewFake().On(flocc, 0, `{"tick_lock": "/l", "claims": []}`).OnName("git", 0, "")
	e := testEnv(t, f)
	os.MkdirAll(e.HomePath("worktrees/NF-1"), 0o755)
	os.MkdirAll(e.HomePath("src/backend"), 0o755)
	if done, _, _ := (OldWorktrees{}).Check(context.Background(), e); done {
		t.Fatal("done with ~/worktrees present")
	}
	if err := (OldWorktrees{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.HomePath("worktrees")); err == nil {
		t.Fatal("not removed")
	}
	if !f.Ran("git -C /home/flocc/src/backend worktree prune") {
		t.Fatal(f.Calls)
	}
}

func TestOldWorktreesWaitForLiveClaims(t *testing.T) {
	f := host.NewFake().On(flocc, 0, `{"tick_lock": "/l", "claims": [{"issue": "NF-1", "phase": "impl"}]}`)
	e := testEnv(t, f)
	os.MkdirAll(e.HomePath("worktrees/NF-1"), 0o755)
	err := (OldWorktrees{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "NF-1") {
		t.Fatalf("%v", err)
	}
}
