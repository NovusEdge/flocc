package ship

import (
	"context"
	"io"
	"strings"

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
	return scrubErr(r.Interactive(ctx, sshCmd(dest, tty, asServiceUser(command))), dest.Token)
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
