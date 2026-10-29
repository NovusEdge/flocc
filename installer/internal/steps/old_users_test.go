package steps

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// oldUser is linear-agents as zer0-day-nimble had it on 2026-10-21: uid 1001, its own group,
// lingering with only its user manager's session.
func oldUser(t *testing.T) (*Env, *host.Fake) {
	f := host.NewFake().
		On("getent passwd linear-agents", 0, "linear-agents:x:1001:1001::/home/linear-agents:/usr/bin/zsh\n").
		On("id -nG linear-agents", 0, "linear-agents\n").
		On("loginctl show-user linear-agents -p Sessions --value", 0, "2\n").
		On("loginctl show-session 2 -p Class --value", 0, "manager\n").
		On("loginctl disable-linger linear-agents", 0, "").
		On("loginctl terminate-user linear-agents", 0, "").
		On("pkill -KILL -u linear-agents", 0, "").
		On("pgrep -u linear-agents", 1, "").
		On("userdel -r linear-agents", 0, "").
		On("systemctl disable --now flocc-cardinal-egress@linear-agents.service", 0, "")
	e := testEnv(t, f)
	os.MkdirAll(e.Path("/home/linear-agents"), 0o700)
	os.MkdirAll(e.Path("/etc"), 0o755)
	os.WriteFile(e.Path("/etc/login.defs"), []byte("UID_MIN\t\t\t 1000\n"), 0o644)
	return e, f
}

const archiveCall = `sh -c umask 077 && tar -czf "$1" -C /home -- "$2" sh /root/flocc-removed-linear-agents-2026-10-21-153000.tar.gz linear-agents`

func TestRemoveOldUserArchivesBeforeUserdel(t *testing.T) {
	e, f := oldUser(t)
	f.On(archiveCall, 0, "")
	r, err := RemoveOldUser(context.Background(), e, "linear-agents", 1000, time.Date(2026, 10, 21, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	tar, del := slices.Index(f.Calls, archiveCall), slices.Index(f.Calls, "userdel -r linear-agents")
	if tar < 0 || del < tar || slices.Index(f.Calls, "pgrep -u linear-agents") > tar {
		t.Fatalf("calls %v", f.Calls)
	}
	if len(r.Steps) != 2 || !strings.Contains(r.Steps[0].Detail, "/root/flocc-removed-linear-agents-2026-10-21-153000.tar.gz") || len(r.Notes) != 1 {
		t.Fatalf("report %+v", r)
	}
}

func TestCheckOldUserRefuses(t *testing.T) {
	cases := map[string]func(e *Env, f *host.Fake){
		"not in the table": nil,
		"below UID_MIN": func(e *Env, f *host.Fake) {
			f.On("getent passwd linear-agents", 0, "linear-agents:x:999:999::/home/linear-agents:/bin/sh\n")
		},
		"the sudo user": func(e *Env, f *host.Fake) {
			f.On("getent passwd linear-agents", 0, "linear-agents:x:1000:1000::/home/linear-agents:/bin/sh\n")
		},
		"home elsewhere": func(e *Env, f *host.Fake) {
			f.On("getent passwd linear-agents", 0, "linear-agents:x:1001:1001::/srv/la:/bin/sh\n")
		},
		"symlinked home": func(e *Env, f *host.Fake) {
			os.Remove(e.Path("/home/linear-agents"))
			os.Symlink(e.Path("/etc"), e.Path("/home/linear-agents"))
		},
		"privileged group": func(e *Env, f *host.Fake) { f.On("id -nG linear-agents", 0, "linear-agents docker\n") },
		"live session": func(e *Env, f *host.Fake) {
			f.On("loginctl show-user linear-agents -p Sessions --value", 0, "2 5\n").On("loginctl show-session 5 -p Class --value", 0, "user\n")
		},
	}
	for name, setup := range cases {
		e, f := oldUser(t)
		user := "linear-agents"
		if setup == nil {
			user = "nobody-flocc"
		} else {
			setup(e, f)
		}
		if err := CheckOldUser(context.Background(), e, user, 1000); err == nil {
			t.Errorf("%s: passed", name)
		}
		if slices.ContainsFunc(f.Calls, func(c string) bool { return strings.HasPrefix(c, "userdel") || strings.Contains(c, "tar ") }) {
			t.Errorf("%s: deleted or archived", name)
		}
	}
	e, _ := oldUser(t)
	if err := CheckOldUser(context.Background(), e, "linear-agents", 1000); err != nil {
		t.Fatalf("the live user refused: %v", err)
	}
}
