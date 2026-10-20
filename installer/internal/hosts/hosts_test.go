package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/pelletier/go-toml/v2"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLoadAndTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hosts")
	if err := Save(dir, Host{Name: "flocc-nimble", SSH: "root@flocc-nimble"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "flocc-nimble.toml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	h, err := Load(dir, "flocc-nimble")
	if err != nil || h.Name != "flocc-nimble" || h.Target() != "root@flocc-nimble" {
		t.Fatalf("%+v %v", h, err)
	}
	if got := (Host{Name: "sb", Daytona: "sb"}).Target(); got != "daytona:sb" {
		t.Fatal(got)
	}
	if got := Dir("/home/u"); got != "/home/u/.config/flocc/hosts" {
		t.Fatal(got)
	}
	if err := Save(dir, Host{Name: "../x", SSH: "x"}); err == nil {
		t.Fatal("a name outside dir was saved")
	}
}

func TestListReturnsGoodHostsByNameAndNamesTheBadOnes(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "zed.toml"), `ssh = "root@zed"`+"\n")
	write(t, filepath.Join(dir, "sb.toml"), `daytona = "sb"`+"\n")
	write(t, filepath.Join(dir, "evil.toml"), `ssh = "-oProxyCommand=id"`+"\n")
	write(t, filepath.Join(dir, "both.toml"), "ssh = \"a\"\ndaytona = \"b\"\n")
	write(t, filepath.Join(dir, "broken.toml"), "ssh = \n")
	write(t, filepath.Join(dir, "notes.txt"), "not a host\n")
	list, err := List(dir)
	if len(list) != 2 || list[0].Name != "sb" || list[1].Name != "zed" {
		t.Fatalf("%+v", list)
	}
	for _, bad := range []string{"both.toml", "broken.toml", "evil.toml"} {
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("the error does not name %s: %v", bad, err)
		}
	}
	if list, err := List(filepath.Join(dir, "none")); list != nil || err != nil {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestHostFileKeepsTheAnswersFlatAndNeverASecret(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hosts")
	h := Host{Name: "flocc-nimble", SSH: "root@flocc-nimble", Answers: answers.Answers{
		TeamKey: "NF", DigestIssue: "NF-186", NtfyTopic: "flocc-x",
		Repos:   []answers.Repo{{Name: "nimblefox", URL: "https://github.com/o/nimblefox", BaseBranch: "main", Check: "just check", Draft: true}},
		Secrets: &answers.Secrets{LinearKey: "lin_api_s3cret"},
	}}
	if err := Save(dir, h); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "flocc-nimble.toml")
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "secrets") {
		t.Fatalf("a secret reached the host file:\n%s", b)
	}
	// go-toml flattens the embedded Answers, so the file reads like a host's own answers.toml.
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil || raw["team_key"] != "NF" || raw["ssh"] != "root@flocc-nimble" || raw["Answers"] != nil {
		t.Fatalf("%v %v\n%s", raw, err, b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	got, err := Load(dir, "flocc-nimble")
	if err != nil || got.Target() != "root@flocc-nimble" || got.TeamKey != "NF" || got.Repos[0].Name != "nimblefox" || got.Secrets != nil {
		t.Fatalf("%+v %v", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".host-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestLoadRefusesWhatSSHOrTheShellWouldMisread(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "none.toml"), "team_key = \"NF\"\n")
	write(t, filepath.Join(dir, "shell.toml"), "ssh = \"h;id\"\n")
	write(t, filepath.Join(dir, "subst.toml"), "daytona = \"$(id)\"\n")
	write(t, filepath.Join(dir, "all.toml"), "ssh = \"root@all\"\n")
	write(t, filepath.Join(dir, "secret.toml"), "ssh = \"root@x\"\n[secrets]\nlinear_key = \"lin_api_s3cret\"\n")
	for _, name := range []string{"none", "shell", "subst", "all", "../x"} {
		if _, err := Load(dir, name); err == nil {
			t.Errorf("%s loaded", name)
		}
	}
	if h, err := Load(dir, "secret"); err != nil || h.Secrets != nil {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := Load(dir, "absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, h := range []Host{{Name: "all", SSH: "root@all"}, {Name: "x", SSH: "h;id"}, {Name: "x"}} {
		if err := Save(dir, h); err == nil {
			t.Errorf("%+v saved", h)
		}
	}
}

func TestValidTarget(t *testing.T) {
	for _, ok := range []string{"root@flocc-nimble", "daytona:flocc-smoke", "box.tailnet.ts.net"} {
		if !ValidTarget(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "h;rm -rf ~", "a b", "$(id)", "h`id`"} {
		if ValidTarget(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
