package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// ErrLocked is what a LoginOptions.Lock returns while a tick holds the tick lock.
var ErrLocked = errors.New("the tick lock is held")

// LoginOptions are Login's settings; nil Lock and Consent take the real lock and run the real
// claude remote-control.
type LoginOptions struct {
	Email   string
	Lock    func(path string) (unlock func(), err error)
	Consent func(ctx context.Context, c host.Cmd) error
}

const pollSessions = 30 * time.Second

// Login gives the host a full claude.ai login and the Remote Control consent, then runs the Run
// step, which starts the timer. A setup-token login cannot open Remote Control sessions, so only
// claude.ai counts.
func Login(ctx context.Context, e *Env, o LoginOptions) error {
	if o.Lock == nil {
		o.Lock = flockPath
	}
	if o.Consent == nil {
		o.Consent = func(ctx context.Context, c host.Cmd) error {
			return answerConsent(ctx, c, os.Stdout, defaultConsentWaits)
		}
	}
	unlock, err := lockWithoutSessions(ctx, e, o.Lock)
	if err != nil {
		return err
	}
	defer unlock()
	if claudeLogin(ctx, e) != "full" {
		args := []string{"auth", "login", "--claudeai"}
		if o.Email != "" {
			args = append(args, "--email", o.Email)
		}
		if err := e.Run.Interactive(ctx, host.Cmd{Name: e.claude(), Args: args}); err != nil {
			return err
		}
	}
	// Checked before the consent: a login that did not take must fail as that, not as a consent
	// error from remote-control.
	if err := requireFullLogin(ctx, e); err != nil {
		return err
	}
	if err := trustWorktrees(e); err != nil {
		return err
	}
	if err := o.Consent(ctx, host.Cmd{Name: e.claude(), Args: []string{"remote-control"}, Dir: e.Real(worktreesRel)}); err != nil {
		return err
	}
	if err := requireFullLogin(ctx, e); err != nil {
		return err
	}
	done, _, err := (Run{}).Check(ctx, e)
	if err != nil || done {
		return err
	}
	return (Run{}).Apply(ctx, e)
}

func requireFullLogin(ctx context.Context, e *Env) error {
	if l := claudeLogin(ctx, e); l != "full" {
		return fmt.Errorf("claude auth status reports %s after the login, not a claude.ai login", l)
	}
	return nil
}

// trustWorktrees marks ~/worktrees trusted the way sessions.trust_folders does for a session's own
// paths. Sessions trust only their worktree and repo, so on a fresh host remote-control would sit
// at the folder-trust dialog in ~/worktrees. The caller holds the tick lock with no session
// running, so nothing rewrites ~/.claude.json from memory meanwhile. Values stay raw JSON: the
// file also holds the account's state, which a float64 round trip could alter.
func trustWorktrees(e *Env) error {
	path, dir := e.HomePath(".claude.json"), e.Real(worktreesRel)
	file := map[string]json.RawMessage{}
	switch b, err := os.ReadFile(path); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &file); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	projects := map[string]map[string]json.RawMessage{}
	if raw, ok := file["projects"]; ok {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return fmt.Errorf("%s: projects: %w", path, err)
		}
	}
	if string(projects[dir]["hasTrustDialogAccepted"]) == "true" {
		return nil
	}
	if projects[dir] == nil {
		projects[dir] = map[string]json.RawMessage{}
	}
	projects[dir]["hasTrustDialogAccepted"] = json.RawMessage("true")
	var err error
	if file["projects"], err = json.Marshal(projects); err != nil {
		return err
	}
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Mode 600 from the start: the file holds the account's OAuth state. WriteFile would keep the
	// mode of a leftover temp file.
	tmp := path + ".flocc.tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// checkStatus refuses a status the lock and the session list cannot be trusted from.
func checkStatus(st hostStatus) error {
	switch {
	case st.TickLock == "":
		return errors.New("flocc status names no tick lock: ship 0.4.0 or later to this host first")
	case st.LedgerError != "":
		return fmt.Errorf("flocc cannot read its ledger, so the running sessions are unknown: %s", st.LedgerError)
	}
	return nil
}

