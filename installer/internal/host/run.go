package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Cmd struct {
	Name string
	Args []string
	Dir  string
	Env  []string // added to the inherited environment
}

func (c Cmd) String() string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

type Result struct {
	Stdout, Stderr string
	Code           int
}

func (r Result) Out() string { return r.Stdout + r.Stderr }

type Runner interface {
	// Run errors only when the command cannot start; a non-zero exit is in Result.Code.
	Run(ctx context.Context, c Cmd) (Result, error)
	// Interactive attaches the command to this terminal.
	Interactive(ctx context.Context, c Cmd) error
}

type Exec struct{ Log io.Writer }

func (e Exec) command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	return cmd
}

func (e Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := e.command(ctx, c)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	fmt.Fprintf(e.Log, "$ %s\n", c)
	err := cmd.Run()
	e.Log.Write(out.Bytes())
	e.Log.Write(errb.Bytes())
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.Code = exit.ExitCode()
		return res, nil
	}
	return res, err
}

func (e Exec) Interactive(ctx context.Context, c Cmd) error {
	cmd := e.command(ctx, c)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	fmt.Fprintf(e.Log, "$ %s (interactive)\n", c)
	return cmd.Run()
}

type CmdError struct {
	Cmd  Cmd
	Code int
	Out  string
}

func (e *CmdError) Error() string {
	return fmt.Sprintf("%s exited %d:\n%s", e.Cmd, e.Code, Tail(e.Out, 20))
}

func Must(ctx context.Context, r Runner, c Cmd) (string, error) {
	res, err := r.Run(ctx, c)
	if err != nil {
		return "", fmt.Errorf("%s: %w", c, err)
	}
	if res.Code != 0 {
		return res.Stdout, &CmdError{Cmd: c, Code: res.Code, Out: res.Out()}
	}
	return res.Stdout, nil
}

func Tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
