package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestConvertOldWritesOneFilePerHostOnce(t *testing.T) {
	home := t.TempDir()
	old := filepath.Join(home, "old", "hosts")
	dir := Dir(home)
	write(t, old, "# tailnet\nroot@la-nimble\nbox.tail1.ts.net\n-oProxyCommand=id\nuser@box.tail1.ts.net\nroot@a/b\n")
	msg, err := ConvertOld(old, dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := List(dir)
	got := map[string]string{}
	for _, h := range list {
		got[h.Name] = h.SSH
	}
	if err != nil || len(got) != 2 || got["la-nimble"] != "root@la-nimble" || got["box"] != "box.tail1.ts.net" {
		t.Fatalf("%v %v", got, err)
	}
	for _, want := range []string{"-oProxyCommand=id", "user@box.tail1.ts.net", "root@a/b", old + ".converted"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the notice lacks %q:\n%s", want, msg)
		}
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old file would be converted again")
	}
	if msg, err := ConvertOld(old, dir); msg != "" || err != nil {
		t.Fatalf("%q %v", msg, err)
	}
}
