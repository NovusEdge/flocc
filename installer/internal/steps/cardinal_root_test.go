package steps

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOwner owner@desktop"

func cardinalEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Stage = t.TempDir()
	os.WriteFile(filepath.Join(e.Stage, "desktop.pub"), []byte(testKey+"\n"), 0o600)
	return e
}

func withCardinal(f *host.Fake, groups string) *host.Fake {
	return f.On("getent passwd cardinal", 0, "cardinal:x:1002:1002::/home/cardinal:/bin/bash\n").
		On("id -u cardinal", 0, "1002\n").
		On("id -nG cardinal", 0, groups+"\n").
		On("sudo -l -U cardinal", 0, "User cardinal is not allowed to run sudo on m.\n").
		On("loginctl show-user cardinal -p Linger --value", 0, "yes\n")
}

func TestCardinalToolsNamesMissingAndNeverInstalls(t *testing.T) {
	f := host.NewFake()
	for _, tool := range []string{"tmux", "curl", "ssh-keygen", "nft"} {
		f.On("sh -c command -v "+tool, 0, "/usr/bin/"+tool)
	}
	e := cardinalEnv(t, f)
	done, detail, _ := CardinalTools{}.Check(context.Background(), e)
	if done || detail != "missing: script" {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
	err := CardinalTools{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "script missing") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.Calls {
		if !strings.HasPrefix(c, "sh -c command -v") {
			t.Fatalf("ran %q", c)
		}
	}
}

func TestCardinalUserRefusesAnExistingUnadoptedUser(t *testing.T) {
	f := withCardinal(host.NewFake(), "cardinal")
	e := cardinalEnv(t, f)
	os.MkdirAll(e.Path("/home/cardinal"), 0o700)
	u := &CardinalUser{User: "cardinal"}
	if done, _, _ := u.Check(context.Background(), e); done {
		t.Fatal("an unadopted user passed the check")
	}
	err := u.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "--adopt") {
		t.Fatalf("err = %v", err)
	}
	if f.Ran("chmod 700 /home/cardinal") || f.Ran("loginctl enable-linger cardinal") {
		t.Fatalf("changed an unadopted account: %v", f.Calls)
	}
}

func TestCardinalUserRefusesPrivilegedGroupsAndSudoRules(t *testing.T) {
	f := withCardinal(host.NewFake(), "cardinal docker adm").
		On("chmod 700 /home/cardinal", 0, "").
		On("sudo -l -U cardinal", 0, "User cardinal may run the following commands on m:\n    (ALL) ALL\n")
	e := cardinalEnv(t, f)
	os.MkdirAll(e.Path("/home/cardinal"), 0o755)
	err := (&CardinalUser{User: "cardinal", Adopt: true}).Apply(context.Background(), e)
	for _, want := range []string{"gpasswd -d cardinal docker", "gpasswd -d cardinal adm", "sudo -l -U cardinal lists rules"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "gpasswd") || strings.Contains(c, "sudoers") {
			t.Fatalf("ran %q", c)
		}
	}
}

// Through RunAll, whose check after Apply must pass on a user Apply just made.
func TestCardinalUserCreatesAndLingers(t *testing.T) {
	none := host.Result{Code: 2}
	f := host.NewFake().
		OnSeq("getent passwd cardinal", none, none, host.Result{Stdout: "cardinal:x:1002:1002::/home/cardinal:/bin/bash\n"}).
		On("useradd --create-home --shell /bin/bash cardinal", 0, "").
		On("chmod 700 /home/cardinal", 0, "").
		On("id -nG cardinal", 0, "cardinal\n").
		On("sudo -l -U cardinal", 0, "").
		On("loginctl enable-linger cardinal", 0, "").
		On("loginctl show-user cardinal -p Linger --value", 0, "yes\n")
	e := cardinalEnv(t, f)
	os.MkdirAll(e.Path("/home/cardinal"), 0o700)
	if err := RunAll(context.Background(), e, []Step{&CardinalUser{User: "cardinal"}}); err != nil {
		t.Fatalf("%v; calls %v", err, f.Calls)
	}
}

func TestDesktopKeyWritesAsTheUserWithTheKeyAsAnArgument(t *testing.T) {
	f := withCardinal(host.NewFake(), "cardinal")
	e := cardinalEnv(t, f)
	f.On("runuser -u cardinal -- /bin/sh -c "+desktopKeyScript+" sh "+testKey+" /home/cardinal", 0, "")
	if err := (DesktopKey{User: "cardinal"}).Apply(context.Background(), e); err != nil {
		t.Fatalf("%v; calls %v", err, f.Calls)
	}
	last := f.Argv[len(f.Argv)-1]
	if last[0] != "runuser" || last[6] != desktopKeyScript || last[8] != testKey || strings.Contains(desktopKeyScript, "ssh-ed25519") {
		t.Fatalf("argv %q", last)
	}
}

func TestDesktopKeyRefusesAStagedFileThatIsNotOneKey(t *testing.T) {
	f := withCardinal(host.NewFake(), "cardinal")
	e := cardinalEnv(t, f)
	os.WriteFile(filepath.Join(e.Stage, "desktop.pub"), []byte(testKey+"\n"+testKey+"\n"), 0o600)
	if err := (DesktopKey{User: "cardinal"}).Apply(context.Background(), e); err == nil {
		t.Fatal("accepted two lines")
	}
	os.WriteFile(filepath.Join(e.Stage, "desktop.pub"), []byte(`command="sh" `+testKey), 0o600)
	if err := (DesktopKey{User: "cardinal"}).Apply(context.Background(), e); err == nil {
		t.Fatal("accepted an options prefix")
	}
}

