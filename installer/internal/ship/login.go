package ship

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/qr"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

var emailRE = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$`)

// LoginOptions are what Login needs. Email picks the claude.ai account and may be empty.
type LoginOptions struct {
	Target, Email string
	Out           io.Writer // the host's output and the summary; nil is stdout
}

func ValidEmail(s string) bool { return emailRE.MatchString(s) }

func loginRemote(addr string) string {
	cmd := "sudo -iu " + steps.ServiceUser + " " + serviceHome + "/.cache/flocc-setup/flocc-setup --phase login"
	if addr != "" {
		cmd += " --email " + addr
	}
	return cmd
}

// Login runs the login phase on the host over ssh -t, opens each sign-in URL it prints in the
// desktop browser and as a QR code, and prints the summary the host ends with. A host that
// reports a failed check exits non-zero, so the error is returned after the summary is shown.
func Login(ctx context.Context, o LoginOptions) error {
	if !ValidTarget(o.Target) {
		return badTarget(o.Target)
	}
	// The address goes into the remote command line, which sudo -i hands to a login shell.
	if o.Email != "" && !ValidEmail(o.Email) {
		return usageError(fmt.Sprintf("%q is not an email address", o.Email))
	}
	r, dest, err := connect(ctx, o.Target)
	if err != nil {
		return err
	}
	out := o.Out
	if out == nil {
		out = os.Stdout
	}
	return login(ctx, r, o.Target, dest, o.Email, out, OpenURL)
}

// target is the name the owner typed, never dest.SSH, which holds the Daytona token.
func login(ctx context.Context, r host.Runner, target string, dest Dest, addr string, out io.Writer, open func(url string)) error {
	tap := &summaryTap{out: &urlWatch{out: out, open: open, seen: map[string]bool{}}}
	c := sshCmd(dest, true, loginRemote(addr))
	c.Stdout = tap
	err := scrubErr(r.Interactive(ctx, c), dest.Token)
	tap.Flush()
	if s, ok := tap.Summary(); ok {
		s.Target = target
		s.Print(out)
	}
	return err
}

var (
	oauthURL = regexp.MustCompile(`https://\S*oauth\S*`)
	ansi     = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
)

// urlWatch passes the host's output through and opens each OAuth URL in it once.
type urlWatch struct {
	out  io.Writer
	open func(string)
	line []byte
	seen map[string]bool
}

func (u *urlWatch) Write(p []byte) (int, error) {
	n, err := u.out.Write(p)
	u.line = append(u.line, p...)
	for {
		i := bytes.IndexByte(u.line, '\n')
		if i < 0 {
			break
		}
		for _, m := range oauthURL.FindAllString(ansi.ReplaceAllString(string(u.line[:i]), ""), -1) {
			if !u.seen[m] {
				u.seen[m] = true
				u.open(m)
			}
		}
		u.line = u.line[i+1:]
	}
	return n, err
}

// OpenURL opens u in the desktop's browser and prints it as a QR code for a phone. ssh -t holds
// this terminal in raw mode; qr.Write ends its lines with \r\n for that.
func OpenURL(u string) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if cmd := exec.Command(opener, u); cmd.Start() == nil {
		go cmd.Wait()
	}
	if err := qr.Write(os.Stderr, u); err != nil {
		fmt.Fprintf(os.Stderr, "\r\nno QR code: %v\r\n", err)
	}
}

// ExitCode passes on the remote command's exit status; ssh killed by a signal reports -1. A
// usageError exits 2.
func ExitCode(err error, what string) int {
	if err == nil {
		return 0
	}
	var usage usageError
	if errors.As(err, &usage) {
		fmt.Fprintln(os.Stderr, what+":", usage)
		return 2
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return code
		}
		return 255
	}
	fmt.Fprintln(os.Stderr, what+":", err)
	return 1
}

// scrubErr removes a Daytona SSH token from an error's text. An ExitError carries no command line
// and is kept as it is, so callers can still read its exit code.
func scrubErr(err error, token string) error {
	var exit *exec.ExitError
	if err == nil || token == "" || errors.As(err, &exit) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
}
