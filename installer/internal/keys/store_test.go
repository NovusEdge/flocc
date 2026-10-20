package keys

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// No test may reach the real keyring of the machine running it.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func TestFileStoreRoundTripsWithMode600(t *testing.T) {
	home := t.TempDir()
	s := fileStore{path: filepath.Join(home, ".config/flocc/secrets.toml")}
	if v, err := s.Get(Agent); err != nil || v != "" {
		t.Fatalf("%q %v", v, err)
	}
	if err := s.Set(Agent, "github_pat_a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(Linear, "lin_api_b"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(Agent); v != "github_pat_a" {
		t.Fatal(v)
	}
	st, _ := os.Stat(s.path)
	dir, _ := os.Stat(filepath.Dir(s.path))
	if st.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Fatalf("%o %o", st.Mode().Perm(), dir.Mode().Perm())
	}
}

func TestOpenUsesTheKeyringWhenItAnswers(t *testing.T) {
	keyring.MockInit()
	var notes bytes.Buffer
	s := Open(t.TempDir(), &notes)
	if s.Where() != "the OS keyring" || notes.Len() != 0 {
		t.Fatalf("%s %q", s.Where(), notes.String())
	}
	if v, err := s.Get(Publish); err != nil || v != "" {
		t.Fatalf("%q %v", v, err)
	}
	if err := s.Set(Publish, "github_pat_p"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(Publish); v != "github_pat_p" {
		t.Fatal(v)
	}
}

func TestOpenKeepsAnExistingSecretsFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config/flocc/secrets.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("ntfy_topic = 'flocc-x'\n"), 0o600)
	s := Open(home, &bytes.Buffer{})
	if s.Where() != path {
		t.Fatal(s.Where())
	}
	if v, _ := s.Get(Ntfy); v != "flocc-x" {
		t.Fatal(v)
	}
}

func TestOpenFallsBackWhenTheKeyringHangs(t *testing.T) {
	get, wait, limit := keyringGet, probeWait, probeLimit
	t.Cleanup(func() { keyringGet, probeWait, probeLimit = get, wait, limit })
	keyringGet = func(string, string) (string, error) { select {} }
	probeWait, probeLimit = 5*time.Millisecond, 50*time.Millisecond
	home := t.TempDir()
	var notes bytes.Buffer
	start := time.Now()
	s := Open(home, &notes)
	if time.Since(start) > time.Second {
		t.Fatal("Open waited on a hung keyring")
	}
	want := filepath.Join(home, ".config/flocc/secrets.toml")
	if s.Where() != want || !strings.Contains(notes.String(), want) || !strings.Contains(notes.String(), "unlock it if it asks") {
		t.Fatalf("%s %q", s.Where(), notes.String())
	}
}

func TestCheckPrefixRefusesAKeyInTheWrongSlot(t *testing.T) {
	for _, c := range []struct {
		slot  Slot
		value string
		want  string
	}{
		{Agent, "lin_api_x", "Linear API key"},
		{Linear, "github_pat_x", "GitHub token"},
		{Publish, "ghp_x", "classic"},
		{Linear, "abc", "starts with lin_api_"},
		{Agent, "abc", "starts with github_pat_"},
	} {
		if err := CheckPrefix(c.slot, c.value); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: %v", c.slot, c.value, err)
		}
	}
	for slot, ok := range map[Slot]string{Linear: "lin_api_x", Agent: "github_pat_x", Publish: "github_pat_y"} {
		if err := CheckPrefix(slot, ok); err != nil {
			t.Errorf("%s: %v", slot, err)
		}
	}
}
