package ship

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

const serviceHome = "/home/" + steps.ServiceUser

// shQuote makes s one word for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// asServiceUser builds the remote command line. Without -i, sudo execs its argv directly, so
// the ssh user's shell is the only one that parses it and the command stays one quoted word.
// A login shell would expand $... inside the quotes and turn newlines into continuations.
func asServiceUser(command []string) string {
	script := "cd && " + strings.Join(command, " ")
	return "sudo -u " + steps.ServiceUser + " -H env HOME=" + serviceHome + " " + steps.ServicePath(serviceHome) + " sh -c " + shQuote(script)
}

// FloccLine is the host's own flocc with each argument quoted on its own, so spaces, quotes and $
// reach the host CLI exactly as typed on the desktop.
func FloccLine(args []string) []string {
	line := []string{"flocc"}
	for _, a := range args {
		line = append(line, shQuote(a))
	}
	return line
}

// OnHost runs command, shell words, on the host as the service user with this terminal's
// streams; tty asks ssh for a terminal.
func OnHost(ctx context.Context, r host.Runner, dest Dest, command []string, tty bool) error {
	return OnHostWhy(ctx, r, dest, command, tty, "command on the host")
}

// OnHostWhy is OnHost with the line the handoff shows ("-" for none). The default, the command
// line, would show a Daytona token.
func OnHostWhy(ctx context.Context, r host.Runner, dest Dest, command []string, tty bool, why string) error {
	return scrubErr(r.Interactive(ctx, withWhy(sshCmd(dest, tty, asServiceUser(command)), why)), dest.Token)
}

// Capture runs command on the host as the service user without a terminal and returns what it
// printed; stdin, when set, is the command's input. Callers send and read secrets with it, so it
// forbids a pty (sshNoTTY). BatchMode makes ssh fail instead of waiting at a prompt nobody sees.
func Capture(ctx context.Context, r host.Runner, dest Dest, command []string, stdin io.Reader) (host.Result, error) {
	d := dest
	d.Opts = append([]string{"-o", "BatchMode=yes"}, dest.Opts...)
	c := sshNoTTY(d, asServiceUser(command))
	c.Stdin = stdin
	res, err := r.Run(ctx, c)
	res.Stdout = scrub(res.Stdout, dest.Token)
	res.Stderr = scrub(res.Stderr, dest.Token)
	return res, scrubErr(err, dest.Token)
}

// connectWait bounds Connect without a terminal: a host holding authentication for a check
// nobody can see would otherwise keep it forever.
var connectWait = 30 * time.Second

// Connect opens the connection to dest that later ssh calls share, before a run's first Capture.
// Capture buffers stderr, where ssh prints a Tailscale check-mode URL or a password prompt, so a
// host waiting on one would hang with nothing shown. With tty the connection opens on this
// terminal, where the owner can answer; without one it must open in BatchMode. A master that
// -O check finds live needs neither. name is the host as the owner calls it.
func Connect(ctx context.Context, r host.Runner, dest Dest, name string, tty bool) error {
	check := host.Cmd{Name: "ssh", Args: append(slices.Clone(dest.Opts), "-O", "check", dest.SSH)}
	if res, err := r.Run(ctx, check); err == nil && res.Code == 0 {
		return nil
	}
	// A machine that is asleep or off the tailnet would otherwise hold the handoff with nothing on
	// screen. ConnectTimeout ends before authentication, so a Tailscale check keeps its time.
	timeout := []string{"-o", "ConnectTimeout=10"}
	if tty {
		d := dest
		d.Opts = append(slices.Clone(timeout), dest.Opts...)
		if err := r.Interactive(ctx, withWhy(sshNoTTY(d, "true"), "connect to "+name)); err != nil {
			return fmt.Errorf("could not reach %s: %w", name, scrubErr(err, dest.Token))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, connectWait)
	defer cancel()
	d := dest
	d.Opts = append(append([]string{"-o", "BatchMode=yes"}, timeout...), dest.Opts...)
	res, err := r.Run(ctx, sshNoTTY(d, "true"))
	if err == nil && res.Code == 0 {
		return nil
	}
	// The last line is where Tailscale puts its URL; with it the owner can approve and retry.
	why := firstLine(host.Tail(res.Stderr, 1))
	switch {
	case why == "" && err != nil:
		why = err.Error()
	case why == "":
		why = fmt.Sprintf("ssh exited %d", res.Code)
	}
	return fmt.Errorf("could not reach %s without a prompt (%s); run it in a terminal", name, scrub(why, dest.Token))
}

// withWhy names an Interactive command for the handoff banner, whose default is the command line
// and so would show a Daytona token.
func withWhy(c host.Cmd, why string) host.Cmd {
	c.Why = why
	return c
}
