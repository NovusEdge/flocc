package steps

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

type Mode string

const (
	Systemd Mode = "systemd"
	Loop    Mode = "loop"
)

func DetectMode(pid1Comm string) Mode {
	if strings.TrimSpace(pid1Comm) == "systemd" {
		return Systemd
	}
	return Loop
}

type Env struct {
	Run             host.Runner
	UI              ui.UI
	Root            string // prefix for file access; "" outside tests
	Home            string // the service user's home in the service phase
	Stage           string // directory holding la-setup, the bundles and plugins.json
	Mode            Mode
	Distro          host.Distro
	Answers         *answers.Answers
	AnswersPath     string
	SkipInteractive bool
	UID             int
	Sleep           func(time.Duration)
}

func (e *Env) Path(p string) string       { return filepath.Join(e.Root, p) }
func (e *Env) HomePath(rel string) string { return e.Path(filepath.Join(e.Home, rel)) }
func (e *Env) Real(rel string) string     { return filepath.Join(e.Home, rel) }

func (e *Env) MustCmd(ctx context.Context, c host.Cmd) (string, error) {
	return host.Must(ctx, e.Run, c)
}

func (e *Env) Must(ctx context.Context, name string, args ...string) (string, error) {
	return e.MustCmd(ctx, host.Cmd{Name: name, Args: args})
}

func (e *Env) Ok(ctx context.Context, name string, args ...string) bool {
	r, err := e.Run.Run(ctx, host.Cmd{Name: name, Args: args})
	return err == nil && r.Code == 0
}

func (e *Env) Output(ctx context.Context, name string, args ...string) string {
	r, err := e.Run.Run(ctx, host.Cmd{Name: name, Args: args})
	if err != nil || r.Code != 0 {
		return ""
	}
	return strings.TrimSpace(r.Stdout)
}

func (e *Env) SaveAnswers() error {
	if e.AnswersPath == "" {
		return nil
	}
	return e.Answers.Save(e.AnswersPath)
}

type Step interface {
	Name() string
	// Check reports whether the step's outcome already holds, with a one-line detail.
	Check(ctx context.Context, e *Env) (bool, string, error)
	Apply(ctx context.Context, e *Env) error
}

// ErrStopped ends a run without a failure, when I decline something only I can decide.
var ErrStopped = errors.New("stopped")

func RunAll(ctx context.Context, e *Env, steps []Step) error {
	statuses := make([]ui.StepStatus, len(steps))
	var done []string
	for i, s := range steps {
		ok, detail, err := s.Check(ctx, e)
		if err != nil {
			return fmt.Errorf("%s: check: %w", s.Name(), err)
		}
		statuses[i] = ui.StepStatus{Name: s.Name(), Detail: detail, Done: ok}
		if ok {
			done = append(done, s.Name())
		}
	}
	e.UI.Status(statuses)
	redo, err := e.UI.PickRedo(done)
	if err != nil {
		return err
	}
	for i, s := range steps {
		if statuses[i].Done && !slices.Contains(redo, s.Name()) {
			continue
		}
		if err := s.Apply(ctx, e); err != nil {
			if errors.Is(err, ErrStopped) {
				return err
			}
			return fmt.Errorf("%s: %w", s.Name(), err)
		}
		ok, detail, err := s.Check(ctx, e)
		if err != nil {
			return fmt.Errorf("%s: check: %w", s.Name(), err)
		}
		if !ok {
			return fmt.Errorf("%s: applied, but the check still fails: %s", s.Name(), detail)
		}
	}
	return nil
}

func cmd(name string, args ...string) host.Cmd { return host.Cmd{Name: name, Args: args} }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
