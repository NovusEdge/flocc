package steps

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/pelletier/go-toml/v2"
)

func TestPermissionsWritesTheHostFilesTable(t *testing.T) {
	e := testEnv(t, host.NewFake())
	want := answers.Permissions{Allow: []string{"Bash(uv sync --frozen)"}, Environment: []string{"Nothing here is production."}}
	e.Answers.Permissions = &want
	ctx := context.Background()
	if done, _, err := (Permissions{}).Check(ctx, e); done || err != nil {
		t.Fatalf("done=%v err=%v before the file exists", done, err)
	}
	if err := (Permissions{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	path := e.HomePath(".config/flocc/permissions.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got answers.Permissions
	if err := toml.Unmarshal(b, &got); err != nil || !slices.Equal(got.Allow, want.Allow) || !slices.Equal(got.Environment, want.Environment) {
		t.Fatalf("file holds %+v (%v)", got, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if done, _, _ := (Permissions{}).Check(ctx, e); !done {
		t.Fatal("want done once written")
	}
	e.Answers.Permissions.JudgeModel = "sonnet"
	if done, _, _ := (Permissions{}).Check(ctx, e); done {
		t.Fatal("a changed host file must be written again")
	}
}

func TestPermissionsRemovesTheFileWhenTheHostFileHasNone(t *testing.T) {
	e := testEnv(t, host.NewFake())
	ctx := context.Background()
	if done, _, _ := (Permissions{}).Check(ctx, e); !done {
		t.Fatal("no table and no file is done")
	}
	path := e.HomePath(".config/flocc/permissions.toml")
	if err := os.MkdirAll(e.HomePath(".config/flocc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("judge_model = \"opus\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if done, _, _ := (Permissions{}).Check(ctx, e); done {
		t.Fatal("a file the host file no longer has is not done")
	}
	if err := (Permissions{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
}