// waitFor sleeps through e.Sleep, which tests replace, but returns at once when ctx ends so Ctrl-C
// does not wait out a poll. The sleeping goroutine is left to finish on its own.
func waitFor(ctx context.Context, e *Env, d time.Duration) error {
	done := make(chan struct{})
	go func() { e.Sleep(d); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lockWithoutSessions takes the tick lock once no session runs: running sessions rewrite
// ~/.claude.json from memory and could drop the consent. The lock is let go between looks, so
// ticks keep supervising the sessions until they end. New sessions can start between looks, so
// each poll after the first prints the list and the owner sees whether it shrinks.
func lockWithoutSessions(ctx context.Context, e *Env, lock func(string) (func(), error)) (func(), error) {
	noted, asked := false, false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		st, err := readStatus(ctx, e)
		if err != nil {
			return nil, fmt.Errorf("flocc status: %w", err)
		}
		if err := checkStatus(st); err != nil {
			return nil, err
		}
		unlock, err := lock(e.Path(st.TickLock))
		if errors.Is(err, ErrLocked) {
			if !noted {
				e.UI.Note("Waiting for the tick", "A tick holds the lock; the login goes on when it ends.")
				noted = true
			}
			if err := waitFor(ctx, e, 2*time.Second); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		// Again under the lock: the tick that just ended may have started a session.
		if st, err = readStatus(ctx, e); err != nil {
			unlock()
			return nil, fmt.Errorf("flocc status: %w", err)
		}
		if err := checkStatus(st); err != nil {
			unlock()
			return nil, err
		}
		live := st.live()
		if len(live) == 0 {
			return unlock, nil
		}
		unlock()
		if !asked {
			q := fmt.Sprintf("%s running: %s. Running sessions rewrite ~/.claude.json and could drop the Remote Control consent. Wait for them to end?",
				plural(len(live), "session"), strings.Join(live, ", "))
			wait, err := e.UI.Confirm(q, true)
			if err != nil {
				return nil, err
			}
			if !wait {
				return nil, ErrStopped
			}
			asked = true
		} else {
			e.UI.Note("Sessions still running", strings.Join(live, ", "))
		}
		if err := waitFor(ctx, e, pollSessions); err != nil {
			return nil, err
		}
	}
}

// flockPath takes the lock flocc tick takes with fcntl.flock on the same file, so a tick and a
// login exclude each other.
func flockPath(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// The consent dialog and the line remote-control prints once it is up, matched on output with
// escape codes removed. Claude Code draws with Ink, which can move the cursor right instead of
// printing a space. connectedLine is calibrated against the real output in Task 10 Step 2; the
// rest of the code treats a match as the only proof that Remote Control is on.
var (
	consentPrompt = regexp.MustCompile(`Enable\s+Remote\s+Control\?`)
	connectedLine = regexp.MustCompile(`(?i)\b(connected|ready)\b|https://claude\.ai/code/\S+`)
	cursorForward = regexp.MustCompile(`\x1b\[\d*C`)
	escapeCodes   = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
)

type consentWaits struct{ connect, stop time.Duration }

// connect is how long a host with the consent and the trust takes to print its connected line.
var defaultConsentWaits = consentWaits{connect: 60 * time.Second, stop: 5 * time.Second}

// answerConsent runs claude remote-control under a pty, answers its consent prompt with y and
// returns once it prints a connected line, after stopping it. Silence is an error: the same
// silence comes from a slow start, from another dialog (the folder trust) and from a process
// that does not prompt on a pipe, and only the connected line proves Remote Control is on.
// script is util-linux's, on every host the installer supports; Ink refuses a stdin that is not
// a terminal.
func answerConsent(ctx context.Context, c host.Cmd, out io.Writer, w consentWaits) error {
	words := make([]string, 0, 1+len(c.Args))
	for _, a := range append([]string{c.Name}, c.Args...) {
		words = append(words, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	cmd := exec.CommandContext(ctx, "script", "-qfec", strings.Join(words, " "), "/dev/null")
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	// A child that keeps the output pipe open must not hold Wait past the stop.
	cmd.WaitDelay = w.stop
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	watch := newConsentWatch(out)
	cmd.Stdout, cmd.Stderr = watch, watch
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	// Closing script's pty hangs up claude, so a kill after the interrupt leaves nothing behind.
	stop := func() {
		cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(w.stop):
			cmd.Process.Kill()
			<-exited
		}
	}
	prompt := watch.prompt
	timeout := time.After(w.connect)
	for {
		select {
		case <-prompt:
			io.WriteString(stdin, "y\n")
			prompt = nil
		case <-watch.connected:
			stop()
			return nil
		case err := <-exited:
			return fmt.Errorf("claude remote-control exited before it connected (%v):\n%s", err, watch.tail())
		case <-timeout:
			stop()
			return fmt.Errorf("claude remote-control printed no connected line in %s; it may be waiting at a prompt of its own. Last output:\n%s\nRun it by hand as %s to see what it waits for",
				w.connect, watch.tail(), ServiceUser)
		case <-ctx.Done():
			stop()
			return ctx.Err()
		}
	}
}

// consentWatch passes output through and closes prompt and connected once each appears. The
// prompt has no newline after it and can arrive split across writes.
type consentWatch struct {
	out                       io.Writer
	prompt, connected         chan struct{}
	oncePrompt, onceConnected sync.Once
	mu                        sync.Mutex
	recent                    []byte
}

func newConsentWatch(out io.Writer) *consentWatch {
	return &consentWatch{out: out, prompt: make(chan struct{}), connected: make(chan struct{})}
}

func (p *consentWatch) Write(b []byte) (int, error) {
	p.out.Write(b)
	p.mu.Lock()
	p.recent = append(p.recent, b...)
	if len(p.recent) > 4096 {
		p.recent = p.recent[len(p.recent)-4096:]
	}
	clean := p.cleanLocked()
	p.mu.Unlock()
	if consentPrompt.Match(clean) {
		p.oncePrompt.Do(func() { close(p.prompt) })
	}
	if connectedLine.Match(clean) {
		p.onceConnected.Do(func() { close(p.connected) })
	}
	return len(b), nil
}

func (p *consentWatch) cleanLocked() []byte {
	return escapeCodes.ReplaceAll(cursorForward.ReplaceAll(p.recent, []byte(" ")), nil)
}

func (p *consentWatch) tail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return host.Tail(string(p.cleanLocked()), 20)
}
