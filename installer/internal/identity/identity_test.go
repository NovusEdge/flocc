package identity

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

func TestFromRepoReadsNameAndEmail(t *testing.T) {
	f := host.NewFake().
		On("git -C /d config user.name", 0, "Ada L\n").
		On("git -C /d config user.email", 0, "ada@example.test\n")
	got, err := FromRepo(context.Background(), f, "/d")
	if err != nil || got != (Identity{"Ada L", "ada@example.test"}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFromRepoFailsWhenEitherIsMissing(t *testing.T) {
	f := host.NewFake().
		On("git -C /d config user.name", 0, "Ada L\n").
		On("git -C /d config user.email", 1, "")
	if _, err := FromRepo(context.Background(), f, "/d"); err == nil || !strings.Contains(err.Error(), "user.email") {
		t.Fatalf("%v", err)
	}
}

func TestWriteLoadRoundTripAndMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "identity.json")
	if got, err := Load(p); err != nil || got != (Identity{}) {
		t.Fatalf("%+v %v", got, err)
	}
	want := Identity{"Ada L", "ada@example.test"}
	if err := want.Write(p); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p); err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
}
