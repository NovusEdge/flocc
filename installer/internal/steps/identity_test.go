package steps

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/identity"
)

func identityEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Stage = t.TempDir()
	want := identity.Identity{Name: "Ada L", Email: "ada@example.test"}
	if err := want.Write(filepath.Join(e.Stage, "identity.json")); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestIdentityCheckAndApply(t *testing.T) {
	f := host.NewFake().
		On("git config --global user.name", 0, "agent\n").
		On("git config --global user.email", 1, "")
	e := identityEnv(t, f)
	if done, _, err := (Identity{}).Check(context.Background(), e); done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	f.On("git config --global user.name Ada L", 0, "").On("git config --global user.email ada@example.test", 0, "")
	if err := (Identity{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	f.On("git config --global user.name", 0, "Ada L\n").On("git config --global user.email", 0, "ada@example.test\n")
	if done, _, _ := (Identity{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
}

func TestIdentityCheckPassesWhenNothingWasShipped(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.Stage = t.TempDir()
	if done, _, err := (Identity{}).Check(context.Background(), e); !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
}
