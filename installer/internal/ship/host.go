package ship

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/steps"
)

const servicePath = "/home/" + steps.ServiceUser + "/.local/bin:/home/" + steps.ServiceUser + "/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin"

// shQuote makes s one word for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// asServiceUser builds the remote command line. The ssh user's shell parses it once, then sudo -i
// re-escapes its argv for the login shell, so the command must reach sudo as a single quoted
// word for sh -c.
func asServiceUser(command []string) string {
	return "sudo -iu " + steps.ServiceUser + " env PATH=" + servicePath + " sh -c " + shQuote(strings.Join(command, " "))
}

// HostMain runs a command on a host as the service user and returns its exit code.
func HostMain(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: la-setup host <ssh-target|daytona:SANDBOX> <command...>")
		return 2
	}
	ctx := context.Background()
	home, _ := os.UserHomeDir()
	dest, err := resolveTarget(ctx, args[0], filepath.Join(home, ".config/daytona/config.json"), &http.Client{Timeout: 30 * time.Second}, realWaiter(os.Stderr))
	if err == nil {
		err = hostRun(ctx, host.Exec{Log: redact{os.Stderr, dest.Token}}, dest, args[1:])
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "host:", err)
		return 1
	}
	return 0
}

// hostRun scrubs the SSH token from errors, as ship does.
func hostRun(ctx context.Context, r host.Runner, dest Dest, command []string) error {
	err := r.Interactive(ctx, sshCmd(dest, true, asServiceUser(command)))
	var exit *exec.ExitError
	if err == nil || dest.Token == "" || errors.As(err, &exit) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), dest.Token, "<token>"))
}
