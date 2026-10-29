package steps

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestRootLineRoundTrips(t *testing.T) {
	r := RootReport{Steps: []ReportStep{{Name: "Units", Detail: "running", Done: true}}}
	got, ok := ParseRootLine(r.Line())
	if !ok || !reflect.DeepEqual(got, r) {
		t.Fatalf("got %+v, %v", got, ok)
	}
	if _, ok := ParseRootLine(SummaryMarker + `{"key":"x"}`); ok {
		t.Fatal("parsed a line with another marker")
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
	good := func(t *testing.T) string { return goodWith(t, "flocc-remove.") }
	for _, prefix := range []string{"flocc-remove.", "flocc-create."} {
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
		"outside of /tmp": {"/var/tmp/flocc-remove.abcdef", uid},
		"another prefix":  {"/tmp/flocc-other.abcdef", uid},
	} {
		if err := CheckRootStage(c.dir, c.uid, files...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPurgeUserWaitsForProcessesThenDeletes(t *testing.T) {
	f := host.NewFake().
		On("getent passwd cardinal", 0, "cardinal:x:1002:1002::/home/cardinal:/bin/bash\n").
		On("loginctl disable-linger cardinal", 0, "").
		On("loginctl terminate-user cardinal", 0, "").
		On("pkill -KILL -u cardinal", 0, "").
		OnSeq("pgrep -u cardinal", host.Result{Code: 0}, host.Result{Code: 1}).
		On("userdel -r cardinal", 0, "").
		On("systemctl disable --now flocc-cardinal-egress@cardinal.service", 0, "")
	e := testEnv(t, f)
	e.Stage = t.TempDir()
	rules := e.Path("/etc/flocc-cardinal/cardinal.nft")
	os.MkdirAll(filepath.Dir(rules), 0o755)
	os.WriteFile(rules, []byte("x"), 0o644)
	if err := PurgeUser(context.Background(), e, "cardinal"); err != nil {
		t.Fatal(err)
	}
	userdel := slices.Index(f.Calls, "userdel -r cardinal")
	if userdel < 0 || userdel > slices.Index(f.Calls, "systemctl disable --now flocc-cardinal-egress@cardinal.service") || strings.Count(strings.Join(f.Calls, "\n"), "pgrep") != 2 {
		t.Fatalf("calls %v", f.Calls)
	}
	if _, err := os.Stat(rules); !os.IsNotExist(err) {
		t.Fatal("rules left behind")
	}
}
