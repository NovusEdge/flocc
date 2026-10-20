package ship

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

// HostMain runs a command on a host as the service user and returns its exit code.
func HostMain(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: flocc-setup host <ssh-target|daytona:SANDBOX> <command...>")
		return 2
	}
	ctx := context.Background()
	home, _ := os.UserHomeDir()
	dest, err := resolveTarget(ctx, args[0], filepath.Join(home, ".config/daytona/config.json"), &http.Client{Timeout: 30 * time.Second}, realWaiter(os.Stderr))
	if err == nil {
		err = hostRun(ctx, host.Exec{Log: host.Redact(os.Stderr, dest.Token)}, dest, args[1:])
	}
	return exitCode(err, "host")
}

// hostRun scrubs the SSH token from errors, as ship does.
func hostRun(ctx context.Context, r host.Runner, dest Dest, command []string) error {
	return scrubErr(r.Interactive(ctx, sshCmd(dest, true, asServiceUser(command))), dest.Token)
}
