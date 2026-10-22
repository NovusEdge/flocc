package keys

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkout(t *testing.T, module, env string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("// c\n"+module+"\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func noEnv(string) string { return "" }

func TestFromEnvReadsTheFileInAFloccCheckout(t *testing.T) {
	root := checkout(t, floccModule, `# keys
export FLOCC_LINEAR_API_KEY="lin_api_a"
FLOCC_GITHUB_AGENT_TOKEN='github_pat_a'
  FLOCC_GITHUB_PUBLISH_TOKEN =  github_pat_p

FLOCC_NTFY_TOPIC=
OTHER=ignored
not a pair
FLOCC_TAILSCALE_OAUTH_SECRET=tskey-client-s
`)
	got := FromEnv(root, noEnv, &bytes.Buffer{})
	want := map[Slot]Taken{
		Linear:    {"lin_api_a", ".env"},
		Agent:     {"github_pat_a", ".env"},
		Publish:   {"github_pat_p", ".env"},
		Tailscale: {"tskey-client-s", ".env"},
	}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for s, w := range want {
		if got[s] != w {
			t.Errorf("%s: %v, want %v", s, got[s], w)
		}
	}
}

func TestTheEnvironmentOverridesTheFileAndAnEmptyValueIsUnset(t *testing.T) {
	root := checkout(t, floccModule, "FLOCC_LINEAR_API_KEY=lin_api_file\nFLOCC_GITHUB_AGENT_TOKEN=github_pat_file\n")
	env := map[string]string{"FLOCC_LINEAR_API_KEY": "lin_api_env", "FLOCC_GITHUB_AGENT_TOKEN": "  "}
	got := FromEnv(root, func(k string) string { return env[k] }, &bytes.Buffer{})
	if got[Linear] != (Taken{"lin_api_env", "the environment"}) || got[Agent] != (Taken{"github_pat_file", ".env"}) {
		t.Fatalf("%v", got)
	}
}

func TestTheFileCountsOnlyInTheFloccCheckout(t *testing.T) {
	root := checkout(t, "module example.com/other", "FLOCC_LINEAR_API_KEY=lin_api_file\n")
	env := func(k string) string { return map[string]string{"FLOCC_NTFY_TOPIC": "t"}[k] }
	got := FromEnv(root, env, &bytes.Buffer{})
	if len(got) != 1 || got[Ntfy].From != "the environment" {
		t.Fatalf("%v", got)
	}
	if got := FromEnv("", env, &bytes.Buffer{}); len(got) != 1 {
		t.Fatalf("no root: %v", got)
	}
	if got := FromEnv(t.TempDir(), noEnv, &bytes.Buffer{}); len(got) != 0 {
		t.Fatalf("no go.mod: %v", got)
	}
}

func TestTheTailscaleValueJoinsTheClientIDWhenBothAreSet(t *testing.T) {
	for _, c := range []struct{ env, want string }{
		{"FLOCC_TAILSCALE_OAUTH_SECRET=tskey-client-s\nFLOCC_TAILSCALE_OAUTH_CLIENT_ID=kid\n", "kid:tskey-client-s"},
		{"FLOCC_TAILSCALE_OAUTH_SECRET=tskey-client-s\n", "tskey-client-s"},
		{"FLOCC_TAILSCALE_OAUTH_CLIENT_ID=kid\n", ""},
	} {
		got := FromEnv(checkout(t, floccModule, c.env), noEnv, &bytes.Buffer{})
		if got[Tailscale].Value != c.want {
			t.Errorf("%q: %q, want %q", c.env, got[Tailscale].Value, c.want)
		}
	}
}

func TestAWorldReadableEnvFileIsSetTo600WithANote(t *testing.T) {
	root := checkout(t, floccModule, "FLOCC_NTFY_TOPIC=t\n")
	path := filepath.Join(root, ".env")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var note bytes.Buffer
	FromEnv(root, noEnv, &note)
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	if !strings.Contains(note.String(), path+" was mode 644; set it to 600.") {
		t.Fatalf("%q", note.String())
	}
}

func TestSeedEnvCopiesTheExampleOnceAndNeverOverwrites(t *testing.T) {
	root := checkout(t, floccModule, "")
	example := filepath.Join(root, ".env.example")
	if err := os.WriteFile(example, []byte("FLOCC_NTFY_TOPIC=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	made, err := SeedEnv(root)
	if err != nil || !made {
		t.Fatal(made, err)
	}
	path := filepath.Join(root, ".env")
	b, _ := os.ReadFile(path)
	if st, _ := os.Stat(path); string(b) != "FLOCC_NTFY_TOPIC=\n" || st.Mode().Perm() != 0o600 {
		t.Fatalf("%q %o", b, st.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("FLOCC_NTFY_TOPIC=mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if made, err := SeedEnv(root); made || err != nil {
		t.Fatal(made, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "FLOCC_NTFY_TOPIC=mine\n" {
		t.Fatalf("overwritten: %q", b)
	}
}

func TestSeedEnvLeavesOtherProjectsAndMissingExamplesAlone(t *testing.T) {
	other := checkout(t, "module example.com/other", "")
	os.WriteFile(filepath.Join(other, ".env.example"), []byte("A=\n"), 0o644)
	bare := checkout(t, floccModule, "")
	for _, root := range []string{other, bare, ""} {
		if made, err := SeedEnv(root); made || err != nil {
			t.Errorf("%q: %v %v", root, made, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".env")); root != "" && err == nil {
			t.Errorf("%q: .env made", root)
		}
	}
}