// Root never names a path inside the home except the home itself, which it chmods after an lstat.
func TestCardinalRootStepsNeverTouchTheHomeAsRoot(t *testing.T) {
	f := withCardinal(host.NewFake(), "cardinal").
		On("chmod 700 /home/cardinal", 0, "").
		On("loginctl enable-linger cardinal", 0, "").
		On("runuser -u cardinal -- /bin/sh -c "+desktopKeyScript+" sh "+testKey+" /home/cardinal", 0, "")
	e := cardinalEnv(t, f)
	os.MkdirAll(e.Path("/home/cardinal"), 0o755)
	for _, s := range []Step{&CardinalUser{User: "cardinal", Adopt: true}, DesktopKey{User: "cardinal"}} {
		if err := s.Apply(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for _, argv := range f.Argv {
		if argv[0] == "runuser" {
			continue
		}
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "/home/cardinal/") {
				t.Fatalf("root ran %q", argv)
			}
		}
	}
}

func TestEgressRulesAcceptMagicDNSBeforeTheDrops(t *testing.T) {
	r := EgressRules("cardinal", 1002)
	dns := strings.Index(r, "100.100.100.100")
	drop4 := strings.Index(r, "ip daddr 100.64.0.0/10 drop")
	drop6 := strings.Index(r, "ip6 daddr fd7a:115c:a1e0::/48 drop")
	if dns < 0 || drop4 < dns || drop6 < dns || !strings.Contains(r, "meta skuid 1002 ") || !strings.HasPrefix(r, "table inet flocc-cardinal-cardinal\ndelete table") {
		t.Fatalf("rules:\n%s", r)
	}
}

func TestEgressRestartsOnlyWhenTheRulesChange(t *testing.T) {
	f := withCardinal(host.NewFake(), "cardinal").
		On("systemctl daemon-reload", 0, "").
		On("systemctl enable flocc-cardinal-egress@cardinal.service", 0, "").
		On("systemctl restart flocc-cardinal-egress@cardinal.service", 0, "").
		On("systemctl start flocc-cardinal-egress@cardinal.service", 0, "")
	e := cardinalEnv(t, f)
	g := Egress{User: "cardinal"}
	if err := g.Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("systemctl restart flocc-cardinal-egress@cardinal.service") {
		t.Fatalf("new rules not loaded: %v", f.Calls)
	}
	f.Calls = nil
	if err := g.Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if f.Ran("systemctl restart flocc-cardinal-egress@cardinal.service") || !f.Ran("systemctl start flocc-cardinal-egress@cardinal.service") {
		t.Fatalf("unchanged rules restarted: %v", f.Calls)
	}
}

func TestCheckRootStage(t *testing.T) {
	uid := os.Getuid()
	files := []string{"flocc-setup", "desktop.pub"}
	goodWith := func(t *testing.T, prefix string) string {
		dir, err := os.MkdirTemp("/tmp", prefix)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		for _, f := range files {
			os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600)
		}
		return dir
	}
	good := func(t *testing.T) string { return goodWith(t, "flocc-cardinal.") }
	for _, prefix := range []string{"flocc-cardinal.", "flocc-create."} {
		if err := CheckRootStage(goodWith(t, prefix), uid, files...); err != nil {
			t.Fatalf("good %s stage refused: %v", prefix, err)
		}
	}
	loose := good(t)
	os.Chmod(loose, 0o755)
	link := good(t)
	os.Remove(filepath.Join(link, "desktop.pub"))
	os.Symlink("/etc/passwd", filepath.Join(link, "desktop.pub"))
	for name, c := range map[string]struct {
		dir string
		uid int
	}{
		"mode 755":        {loose, uid},
		"symlinked file":  {link, uid},
		"another owner":   {good(t), uid + 1},
		"fixed stage":     {"/tmp/flocc-setup", uid},
		"outside of /tmp": {"/var/tmp/flocc-cardinal.abcdef", uid},
		"another prefix":  {"/tmp/flocc-other.abcdef", uid},
	} {
		if err := CheckRootStage(c.dir, c.uid, files...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCardinalPurgeWaitsForProcessesThenDeletes(t *testing.T) {
	f := withCardinal(host.NewFake(), "cardinal").
		On("loginctl disable-linger cardinal", 0, "").
		On("loginctl terminate-user cardinal", 0, "").
		On("pkill -KILL -u cardinal", 0, "").
		OnSeq("pgrep -u cardinal", host.Result{Code: 0}, host.Result{Code: 1}).
		On("userdel -r cardinal", 0, "").
		On("systemctl disable --now flocc-cardinal-egress@cardinal.service", 0, "")
	e := cardinalEnv(t, f)
	os.MkdirAll(e.Path("/etc/flocc-cardinal"), 0o755)
	os.WriteFile(e.Path(EgressRulesPath("cardinal")), []byte("x"), 0o644)
	if err := CardinalPurge(context.Background(), e, "cardinal"); err != nil {
		t.Fatal(err)
	}
	userdel := slices.Index(f.Calls, "userdel -r cardinal")
	if userdel < 0 || userdel > slices.Index(f.Calls, "systemctl disable --now flocc-cardinal-egress@cardinal.service") || strings.Count(strings.Join(f.Calls, "\n"), "pgrep") != 2 {
		t.Fatalf("calls %v", f.Calls)
	}
	if _, err := os.Stat(e.Path(EgressRulesPath("cardinal"))); !os.IsNotExist(err) {
		t.Fatal("rules left behind")
	}
}
