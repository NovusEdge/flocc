package steps

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// OldName is flocc's name before the rename, for its service user and its run command.
const OldName = "linear-agents"

// OldUsers are the service users earlier flocc versions made. flocc setup offers to remove one it
// finds; the root pass removes no other name. A later rename adds its old name here.
var OldUsers = []string{OldName}

var uidMinLine = regexp.MustCompile(`(?m)^\s*UID_MIN\s+(\d+)`)

func uidMin(e *Env) int {
	b, _ := os.ReadFile(e.Path("/etc/login.defs"))
	if m := uidMinLine.FindSubmatch(b); m != nil {
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			return n
		}
	}
	return 1000
}

// CheckOldUser refuses unless user is a plain account of an old install: on the list, a regular
// uid that is not the one running sudo, a real /home/<user>, no group that reaches root, and no
// session but its user manager's. The desktop checked before asking; root does not take its word.
func CheckOldUser(ctx context.Context, e *Env, user string, sudoUID int) error {
	if !slices.Contains(OldUsers, user) {
		return fmt.Errorf("%s is not a user an earlier flocc made", user)
	}
	f := strings.Split(e.Output(ctx, "getent", "passwd", user), ":")
	if len(f) < 7 {
		return fmt.Errorf("no %s user", user)
	}
	uid, err := strconv.Atoi(f[2])
	switch {
	case err != nil:
		return fmt.Errorf("%s's uid %q: %w", user, f[2], err)
	case uid < uidMin(e):
		return fmt.Errorf("%s has uid %d, below UID_MIN %d, so it is a system account", user, uid, uidMin(e))
	case uid == sudoUID:
		return fmt.Errorf("%s is the account running sudo", user)
	case f[5] != "/home/"+user:
		return fmt.Errorf("%s's home is %s, not /home/%s", user, f[5], user)
	}
	// Lstat: a symlinked home is not a directory, and tar and userdel -r would follow it.
	if st, err := os.Lstat(e.Path(f[5])); err != nil || !st.IsDir() {
		return fmt.Errorf("%s's home %s is not a directory", user, f[5])
	}
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", user)) {
		if slices.Contains(CardinalPrivileged, g) {
			return fmt.Errorf("%s is in group %s, which reaches root; remove it by hand if it is an old install", user, g)
		}
	}
	for _, s := range strings.Fields(e.Output(ctx, "loginctl", "show-user", user, "-p", "Sessions", "--value")) {
		if c := e.Output(ctx, "loginctl", "show-session", s, "-p", "Class", "--value"); !strings.HasPrefix(c, "manager") {
			return fmt.Errorf("%s has session %s (class %q) open; end it first", user, s, c)
		}
	}
	return nil
}

// RemoveOldUser archives an old install's home to a root-only tarball, then deletes the user as
// CardinalPurge does. Its processes stop first, so tar reads a home nothing writes to.
func RemoveOldUser(ctx context.Context, e *Env, user string, sudoUID int, now time.Time) (CardinalReport, error) {
	if err := CheckOldUser(ctx, e, user, sudoUID); err != nil {
		return CardinalReport{}, err
	}
	if err := stopUser(ctx, e, user); err != nil {
		return CardinalReport{}, err
	}
	archive := "/root/flocc-removed-" + user + "-" + now.Format("2006-01-02-150405") + ".tar.gz"
	// $1 and $2 keep the names out of the script's text; umask 077 makes the archive mode 600.
	if _, err := e.Must(ctx, "sh", "-c", `umask 077 && tar -czf "$1" -C /home -- "$2"`, "sh", archive, user); err != nil {
		return CardinalReport{}, fmt.Errorf("archiving /home/%s: %w", user, err)
	}
	r := CardinalReport{Steps: []ReportStep{{Name: "Archive", Detail: archive + ", mode 600", Done: true}}}
	if err := CardinalPurge(ctx, e, user); err != nil {
		return r, err
	}
	r.Steps = append(r.Steps, ReportStep{Name: "User", Detail: user + " and its home deleted", Done: true})
	r.Notes = []string{"revoke the old install's GitHub tokens and Linear key; this removal cannot"}
	return r, nil
}
