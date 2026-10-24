package steps

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

const a2aHead = "3333333333333333333333333333333333333333"

func a2aEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Answers.Secrets = &answers.Secrets{A2A: &answers.A2AFile{
		Name: "flocc-nimble", Token: "tok", Accept: map[string]string{"owner-tok": "owner"},
		Hosts: map[string]answers.A2APeer{"flocc-nimble": {URL: "http://flocc-nimble:7443", Repos: []string{"nimblefox"}}},
	}}
	return e
}

func a2aFake() *host.Fake {
	return host.NewFake().
		On(userctlPrefix+"daemon-reload", 0, "").
		On(userctlPrefix+"enable flocc-a2a.service", 0, "").
		On(userctlPrefix+"restart flocc-a2a.service", 0, "").
		On(userctlPrefix+"is-active --quiet flocc-a2a.service", 0, "").
		On("git -C /home/flocc/flocc rev-parse HEAD", 0, a2aHead+"\n")
}

func writeServed(t *testing.T, e *Env, head string) {
	t.Helper()
	p := e.HomePath(".local/state/flocc/a2a-serve.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(`{"head":"`+head+`","at":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestA2AWritesTheFileAndStartsTheService(t *testing.T) {
	f := a2aFake()
	e := a2aEnv(t, f)
	if err := (A2A{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	path := e.HomePath(".config/flocc/a2a.toml")
	b, err := os.ReadFile(path)
	want, _ := toml.Marshal(*e.Answers.Secrets.A2A)
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("file %q (%v)", b, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	unit, _ := deploy.Files.ReadFile(deploy.A2AUnit)
	if got, _ := os.ReadFile(e.HomePath(".config/systemd/user/flocc-a2a.service")); !bytes.Equal(got, unit) {
		t.Fatal("unit differs from the embedded copy")
	}
	for _, c := range []string{"daemon-reload", "enable flocc-a2a.service", "restart flocc-a2a.service"} {
		if !f.Ran(userctlPrefix + c) {
			t.Errorf("did not run systemctl --user %s", c)
		}
	}
}

func TestA2ACheckPassesWhenServingTheCurrentHead(t *testing.T) {
	e := a2aEnv(t, a2aFake())
	ctx := context.Background()
	if err := (A2A{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	writeServed(t, e, a2aHead)
	if done, detail, err := (A2A{}).Check(ctx, e); !done || err != nil {
		t.Fatalf("done=%v detail=%q err=%v", done, detail, err)
	}
}

func TestA2AApplyWaitsForTheServerToRecordItsHead(t *testing.T) {
	e := a2aEnv(t, a2aFake())
	ctx := context.Background()
	slept := 0
	e.Sleep = func(time.Duration) {
		slept++
		if slept == 2 {
			writeServed(t, e, a2aHead)
		}
	}
	if err := (A2A{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if done, detail, err := (A2A{}).Check(ctx, e); !done || err != nil || slept != 2 {
		t.Fatalf("done=%v detail=%q err=%v slept=%d", done, detail, err, slept)
	}
}

func TestA2ACheckFailsOnANewHead(t *testing.T) {
	e := a2aEnv(t, a2aFake())
	ctx := context.Background()
	if err := (A2A{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	writeServed(t, e, "4444444444444444444444444444444444444444")
	if done, detail, _ := (A2A{}).Check(ctx, e); done || !strings.Contains(detail, "serving an older flocc") {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
}

func TestA2AWithoutAnswersRemovesNothing(t *testing.T) {
	f := host.NewFake()
	e := testEnv(t, f)
	ctx := context.Background()
	if done, detail, err := (A2A{}).Check(ctx, e); !done || err != nil || !strings.Contains(detail, "not shipped from a desktop with A2A") {
		t.Fatalf("done=%v detail=%q err=%v", done, detail, err)
	}
	path := e.HomePath(".config/flocc/a2a.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("name = \"x\"\n"), 0o600)
	if err := (A2A{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil || len(f.Calls) != 0 {
		t.Fatalf("stat %v, calls %v", err, f.Calls)
	}
}
