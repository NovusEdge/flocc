package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// RootMarker starts the one line of JSON that ends each root phase's output; the desktop takes it
// out of the stream.
const RootMarker = "flocc-root "

// ReportStep is one step's state at the end of a pass.
type ReportStep struct {
	Name    string `json:"name"`
	Detail  string `json:"detail"`
	Done    bool   `json:"done"`
	Pending bool   `json:"pending,omitempty"`
}

// RootReport ends a root pass.
type RootReport struct {
	Steps []ReportStep `json:"steps,omitempty"`
	Notes []string     `json:"notes,omitempty"`
	Error string       `json:"error,omitempty"`
}

func (r RootReport) JSON() string {
	b, _ := json.Marshal(r)
	return string(b)
}

func (r RootReport) Line() string { return RootMarker + r.JSON() }

func ParseRootLine(line string) (RootReport, bool) {
	var r RootReport
	rest, ok := strings.CutPrefix(line, RootMarker)
	if !ok || json.Unmarshal([]byte(rest), &r) != nil {
		return RootReport{}, false
	}
	return r, true
}

// ReportSteps checks every step again for the pass's report; pending names steps whose Apply
// returned ErrPending.
func ReportSteps(ctx context.Context, e *Env, list []Step) []ReportStep {
	var out []ReportStep
	for _, s := range list {
		ok, detail, err := s.Check(ctx, e)
		if err != nil {
			detail = err.Error()
		}
		out = append(out, ReportStep{Name: s.Name(), Detail: detail, Done: ok, Pending: slices.Contains(e.Pending, s.Name())})
	}
	return out
}

var rootStageName = regexp.MustCompile(`^/tmp/flocc-(create|remove)\.[A-Za-z0-9]{6,}$`)

// ValidRootStage reports whether dir is a name mktemp -d /tmp/flocc-create.XXXXXX or
// /tmp/flocc-remove.XXXXXX gives.
func ValidRootStage(dir string) bool { return rootStageName.MatchString(dir) }

// CheckRootStage refuses a stage the root pass cannot trust. The owner's mktemp made it, so
// anything not owned by SUDO_UID, a symlink or a looser mode means someone else had a hand in it.
// files are the ones the pass reads from it.
func CheckRootStage(dir string, sudoUID int, files ...string) error {
	if !ValidRootStage(dir) {
		return fmt.Errorf("the root pass runs from a /tmp/flocc-create.XXXXXX or /tmp/flocc-remove.XXXXXX stage, not %s", dir)
	}
	owned := func(p string, wantDir bool) error {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		sys, _ := st.Sys().(*syscall.Stat_t)
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink", p)
		case wantDir && !st.IsDir(), !wantDir && !st.Mode().IsRegular():
			return fmt.Errorf("%s is not a %s", p, map[bool]string{true: "directory", false: "regular file"}[wantDir])
		case sys == nil || int(sys.Uid) != sudoUID:
			return fmt.Errorf("%s is not owned by uid %d", p, sudoUID)
		case wantDir && st.Mode().Perm() != 0o700:
			return fmt.Errorf("%s is mode %o, not 700", p, st.Mode().Perm())
		}
		return nil
	}
	if err := owned(dir, true); err != nil {
		return err
	}
	for _, f := range files {
		if err := owned(filepath.Join(dir, f), false); err != nil {
			return err
		}
	}
	return nil
}

// PrivilegedGroups are groups that give root on the machine, or read it all.
var PrivilegedGroups = []string{"sudo", "wheel", "admin", "adm", "systemd-journal", "disk", "incus", "incus-admin", "lxd", "docker", "libvirt"}

// oldHome is the user's home from the passwd database, "" when the user does not exist.
func oldHome(ctx context.Context, e *Env, user string) string {
	f := strings.Split(e.Output(ctx, "getent", "passwd", user), ":")
	if len(f) < 7 {
		return ""
	}
	return f[5]
}

// purgeWait bounds the wait for the user's processes to end.
const purgeWait = 10 * time.Second

// stopUser ends every process of user; userdel refuses while any is left, and tmux's pane scopes
// outlive the session.
func stopUser(ctx context.Context, e *Env, user string) error {
	e.Ok(ctx, "loginctl", "disable-linger", user)
	e.Ok(ctx, "loginctl", "terminate-user", user)
	for waited := time.Duration(0); ; waited += 500 * time.Millisecond {
		e.Ok(ctx, "pkill", "-KILL", "-u", user)
		if !e.Ok(ctx, "pgrep", "-u", user) {
			return nil
		}
		if waited >= purgeWait {
			return fmt.Errorf("%s still has processes after %s; stop them and run again", user, purgeWait)
		}
		e.Sleep(500 * time.Millisecond)
	}
}

// PurgeUser deletes the user and its home, then the egress block, which must hold until no
// process of the user is left. The unit and rules path are cardinal 0.12's: an old install may
// still carry them.
func PurgeUser(ctx context.Context, e *Env, user string) error {
	if oldHome(ctx, e, user) != "" {
		if err := stopUser(ctx, e, user); err != nil {
			return err
		}
		if _, err := e.Must(ctx, "userdel", "-r", user); err != nil {
			return err
		}
	}
	e.Ok(ctx, "systemctl", "disable", "--now", "flocc-cardinal-egress@"+user+".service")
	if err := os.Remove(e.Path("/etc/flocc-cardinal/" + user + ".nft")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// writeAtomic replaces path with b through a temp file in its directory, so a reader never sees
// half a file and a running binary keeps its old inode.
func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
