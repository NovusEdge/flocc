package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// flocc is a checkout of this module with the given .env, which d runs inside.
func flocc(t *testing.T, d *deps, env string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/NovusEdge/flocc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d.repoRoot = func() (string, error) { return root, nil }
	return root
}

func TestKeysTakesAChangedTokenFromDotEnvAndPushesItAfterAYes(t *testing.T) {
	p := &hostScript{confirms: []bool{true}}
	d, f, rec := rotateDeps(t, p)
	flocc(t, d, "FLOCC_GITHUB_AGENT_TOKEN=github_pat_agent2\nFLOCC_GITHUB_PUBLISH_TOKEN=github_pat_publish\n")
	run(context.Background(), d, []string{"keys"})
	if v, _ := d.store().Get(keys.Agent); v != "github_pat_agent2" {
		t.Fatal(v)
	}
	if r := row(rec, "Check GitHub agent token (read-only) from .env"); r.State != ui.Done {
		t.Fatalf("%+v", rec.Rows())
	}
	if in, ok := pushedTo(f, "root@a", "gh-agent.token"); !ok || in != "github_pat_agent2\n" {
		t.Fatalf("runs %v stdins %q", f.runs, f.stdins)
	}
	if _, ok := pushedTo(f, "root@a", "gh-publish.token"); ok {
		t.Fatal("the publish token did not change, so it must not be pushed")
	}
	if row(rec, "Push keys to a").State != ui.Done || row(rec, "Push keys to b").State != ui.Failed {
		t.Fatalf("%+v", rec.Rows())
	}
	if got := sessionText(rec); !strings.Contains(got, "revoke the old token") || !strings.Contains(got, "run flocc ship -H <host>") {
		t.Fatal(got)
	}
	if len(p.confirms) != 0 {
		t.Fatal("the push was not offered")
	}
}

func TestKeysStoresATokenFromDotEnvAndPushesNothingOnANo(t *testing.T) {
	d, f, _ := rotateDeps(t, &hostScript{confirms: []bool{false}})
	flocc(t, d, "export FLOCC_GITHUB_AGENT_TOKEN='github_pat_agent3'\n")
	run(context.Background(), d, []string{"keys"})
	if v, _ := d.store().Get(keys.Agent); v != "github_pat_agent3" {
		t.Fatal(v)
	}
	if len(f.captured()) != 0 {
		t.Fatalf("pushed: %v", f.runs)
	}
}

func TestKeysDoesNotOfferAPushForAKeyThatDidNotChange(t *testing.T) {
	d, f, rec := rotateDeps(t, &hostScript{})
	flocc(t, d, "FLOCC_GITHUB_AGENT_TOKEN=github_pat_agent\n")
	run(context.Background(), d, []string{"keys"})
	if r := row(rec, "Check GitHub agent token (read-only) from .env"); r.Name != "" || len(f.captured()) != 0 {
		t.Fatalf("%+v", rec.Rows())
	}
}

func TestAFailingDotEnvKeyIsSkippedMaskedAndLeavesTheStoredKey(t *testing.T) {
	d, f, rec := rotateDeps(t, &hostScript{})
	flocc(t, d, "FLOCC_LINEAR_API_KEY=github_pat_SECRETVALUE0123456789\n")
	run(context.Background(), d, []string{"keys"})
	r := row(rec, "Check Linear API key from .env")
	if r.State != ui.Skipped || !strings.Contains(r.Detail, "github_pat_…6789, 32 characters") {
		t.Fatalf("%+v", rec.Rows())
	}
	if v, _ := d.store().Get(keys.Linear); v != "lin_api_k" {
		t.Fatal(v)
	}
	if got := sessionText(rec); strings.Contains(got, "SECRETVALUE") {
		t.Fatalf("the value reached the session:\n%s", got)
	}
	if len(f.captured()) != 0 {
		t.Fatal("pushed")
	}
}

func TestDotEnvOutsideAFloccCheckoutIsIgnored(t *testing.T) {
	d, _, rec := rotateDeps(t, &hostScript{})
	root := flocc(t, d, "FLOCC_LINEAR_API_KEY=lin_api_new\n")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(context.Background(), d, []string{"keys"})
	if v, _ := d.store().Get(keys.Linear); v != "lin_api_k" || row(rec, "Check Linear API key from .env").Name != "" {
		t.Fatal(v, rec.Rows())
	}
}

func TestEnvKeysFailsOnAFailedStore(t *testing.T) {
	d, _, _ := rotateDeps(t, &hostScript{})
	flocc(t, d, "FLOCC_GITHUB_AGENT_TOKEN=github_pat_agent\n")
	d.store = func() keys.Store { return failingStore{} }
	if err := envKeys(context.Background(), d, keys.Slots); err == nil {
		t.Fatal("no error")
	}
}

type failingStore struct{}

func (failingStore) Get(keys.Slot) (string, error) { return "", nil }
func (failingStore) Set(keys.Slot, string) error   { return errors.New("keyring locked") }
func (failingStore) Where() string                 { return "nowhere" }

func TestSeedEnvMakesDotEnvFromTheExampleAndSaysSo(t *testing.T) {
	d, _, _ := testDeps(t)
	rec := &ui.Recorder{}
	d.ui = ui.New(rec)
	root := flocc(t, d, "")
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("FLOCC_LINEAR_API_KEY=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedEnv(d, root)
	st, err := os.Stat(filepath.Join(root, ".env"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal(err, st)
	}
	if got := sessionText(rec); !strings.Contains(got, filepath.Join(root, ".env")) {
		t.Fatalf("the note does not name the file:\n%s", got)
	}
	rec.Events = nil
	seedEnv(d, root)
	if len(rec.Events) != 0 {
		t.Fatalf("a second run said something: %+v", rec.Events)
	}
}
