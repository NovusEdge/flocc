# flocc Part 2a: host side of setup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship takes every answer on stdin, runs its setup in one SSH session and never prompts; the Claude login becomes one command (`just login <target>`); ship ends with a summary; the tick pages once when the Claude login lapses. Released as 0.4.0.

**Architecture:** The answers document (TOML, `[secrets]` included) travels desktop → `ssh` stdin → root phase (read to EOF first) → `sudo -iu flocc flocc-setup --phase service --answers -` stdin, and is never written on the host; `host.Exec` redacts known secret values from `setup.log` and stdout as a second guard. The old Claude step splits into **Claude** (install, push settings, plugins) and **Claude login** (probe only; returns a new `steps.ErrPending`), and the Run step leaves the timer off while that login is pending. A new host-side `--phase login` (Go, run by the desktop's `flocc-setup login` over `ssh -t`) holds the tick lock (path read from `flocc status --json`), runs `claude auth login --claudeai`, marks `~/worktrees` trusted, runs `claude remote-control` under a pty (`script -qfec`), answers its consent prompt and waits for its connected line, then runs the Run step. The service and login phases end their output with one `flocc-summary <json>` line; the desktop (`ship.Ship`, `ship.Login`) takes it out of the stream, decodes it and prints the summary itself (`ship.Summary.Print`). Every QR code the desktop shows comes from one helper, `installer/internal/qr`. This plan is written against the code as it is after Part 1: every name below is the Part 1 name (Python package `src/flocc/`, CLI `flocc`, Go module `github.com/NovusEdge/flocc`, installer `flocc-setup`, stage `/tmp/flocc-setup`, service user `flocc`, units `flocc.timer`/`flocc-tmux.service`, tmux socket `flocc`, loop session `flocc-loop`, worktrees `~/worktrees`, config `~/.config/flocc/`); today's code still has the old names, mapped by the spec's Part 1 Names table.

**Tech Stack:** Go 1.27 (`installer/`, go-toml v2, charm huh, new: `github.com/mdp/qrterminal/v4` v4.0.0 for QR codes), Python 3.13 with uv and pytest, just. Rejected for the QR code: shelling out to `qrencode`, which is missing on most desktops and shows nothing when it is.

**Spec:** `docs/superpowers/specs/2026-10-20-flocc-design.md`, section "Part 2a: host side of setup".

## Global Constraints

- Non-interactive ship runs its setup in one SSH session forced to have no TTY: the run, the `sudo -n` probes and the stage reset pass `-T`, so a `RequestTTY` setting in the owner's ssh config cannot give the run a pty, which would echo the answers document back and never pass EOF. (The `uname` probe, the stage reset, `scp` and the run are separate connections; the `uname` probe and `scp` carry neither sudo nor secrets and keep ssh's defaults.) It first checks `sudo -n true` and stops with "give <user> passwordless sudo, or use root" otherwise.
- The answers document, secrets included, goes on stdin. The root phase reads stdin to EOF before anything else and passes it to `sudo -iu flocc flocc-setup --phase service --answers -` through a pipe.
- No secret is written to a file on the host except the service's own secret files (mode 600), appears in argv, or reaches `setup.log` or stdout: the runner redacts known secret values from its log as a second guard. The guard covers the secrets of the stdin document; secrets typed into the forms of an `--interactive` ship are not in its set, and no step logs them (the first guard, tested in Task 7).
- `--interactive` keeps `-t` and today's forms.
- A full Claude login is required: the long-lived `claude setup-token` token cannot open Remote Control sessions. `claude auth login` needs Claude Code 2.1.41 or later (the installer already requires 2.1.295).
- Take the tick lock for the whole login. If sessions are running, say so and wait or abort.
- The Remote Control consent never reports success without a positive signal: `~/worktrees` is trusted first (the same `hasTrustDialogAccepted` write `sessions.trust_folders` makes, under the tick lock), `claude remote-control` runs under `script -qfec` from the start, and only a connected line counts as done. Silence is an error that tells the owner to run it by hand.
- Ship prints a summary instead of `Done.`: version on the host, Claude login (full, token-only or none), GitHub token expiries, each failed doctor check, the timer, live sessions. It exits non-zero when a check other than the pending login fails. The host sends the summary as one `flocc-summary <json>` line at the end of its output; the desktop strips it and prints the summary.
- Token expiry warnings stay as they are (digest line at 14 days, tier 1 at 3 days). The tick sends one tier 1 escalation when the Claude login lapses.
- The `just` recipe is `just login <target>` until 2b adds `flocc login -H`. The desktop `flocc` binary is Part 2b, not this plan.
- Version 0.4.0 in `pyproject.toml`, `plugin/.claude-plugin/plugin.json`, `uv.lock` and a `## 0.4.0 (…)` CHANGELOG entry (`tests/test_plugin.py::test_the_plugin_and_the_package_share_one_version`).
- Code comments only for facts the code cannot show (why, constraints, past bugs); no narration. Commits use `git commit -s`, with no `Co-Authored-By` line and no AI attribution. Run `gofmt -w` on the Go files a task touches before its commit; the plan's struct-tag and comment alignment is a sketch.
- Steps that touch a real host are for the controller only (Task 10).

## Review Focus

1. A malformed answers document whose bad line holds a token: go-toml's error quotes the line, so the error must give only line and column (test in Task 2).
2. Shipping as `root@host` to a host without `sudo` installed: `sudo -n true` exits 127 and ship must say "install sudo", not "give root passwordless sudo" (test in Task 6).
3. `claude remote-control` that shows the consent prompt split across writes, never prompts but connects (consent already given), exits at once, or stalls at another prompt such as the folder-trust dialog: the login must neither hang nor report success without a connected line (tests in Task 5).
4. A login started while a tick holds the lock, or while sessions run: it waits for the tick, and between looks at the sessions it lets the lock go so ticks keep supervising them (tests in Task 5).
5. A target or email with shell metacharacters: the target is an ssh destination (a leading `-` would be an option) and the email goes into a remote command line that `sudo -i` hands to a login shell, so `Ship` and `Login` must refuse both (tests in Task 6).
6. A non-interactive ship whose ssh config says `RequestTTY force`: every secret-carrying ssh call passes `-T` (test in Task 6).
7. The `flocc-summary` line split across writes, or a prompt with no newline (the sign-in code prompt): the stream filter must pass the prompt at once and still catch the line (tests in Task 6).

## Interfaces this plan produces for Part 2b

- **Stdin answers protocol:** `flocc-setup --answers -` (root phase, run with `sudo -n`) reads one TOML answers document (`answers.Answers`, with `[secrets] linear_key / agent_token / publish_token`) from stdin to EOF and pipes the same bytes to `flocc-setup --phase service --answers -`. Any other `--answers` value is refused. The host takes no `--target`: the desktop prints the summary and knows the target.
- **Desktop ship (package `installer/internal/ship`):**
  - `type Options struct{ Target string; Interactive bool; Out io.Writer }` (`Out` nil: stdout)
  - `func Ship(ctx context.Context, o Options, answers io.Reader) (Summary, error)` — `answers` is the TOML document (nil with `Interactive`; with `Interactive` it must be nil); `Ship` validates the target, parses the document before anything reaches the host, stages, runs and returns the summary the host sent. It does not print it.
  - `type Summary = steps.Summary`: `{Version, Login string; LoginPending bool; TokenExpiry map[string]time.Time; Failed []string; Timer bool; Sessions []string; SessionsErr string}` plus an unjson'd `Target string` that `Print` uses in the login command. `func (s Summary) Print(w io.Writer)` prints nothing for a zero Summary. The extra fields beyond the ruled list are `SessionsErr` and `Target`.
  - `flocc-setup ship [--interactive] [--answers -|FILE] <target>` (`ship.Main(args []string) int`) calls `Ship` and prints the summary; `--answers -` reads the desktop's stdin.
- **Claude login:** host side `flocc-setup --phase login [--email ADDR]` run as `flocc` over `ssh -t`. Desktop side: `type LoginOptions struct{ Target, Email string; Out io.Writer }` and `func Login(ctx context.Context, o LoginOptions) error` (`Email` may be empty; `Login` prints the host's summary to `Out` itself), `ship.LoginMain(args []string) int`, `ship.OpenURL(url string)`. The step in ship is `steps.ClaudeLogin` (name `"Claude login"`), which returns `steps.ErrPending` while the login is not full.
- **Summary wire format:** the service and login phases end their stdout with `\nflocc-summary <json>\n` (`steps.SummaryMarker`, `Summary.Line`, `steps.ParseSummaryLine`).
- **QR codes:** `qr.Write(w io.Writer, text string) error` in `installer/internal/qr`, half-block output with `\r\n` line endings for a terminal that `ssh -t` holds in raw mode. Part 2b uses it for the ntfy topic.
- `ship.ValidTarget(t string) bool`, `answers.Parse([]byte) (*answers.Answers, error)`, `(*answers.Secrets).Values() []string`, `host.Redact(w io.Writer, secrets ...string) io.Writer`, and the unexported ship internals 2b's tests reuse: `stageFiles`, `stageOK`, `shipRoot`, `sshCmd`, `sshNoTTY`, `exitCode`, `scrub`.

## Task order and parallelism

| Task | Depends on | Files it owns |
|---|---|---|
| 1 Runner stdin, stdout and redaction | — | `installer/internal/host/*`, redact move in `installer/internal/ship/{daytona,ship,host}.go`, `ship_test.go` |
| 1b QR writer | — | `installer/internal/qr/*`, `go.mod`, `go.sum` |
| 2 Answers parsing | — | `installer/internal/answers/*` |
| 3 Pending steps, Claude login step, Run step | — | `installer/internal/steps/{step,claude,run,config,list}.go` and their tests |
| 8 Python: tick lock in status, login lapse page | — | `src/flocc/*`, `tests/*` |
| 4 Summary | 3 | `installer/internal/steps/summary.go`, `summary_test.go` |
| 5 Host login phase | 4 | `installer/internal/steps/login.go`, `login_test.go` |
| 6 Desktop ship and login | 1, 1b, 2, 4 | `installer/internal/ship/{ship,host,login,summary}.go`, `ship_test.go`, `login_test.go`, `summary_test.go` |
| 7 Installer entry point | 1, 2, 4, 5, 6 | `installer/main.go`, `installer/main_test.go` |
| 9 Recipe, docs, version | 7, 8 | `justfile`, `docs/vm-setup.md`, `CHANGELOG.md`, `pyproject.toml`, `plugin/.claude-plugin/plugin.json`, `uv.lock` |
| 10 Host verification (controller, manual) | 9 | none |

Tasks 1, 1b, 2, 3 and 8 run in parallel. Task 6 runs in parallel with 5 once 1, 1b, 2 and 4 are in. Each parallel task checks only its own packages (its Run steps say which); the whole tree is built and tested in Task 7 Step 4.

---

### Task 1: Runner stdin, stdout and redaction

**Files:**
- Modify: `installer/internal/host/run.go` (Cmd, Exec.Run, Exec.Interactive)
- Create: `installer/internal/host/redact.go`
- Modify: `installer/internal/host/fake.go`
- Test: `installer/internal/host/host_test.go`
- Modify: `installer/internal/ship/daytona.go` (delete `type redact` and its `Write`), `installer/internal/ship/ship.go` and `installer/internal/ship/host.go` (use `host.Redact`), `installer/internal/ship/ship_test.go` (delete `TestRedactHidesTheToken`; it moves to host)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `host.Cmd` gains `Stdin io.Reader` (nil: `/dev/null` for `Run`, the terminal for `Interactive`) and `Stdout io.Writer` (`Interactive` only; nil: the terminal).
  - `func Redact(w io.Writer, secrets ...string) io.Writer` replacing each non-empty secret with `<token>`.
  - `host.Fake` gains `Stdins map[string]string` (stdin each command got, by `String()`), `InteractiveOuts map[string]string` and `func (f *Fake) OnInteractiveOut(cmd, out string) *Fake` (written to `Cmd.Stdout` by `Interactive`).

- [ ] **Step 1: Write the failing tests**

Append to `installer/internal/host/host_test.go` (add `bytes` and `strings` to its imports if missing):

```go
func TestExecLogRedactsSecretsButTheCallerGetsTheOutput(t *testing.T) {
	var log strings.Builder
	e := Exec{Log: Redact(&log, "s3cret", "")}
	res, err := e.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", `printf '%s\n' "$S"; printf '%s' "$S" >&2`},
		Env: []string{"S=s3cret"}})
	if err != nil || res.Stdout != "s3cret\n" || res.Stderr != "s3cret" {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Contains(log.String(), "s3cret") || strings.Count(log.String(), "<token>") != 2 {
		t.Fatalf("log %q", log.String())
	}
}

func TestRedactHidesTheToken(t *testing.T) {
	var b strings.Builder
	Redact(&b, "tok123").Write([]byte("$ ssh tok123@ssh.app.daytona.io uname -m\n"))
	if strings.Contains(b.String(), "tok123") || !strings.Contains(b.String(), "<token>@ssh.app.daytona.io") {
		t.Fatalf("%q", b.String())
	}
}

// strings.ReplaceAll with an empty old string inserts the new one between every byte.
func TestRedactWithNoSecretsPassesThrough(t *testing.T) {
	var b strings.Builder
	Redact(&b, "", "").Write([]byte("plain\n"))
	if b.String() != "plain\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestExecPassesStdin(t *testing.T) {
	res, err := Exec{}.Run(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("doc\n")})
	if err != nil || res.Stdout != "doc\n" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestExecInteractiveUsesTheCmdStreams(t *testing.T) {
	var out bytes.Buffer
	if err := (Exec{}).Interactive(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("x"), Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "x" {
		t.Fatalf("%q", out.String())
	}
}

func TestFakeRecordsStdinAndWritesInteractiveOutput(t *testing.T) {
	f := NewFake().On("cat", 0, "").OnInteractiveOut("ssh -t h x", "hello\r\n")
	f.Run(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("doc")})
	var out bytes.Buffer
	f.Interactive(context.Background(), Cmd{Name: "ssh", Args: []string{"-t", "h", "x"}, Stdin: strings.NewReader("in"), Stdout: &out})
	if f.Stdins["cat"] != "doc" || f.Stdins["ssh -t h x"] != "in" || out.String() != "hello\r\n" {
		t.Fatalf("%v %q", f.Stdins, out.String())
	}
}
```

Delete `TestRedactHidesTheToken` from `installer/internal/ship/ship_test.go`.

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/internal/host/`
Expected: build failure, `undefined: Redact` and `unknown field Stdin in struct literal`.

- [ ] **Step 3: Implement**

In `installer/internal/host/run.go`, extend `Cmd` and both Exec methods:

```go
type Cmd struct {
	Name string
	Args []string
	Dir  string
	Env  []string // added to the inherited environment
	// Stdin is nil for /dev/null in Run and for the terminal in Interactive.
	Stdin io.Reader
	// Stdout replaces the terminal in Interactive; Run always captures.
	Stdout io.Writer
}
```

```go
func (e Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := e.command(ctx, c)
	cmd.Stdin = c.Stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	// … the rest of Run unchanged
}

func (e Exec) Interactive(ctx context.Context, c Cmd) error {
	cmd := e.command(ctx, c)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if c.Stdin != nil {
		cmd.Stdin = c.Stdin
	}
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	}
	fmt.Fprintf(e.log(), "$ %s (interactive)\n", c)
	return cmd.Run()
}
```

Create `installer/internal/host/redact.go`:

```go
package host

import (
	"bytes"
	"io"
)

// Redact returns w with every non-empty secret replaced by <token>. It works per Write, so a
// secret split across two Writes would pass; Exec writes each command line and each output whole.
func Redact(w io.Writer, secrets ...string) io.Writer {
	var keep [][]byte
	for _, s := range secrets {
		if s != "" {
			keep = append(keep, []byte(s))
		}
	}
	if len(keep) == 0 {
		return w
	}
	return redactor{w, keep}
}

type redactor struct {
	w       io.Writer
	secrets [][]byte
}

func (r redactor) Write(p []byte) (int, error) {
	out := p
	for _, s := range r.secrets {
		out = bytes.ReplaceAll(out, s, []byte("<token>"))
	}
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
```

In `installer/internal/host/fake.go`:

```go
type Fake struct {
	Responses map[string]Result
	Seqs      map[string][]Result
	Calls     []string
	Argv      [][]string // name and args of each Run, unjoined
	Stdins    map[string]string

	InteractiveErrs map[string]error
	InteractiveOuts map[string]string
}

func NewFake() *Fake {
	return &Fake{Responses: map[string]Result{}, Seqs: map[string][]Result{}, Stdins: map[string]string{},
		InteractiveErrs: map[string]error{}, InteractiveOuts: map[string]string{}}
}

func (f *Fake) readStdin(c Cmd) {
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		f.Stdins[c.String()] = string(b)
	}
}
```

Call `f.readStdin(c)` in `Run` right after the `Argv` append, and rewrite `Interactive`:

```go
// OnInteractiveOut makes Interactive write out to the command's Stdout.
func (f *Fake) OnInteractiveOut(cmd, out string) *Fake {
	f.InteractiveOuts[cmd] = out
	return f
}

func (f *Fake) Interactive(_ context.Context, c Cmd) error {
	f.Calls = append(f.Calls, "interactive: "+c.String())
	f.readStdin(c)
	if out, ok := f.InteractiveOuts[c.String()]; ok && c.Stdout != nil {
		io.WriteString(c.Stdout, out)
	}
	return f.InteractiveErrs[c.String()]
}
```

Add `"io"` to fake.go's imports. In `installer/internal/ship/daytona.go` delete the `redact` type, its comment and its `Write` method (drop the `bytes` and `io` imports if nothing else uses them). In `ship.go` and `host.go` replace `redact{os.Stderr, dest.Token}` with `host.Redact(os.Stderr, dest.Token)`.

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/internal/host/ ./installer/internal/ship/ && go vet ./installer/internal/host/ ./installer/internal/ship/`
Expected: `ok` for both packages, no vet output.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/host installer/internal/ship
git commit -s -m "Give runner commands their own stdin and stdout, and redact secrets in host logs"
```

---

### Task 1b: QR writer

**Files:**
- Create: `installer/internal/qr/qr.go`
- Test: `installer/internal/qr/qr_test.go`
- Modify: `go.mod`, `go.sum` at the repository root (via `go get`)

**Interfaces:**
- Consumes: nothing.
- Produces: `func Write(w io.Writer, text string) error`. It draws `text` as a half-block QR code and ends every line with `\r\n`, because `ssh -t` holds the desktop terminal in raw mode, where a bare `\n` only moves down. It returns an error, writing nothing, when `text` does not fit in a QR code: `qrterminal.GenerateHalfBlock` panics on such input (nil dereference, checked with 5000 bytes). Part 2b uses it for the ntfy topic.

- [ ] **Step 1: Write the failing tests**

Create `installer/internal/qr/qr_test.go`:

```go
package qr

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteDrawsBlocksWithCRLFLines(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, "https://claude.ai/oauth/authorize?code=true&state=abc"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	lines := strings.Count(out, "\n")
	if !strings.Contains(out, "█") || lines < 10 || strings.Count(out, "\r\n") != lines {
		t.Fatalf("%d lines, %d CRLF:\n%q", lines, strings.Count(out, "\r\n"), out)
	}
}

func TestWriteRefusesTextThatDoesNotFit(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, strings.Repeat("a", 5000)); err == nil || b.Len() != 0 {
		t.Fatalf("err=%v wrote %d bytes", err, b.Len())
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/internal/qr/`
Expected: build failure, `undefined: Write`.

- [ ] **Step 3: Implement**

Run from the repository root: `go get github.com/mdp/qrterminal/v4@v4.0.0` (the only released version; it brings `rsc.io/qr`, `golang.org/x/sys` and `golang.org/x/term`).

Create `installer/internal/qr/qr.go`:

```go
// Package qr draws QR codes on a terminal.
package qr

import (
	"bytes"
	"errors"
	"io"

	"github.com/mdp/qrterminal/v4"
)

// Write draws text as a QR code on w. Every line ends in \r\n: ssh -t holds the terminal in raw
// mode, where \n alone does not return the cursor to column 0.
func Write(w io.Writer, text string) (err error) {
	// qrterminal ignores the encoder's error and then dereferences the nil result.
	defer func() {
		if recover() != nil {
			err = errors.New("qr: the text does not fit in a QR code")
		}
	}()
	var b bytes.Buffer
	qrterminal.GenerateHalfBlock(text, qrterminal.L, &b)
	_, err = w.Write(bytes.ReplaceAll(b.Bytes(), []byte("\n"), []byte("\r\n")))
	return err
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/internal/qr/ && go vet ./installer/internal/qr/`
Expected: `ok`, no vet output.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/qr go.mod go.sum
git commit -s -m "Add a QR code writer for terminals in raw mode"
```

---

### Task 2: Answers parsing

**Files:**
- Modify: `installer/internal/answers/answers.go` (`Load`, new `Parse`, `Secrets.Values`)
- Test: `installer/internal/answers/answers_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func Parse(b []byte) (*Answers, error)` (errors name line and column only); `func (s *Secrets) Values() []string` (non-empty values; nil receiver gives nil). `Load` keeps its signature and returns an empty `*Answers` for a missing file.

- [ ] **Step 1: Write the failing tests**

Append to `installer/internal/answers/answers_test.go` (add `slices` and `strings` imports if missing):

```go
// go-toml's own message quotes the offending line, and in an answers document that line can
// be a token.
func TestParseErrorsNeverQuoteTheDocument(t *testing.T) {
	for _, doc := range []string{
		"[secrets]\nlinear_key = \"lin_api_s3cret\n",
		"[secrets]\nagent_token = \"github_pat_s3cret\" junk\n",
		"[secrets]\npublish_token = [\"github_pat_s3cret\"]\n",
	} {
		_, err := Parse([]byte(doc))
		if err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: %v", doc, err)
		}
	}
	if _, err := Parse([]byte("[secrets]\nlinear_key = \"x\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("want the line number, got %v", err)
	}
}

func TestParseReadsSecretsAndValuesSkipsEmptyOnes(t *testing.T) {
	a, err := Parse([]byte("team_key = \"NF\"\n[secrets]\nlinear_key = \"lin_api_x\"\npublish_token = \"github_pat_y\"\n"))
	if err != nil || a.TeamKey != "NF" {
		t.Fatalf("%+v %v", a, err)
	}
	if got := a.Secrets.Values(); !slices.Equal(got, []string{"lin_api_x", "github_pat_y"}) {
		t.Fatalf("%v", got)
	}
	if (*Secrets)(nil).Values() != nil {
		t.Fatal("a document without [secrets] has no values")
	}
	if a, err := Parse(nil); err != nil || a.Secrets != nil {
		t.Fatalf("empty document: %+v %v", a, err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/internal/answers/`
Expected: build failure, `undefined: Parse`.

- [ ] **Step 3: Implement**

In `answers.go` add `"fmt"` to the imports and replace `Load`:

```go
func Load(path string) (*Answers, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Answers{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads an answers document. Its errors give only the position: go-toml's own message
// quotes the offending line, which can hold a token.
func Parse(b []byte) (*Answers, error) {
	a := &Answers{}
	err := toml.Unmarshal(b, a)
	if err == nil {
		return a, nil
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return nil, fmt.Errorf("answers: invalid TOML at line %d, column %d", row, col)
	}
	return nil, errors.New("answers: a value does not fit its field; check the types against the example in docs/vm-setup.md")
}
```

Below the `Secrets` type:

```go
// Values is every secret that is set, for redacting logs and output.
func (s *Secrets) Values() []string {
	if s == nil {
		return nil
	}
	var v []string
	for _, x := range []string{s.LinearKey, s.AgentToken, s.PublishToken} {
		if x != "" {
			v = append(v, x)
		}
	}
	return v
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/internal/answers/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/answers
git commit -s -m "Parse answers documents without quoting their lines in errors"
```

---

### Task 3: Pending steps, the Claude login step and the Run step

**Files:**
- Modify: `installer/internal/steps/step.go` (`Env.Pending`, `Env.lastDoctor`, `ErrPending`, `RunAll`)
- Modify: `installer/internal/steps/claude.go` (drop the login from `Claude`, add `ClaudeLogin`, `claudeLogin`)
- Modify: `installer/internal/steps/config.go` (`doctorResult`)
- Modify: `installer/internal/steps/run.go` (`DoctorFailed`, `timerOn`, pending login)
- Modify: `installer/internal/steps/list.go`
- Test: `installer/internal/steps/step_test.go`, `claude_test.go`, `run_test.go`

**Interfaces:**
- Consumes (from Part 1's renamed tests): `testEnv` (Home `/home/flocc`, Mode Systemd), `claudeBin` (`/home/flocc/.local/bin/claude`), `doctorCmd` (`/home/flocc/.local/bin/flocc doctor`), `declineUI`.
- Produces:
  - `var ErrPending error`; `Env.Pending []string` (names of steps whose Apply returned it); unexported `Env.lastDoctor *doctorResult` and `type doctorResult struct{ out string; fails []string; ok bool }`.
  - `type ClaudeLogin struct{}` with `Name() == "Claude login"`; `func claudeLogin(ctx, e) string` returning `"full"`, `"token-only (<authMethod>)"` or `"none"`.
  - `type DoctorFailed struct{ Fails []string }` (error); `const loginFail = "FAIL  claude login:"`; `func withoutLogin(fails []string) []string`; `func timerOn(ctx, e) bool`.
  - Test helper `systemdRun(f *host.Fake, doctorCode int, doctorOut string) *host.Fake` in `run_test.go`.
  - `ServiceSteps()` = Repo, Identity, Claude, ClaudeLogin, Secrets, Config, Run.

- [ ] **Step 1: Write the failing tests**

In `step_test.go` (add `slices` to imports):

```go
func TestRunAllRecordsPendingStepsAndGoesOn(t *testing.T) {
	a, b := &fakeStep{name: "a", applyErr: ErrPending}, &fakeStep{name: "b"}
	e := newEnv(scripted())
	if err := RunAll(context.Background(), e, []Step{a, b}); err != nil || b.applied != 1 || !slices.Equal(e.Pending, []string{"a"}) {
		t.Fatalf("err=%v b.applied=%d pending=%v", err, b.applied, e.Pending)
	}
}
```

In `claude_test.go`, replace `TestClaudeCheck` and `TestClaudeApplyAfterAbortedLoginSkipsReinstall` with:

```go
func TestClaudeCheck(t *testing.T) {
	f := host.NewFake().On(claudeBin+" --version", 0, "2.1.295 (Claude Code)\n")
	e := claudeEnv(t, f, plugins.List{Plugins: []string{"curt@curt"}})
	writeSettings(t, e, pushOn)
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "plugins missing: curt@curt" {
		t.Fatalf("detail=%q", detail)
	}
	writeInstalled(t, e, `{"version":2,"plugins":{"curt@curt":[{"scope":"user"}]}}`)
	if done, detail, _ := (Claude{}).Check(context.Background(), e); !done || detail != "2.1.295 (Claude Code), 1 plugin" {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
}

func TestClaudeApplyNeverLogsIn(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 1, `{"loggedIn":false}`).
		On(claudeBin+" plugin marketplace add o/curt", 0, "").
		On(claudeBin+" plugin install curt@curt", 0, "")
	e := claudeEnv(t, f, plugins.List{Marketplaces: []plugins.Marketplace{{Name: "curt", GitHub: "o/curt"}}, Plugins: []string{"curt@curt"}})
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "interactive:") || c == "sh -c curl -fsSL https://claude.ai/install.sh | bash" {
			t.Fatalf("unexpected %s", c)
		}
	}
	if !f.Ran(claudeBin + " plugin install curt@curt") {
		t.Fatalf("calls: %v", f.Calls)
	}
}

func TestClaudeLoginProbesInEveryMode(t *testing.T) {
	for _, c := range []struct {
		code       int
		out, login string
	}{
		{0, `{"loggedIn":true,"authMethod":"claude.ai"}`, "full"},
		{0, `{"loggedIn":true,"authMethod":"oauth_token"}`, "token-only (oauth_token)"},
		{1, `{"loggedIn":false}`, "none"},
		{0, `not json`, "none"},
	} {
		for _, skip := range []bool{false, true} {
			f := host.NewFake().On(claudeBin+" auth status", c.code, c.out)
			e := testEnv(t, f)
			e.SkipInteractive = skip
			if got := claudeLogin(context.Background(), e); got != c.login {
				t.Errorf("%s skip=%v: %q", c.out, skip, got)
			}
			done, detail, _ := (ClaudeLogin{}).Check(context.Background(), e)
			if done != (c.login == "full") || !done && !strings.HasPrefix(detail, c.login+"; ") {
				t.Errorf("%s skip=%v: done=%v detail=%q", c.out, skip, done, detail)
			}
		}
	}
}

func TestClaudeLoginApplyLeavesAMissingLoginPendingAndStartsNothing(t *testing.T) {
	f := host.NewFake().On(claudeBin+" auth status", 1, `{"loggedIn":false}`)
	if err := (ClaudeLogin{}).Apply(context.Background(), testEnv(t, f)); !errors.Is(err, ErrPending) ||
		!slices.Equal(f.Calls, []string{claudeBin + " auth status"}) {
		t.Fatalf("err=%v calls=%v", err, f.Calls)
	}
}

// --interactive offers every finished step for redo; redoing a full login must not keep the
// Run step from starting the timer.
type redoUI struct{ *ui.Scripted }

func (redoUI) PickRedo([]string) ([]string, error) { return []string{"Claude login"}, nil }

func TestRedoingAFullLoginLeavesNothingPending(t *testing.T) {
	f := host.NewFake().On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := testEnv(t, f)
	e.UI = redoUI{&ui.Scripted{A: &answers.Answers{}, Out: io.Discard}}
	if err := RunAll(context.Background(), e, []Step{ClaudeLogin{}}); err != nil || len(e.Pending) != 0 {
		t.Fatalf("err=%v pending=%v", err, e.Pending)
	}
}
```

Add `"errors"`, `"io"`, `"slices"` and the `answers` and `ui` packages to claude_test.go's imports. In `run_test.go`:

```go
const loginFailLine = "FAIL  claude login: none; on the desktop, run just login <ssh target of this host>\n"

// systemdRun answers what Run.Check and Run.Apply run in systemd mode, around doctor.
func systemdRun(f *host.Fake, doctorCode int, doctorOut string) *host.Fake {
	return f.On("systemctl --user daemon-reload", 0, "").
		On("systemctl --user enable --now flocc-tmux.service", 0, "").
		On("systemctl --user is-active --quiet flocc-tmux.service", 0, "").
		On("tmux -N -L flocc list-sessions", 0, "").
		On(doctorCmd, doctorCode, doctorOut).
		On("systemctl --user enable --now flocc.timer", 0, "")
}

func TestRunWithTheLoginPendingStartsTmuxButNotTheTimer(t *testing.T) {
	f := systemdRun(host.NewFake(), 1, "ok    tmux: server on socket flocc\n"+loginFailLine)
	e := testEnv(t, f)
	e.Pending = []string{"Claude login"}
	if err := (Run{}).Apply(context.Background(), e); !errors.Is(err, ErrPending) {
		t.Fatalf("err=%v", err)
	}
	if !f.Ran("systemctl --user enable --now flocc-tmux.service") || f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("calls: %v", f.Calls)
	}
	if e.lastDoctor == nil || len(e.lastDoctor.fails) != 1 {
		t.Fatalf("doctor result not kept: %+v", e.lastDoctor)
	}
}

func TestRunWithTheLoginPendingStillStopsOnOtherFails(t *testing.T) {
	f := systemdRun(host.NewFake(), 1, loginFailLine+"FAIL  linear: 401\n")
	e := testEnv(t, f)
	e.Pending = []string{"Claude login"}
	var failed *DoctorFailed
	if err := (Run{}).Apply(context.Background(), e); !errors.As(err, &failed) || !slices.Equal(failed.Fails, []string{"FAIL  linear: 401"}) {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatal("timer started despite a FAIL line")
	}
}

func TestRunWithoutAPendingLoginTreatsTheLoginFailAsAFailure(t *testing.T) {
	f := systemdRun(host.NewFake(), 1, loginFailLine)
	var failed *DoctorFailed
	if err := (Run{}).Apply(context.Background(), testEnv(t, f)); !errors.As(err, &failed) || len(failed.Fails) != 1 {
		t.Fatalf("err=%v", err)
	}
}
```

Add `"slices"` to run_test.go's imports.

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/internal/steps/`
Expected: build failure, `undefined: ErrPending`, `undefined: ClaudeLogin`, `undefined: DoctorFailed`.

- [ ] **Step 3: Implement step.go**

Add to `Env`, after `SkipInteractive`:

```go
	// Pending names the steps whose Apply returned ErrPending in this run.
	Pending []string
```

and as the last field:

```go
	lastDoctor *doctorResult // the Run step's doctor run, which Summarize reuses
```

Below `ErrStopped`:

```go
// ErrPending ends a step's Apply when its outcome needs the owner after the run, as the Claude
// login does; RunAll records the step in Env.Pending and goes on.
var ErrPending = errors.New("pending")
```

In `RunAll`, replace the error branch of the apply loop:

```go
		if err := s.Apply(ctx, e); err != nil {
			if errors.Is(err, ErrPending) {
				e.Pending = append(e.Pending, s.Name())
				continue
			}
			if errors.Is(err, ErrStopped) {
				return err
			}
			return fmt.Errorf("%s: %w", s.Name(), err)
		}
```

- [ ] **Step 4: Implement claude.go**

Delete `loginNote` and `loggedIn`. In `Claude.Check` delete the `loggedIn` branch and end with:

```go
	_, v := claudeVersion(ctx, e)
	return true, v + ", " + plural(len(l.Plugins), "plugin"), nil
```

In `Claude.Apply` delete the whole `if !loggedIn(ctx, e) { … }` block. Add:

```go
// claudeLogin is "full" for a claude.ai login, "token-only (<method>)" for any other, or "none".
// Only a full login can open Remote Control sessions.
func claudeLogin(ctx context.Context, e *Env) string {
	var s struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal([]byte(e.Output(ctx, e.claude(), "auth", "status")), &s) != nil || !s.LoggedIn {
		return "none"
	}
	if s.AuthMethod == "claude.ai" {
		return "full"
	}
	return "token-only (" + s.AuthMethod + ")"
}

// ClaudeLogin reports the login and never does it: it needs a browser and the owner, so ship
// leaves it to just login.
type ClaudeLogin struct{}

func (ClaudeLogin) Name() string { return "Claude login" }

func (ClaudeLogin) Check(ctx context.Context, e *Env) (bool, string, error) {
	if l := claudeLogin(ctx, e); l != "full" {
		return false, l + "; just login does it after ship", nil
	}
	return true, "claude.ai subscription", nil
}

// Apply is reached for a full login only when --interactive offers the step for redo; pending
// then would keep the Run step from starting the timer.
func (ClaudeLogin) Apply(ctx context.Context, e *Env) error {
	if claudeLogin(ctx, e) == "full" {
		return nil
	}
	return ErrPending
}
```

- [ ] **Step 5: Implement config.go, run.go and list.go**

In `config.go`, above `doctor`:

```go
type doctorResult struct {
	out   string
	fails []string
	ok    bool
}
```

In `run.go` add `"errors"` and `"slices"` to the imports, then:

```go
// DoctorFailed is the Run step's error when doctor reports FAIL lines other than a pending
// Claude login; the service phase still prints its summary for it.
type DoctorFailed struct{ Fails []string }

func (d *DoctorFailed) Error() string {
	return fmt.Sprintf("doctor reports %s; fix them and run just ship again", plural(len(d.Fails), "FAIL line"))
}

const loginFail = "FAIL  claude login:"

func withoutLogin(fails []string) []string {
	return slices.DeleteFunc(slices.Clone(fails), func(f string) bool { return strings.HasPrefix(f, loginFail) })
}

// timerOn reports whether ticks run: the timer in systemd mode, the loop command in loop mode.
func timerOn(ctx context.Context, e *Env) bool {
	if e.Mode == Loop {
		return strings.Contains(e.loopPane(ctx), "flocc run")
	}
	r, err := e.Run.Run(ctx, e.userctl("is-enabled", "--quiet", "flocc.timer"))
	return err == nil && r.Code == 0
}
```

In `Run.Check`, make the loop branch `if timerOn(ctx, e) { return true, "flocc-loop ticking", nil }` and replace the final `is-enabled` block with:

```go
	if !timerOn(ctx, e) {
		return false, "timer not enabled", nil
	}
	return true, "timer enabled", nil
```

In `Run.Apply`, replace everything from `out, fails, ok := doctor(ctx, e)` up to (not including) the `Confirm` call:

```go
	out, fails, ok := doctor(ctx, e)
	e.lastDoctor = &doctorResult{out: out, fails: fails, ok: ok}
	e.UI.Note("flocc doctor", out)
	// A pending login fails doctor until just login runs, which starts the timer itself.
	pending := slices.Contains(e.Pending, ClaudeLogin{}.Name())
	others := fails
	if pending {
		others = withoutLogin(fails)
	}
	switch {
	case len(others) > 0:
		return &DoctorFailed{Fails: others}
	case !ok && len(fails) == 0:
		return errors.New("flocc doctor did not run cleanly; see its output above")
	case pending:
		return ErrPending
	}
```

In `list.go`:

```go
func ServiceSteps() []Step {
	return []Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Config{}, Run{}}
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./installer/internal/steps/ && go vet ./installer/internal/steps/`
Expected: `ok`, no vet output. `TestRunStopsOnDoctorFailLines` still passes on the `2 FAIL lines` message.

- [ ] **Step 7: Commit**

```bash
git add installer/internal/steps
git commit -s -m "Split the Claude login out of the Claude step and leave the timer off while it is pending"
```

---

### Task 4: Summary

**Files:**
- Create: `installer/internal/steps/summary.go`
- Test: `installer/internal/steps/summary_test.go`

**Interfaces:**
- Consumes (Task 3): `claudeLogin`, `withoutLogin`, `timerOn`, `doctor`, `doctorResult`, `Env.lastDoctor`, `ServicePath`; test helpers `testEnv`, `claudeBin`, `doctorCmd`.
- Produces:
  - `type Summary struct{ Version, Login string; LoginPending bool; TokenExpiry map[string]time.Time; Failed []string; Timer bool; Sessions []string; SessionsErr string; Target string }`, JSON-tagged for the wire (`Target` is `json:"-"`; the desktop sets it). `TokenExpiry` is keyed `"agent token"` / `"publish token"`; the zero time means the token never expires. `LoginPending` is `Login != "full"`.
  - `func Summarize(ctx context.Context, e *Env) Summary`, `func (s Summary) Failing() bool` (a FAIL line other than the Claude login's), `func (s Summary) Print(w io.Writer)` (prints nothing when `Login` is empty)
  - `const SummaryMarker = "flocc-summary "`, `func (s Summary) Line() string` (marker plus JSON, no newline), `func ParseSummaryLine(line string) (Summary, bool)`
  - `var now = time.Now`, which `Print` uses for the days left on a token
  - `type hostStatus struct{ TickLock, LedgerError string; Claims []… }`, `func (s hostStatus) live() []string`, `func readStatus(ctx, e) (hostStatus, error)` (used by Task 5)
  - Test helpers `floccBin`, `statusJSON(live ...string) string`, `row(k, v string) string`, `fixedNow(t)` in `summary_test.go`.

- [ ] **Step 1: Write the failing tests**

Create `installer/internal/steps/summary_test.go`:

```go
package steps

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const floccBin = "/home/flocc/.local/bin/flocc"

// fixedNow makes the days left on a token the 100 that doctorReport shows.
func fixedNow(t *testing.T) {
	t.Helper()
	old := now
	now = func() time.Time { return time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = old })
}

var jan31 = time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)

const doctorReport = `ok    config: /home/flocc/.config/flocc/config.toml
ok    github agent token: /home/flocc/.config/flocc/gh-agent.token
ok    github agent token: expires 2027-01-31 (100 days)
ok    github agent token: read-only on o/r
ok    github publish token: /home/flocc/.config/flocc/gh-publish.token
ok    github publish token: never expires
ok    github publish token: write on o/r
` + loginFailLine + `ok    tmux: server on socket flocc
9 checks, 1 failed
`

// statusJSON is flocc status --json with one live implementer session per key.
func statusJSON(live ...string) string {
	var claims []string
	for _, k := range live {
		claims = append(claims, `{"issue":"`+k+`","phase":"impl","tmux_alive":true}`)
	}
	return `{"tick_lock":"/home/flocc/.local/state/flocc/ledger.lock","claims":[` + strings.Join(claims, ",") + `]}`
}

func summaryFake(doctorCode int, doctorOut string) *host.Fake {
	return host.NewFake().
		On(doctorCmd, doctorCode, doctorOut).
		On(floccBin+" --version", 0, "flocc 0.4.0\n").
		On(claudeBin+" auth status", 1, `{"loggedIn":false}`).
		On(floccBin+" status --json", 0, statusJSON())
}

func row(k, v string) string { return fmt.Sprintf("  %-14s %s\n", k, v) }

func TestSummaryOfAFreshHostWithTheLoginPending(t *testing.T) {
	fixedNow(t)
	s := Summarize(context.Background(), testEnv(t, summaryFake(1, doctorReport)))
	if s.Failing() {
		t.Fatalf("a pending login alone must not fail ship: %+v", s)
	}
	if s.Version != "flocc 0.4.0" || s.Login != "none" || !s.LoginPending || s.Timer || len(s.Sessions) != 0 {
		t.Fatalf("%+v", s)
	}
	if got, ok := s.TokenExpiry["agent token"]; !ok || !got.Equal(jan31) {
		t.Fatalf("agent token expiry %v %v", got, ok)
	}
	if got, ok := s.TokenExpiry["publish token"]; !ok || !got.IsZero() {
		t.Fatalf("publish token never expires: %v %v", got, ok)
	}
	var b strings.Builder
	s.Target = "root@h"
	s.Print(&b)
	want := "flocc 0.4.0\n" +
		row("claude login", "none; on the desktop run: just login root@h") +
		row("agent token", "expires 2027-01-31 (100 days)") +
		row("publish token", "never expires") +
		row("timer", "off until the Claude login") +
		row("sessions", "none")
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestSummaryFailsOnAnyOtherFailLine(t *testing.T) {
	fixedNow(t)
	s := Summarize(context.Background(), testEnv(t, summaryFake(1, doctorReport+"FAIL  linear: 401\n")))
	if !s.Failing() || !slices.Equal(s.Failed, []string{"FAIL  linear: 401"}) {
		t.Fatalf("%+v", s)
	}
	var b strings.Builder
	s.Print(&b)
	if !strings.HasSuffix(b.String(), "  FAIL  linear: 401\n") {
		t.Fatalf("%q", b.String())
	}
}

func TestSummaryOfAnUpgradedHostWhoseLoginLapsed(t *testing.T) {
	fixedNow(t)
	f := summaryFake(1, doctorReport).
		On("systemctl --user is-enabled --quiet flocc.timer", 0, "").
		On(floccBin+" status --json", 0, statusJSON("NF-1"))
	s := Summarize(context.Background(), testEnv(t, f))
	var b strings.Builder
	s.Target = "root@h"
	s.Print(&b)
	out := b.String()
	if s.Failing() || !s.Timer || !s.LoginPending || !strings.Contains(out, row("timer", "on")) ||
		!strings.Contains(out, row("sessions", "NF-1 impl")) || !strings.Contains(out, "just login root@h") {
		t.Fatalf("%q", out)
	}
}

func TestSummaryLineRoundTripsAndPrintsNothingWhenEmpty(t *testing.T) {
	in := Summary{Version: "flocc 0.4.0", Login: "token-only (oauth_token)", LoginPending: true,
		TokenExpiry: map[string]time.Time{"agent token": jan31, "publish token": {}},
		Failed:      []string{"FAIL  linear: 401"}, Sessions: []string{"NF-1 impl"}, Target: "never sent"}
	line := in.Line()
	if !strings.HasPrefix(line, SummaryMarker) || strings.ContainsAny(line, "\r\n") {
		t.Fatalf("%q", line)
	}
	out, ok := ParseSummaryLine(line)
	if !ok || out.Login != in.Login || !out.LoginPending || !out.TokenExpiry["agent token"].Equal(jan31) ||
		!slices.Equal(out.Failed, in.Failed) || !slices.Equal(out.Sessions, in.Sessions) || out.Target != "" {
		t.Fatalf("%+v %v", out, ok)
	}
	if _, ok := ParseSummaryLine("flocc-summary {not json"); ok {
		t.Fatal("a corrupt line must not parse")
	}
	var b strings.Builder
	Summary{}.Print(&b)
	if b.Len() != 0 {
		t.Fatalf("%q", b.String())
	}
}

func TestSummaryReusesTheRunStepsDoctor(t *testing.T) {
	f := summaryFake(0, "")
	delete(f.Responses, doctorCmd)
	e := testEnv(t, f)
	e.lastDoctor = &doctorResult{out: "ok    tmux: server on socket flocc\n", ok: true}
	Summarize(context.Background(), e)
	if f.Ran(doctorCmd) {
		t.Fatal("doctor ran a second time")
	}
}

func TestSummaryWhenDoctorDoesNotRun(t *testing.T) {
	f := summaryFake(0, "")
	f.Responses[doctorCmd] = host.Result{Code: 2, Stderr: "Traceback (most recent call last):\n"}
	s := Summarize(context.Background(), testEnv(t, f))
	if !s.Failing() || !strings.Contains(s.Failed[0], "did not run cleanly") {
		t.Fatalf("%+v", s)
	}
}

func TestSummaryShowsAStatusFailure(t *testing.T) {
	fixedNow(t)
	f := summaryFake(1, doctorReport)
	f.Responses[floccBin+" status --json"] = host.Result{Code: 1}
	var b strings.Builder
	Summarize(context.Background(), testEnv(t, f)).Print(&b)
	if !strings.Contains(b.String(), row("sessions", "unknown: flocc status failed")) ||
		!strings.Contains(b.String(), "just login <host>") {
		t.Fatalf("%q", b.String())
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/internal/steps/ -run Summary`
Expected: build failure, `undefined: Summarize`.

- [ ] **Step 3: Implement**

Create `installer/internal/steps/summary.go`:

```go
package steps

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// SummaryMarker starts the one line that ends the service and login phases' output. The
// desktop takes that line out of the stream and prints the summary itself.
const SummaryMarker = "flocc-summary "

// Summary is what the service and login phases report at the end.
type Summary struct {
	Version      string               `json:"version"`
	Login        string               `json:"login"` // full, token-only (<method>) or none
	LoginPending bool                 `json:"login_pending"`
	TokenExpiry  map[string]time.Time `json:"token_expiry,omitempty"` // by "agent token" / "publish token"; the zero time never expires
	Failed       []string             `json:"failed,omitempty"`       // doctor FAIL lines, the Claude login's left out
	Timer        bool                 `json:"timer"`
	Sessions     []string             `json:"sessions,omitempty"` // live sessions, "NF-1 impl"
	SessionsErr  string               `json:"sessions_err,omitempty"`
	// Target is the desktop's name for the host, for the login command Print shows.
	Target string `json:"-"`
}

// now is a variable so tests can fix the days left on a token.
var now = time.Now

func (s Summary) Line() string {
	b, _ := json.Marshal(s) // plain strings, bools and times cannot fail
	return SummaryMarker + string(b)
}

func ParseSummaryLine(line string) (Summary, bool) {
	var s Summary
	rest, ok := strings.CutPrefix(line, SummaryMarker)
	if !ok || json.Unmarshal([]byte(rest), &s) != nil {
		return Summary{}, false
	}
	return s, true
}

type hostStatus struct {
	TickLock    string `json:"tick_lock"`
	LedgerError string `json:"ledger_error"`
	Claims      []struct {
		Issue     string `json:"issue"`
		Phase     string `json:"phase"`
		TmuxAlive bool   `json:"tmux_alive"`
	} `json:"claims"`
}

func (s hostStatus) live() []string {
	var live []string
	for _, c := range s.Claims {
		if c.TmuxAlive {
			live = append(live, c.Issue+" "+c.Phase)
		}
	}
	return live
}

// readStatus asks flocc status --json, which takes no lock and calls neither Linear nor GitHub.
func readStatus(ctx context.Context, e *Env) (hostStatus, error) {
	var st hostStatus
	out, err := e.MustCmd(ctx, host.Cmd{Name: e.Real(".local/bin/flocc"), Args: []string{"status", "--json"},
		Env: []string{ServicePath(e.Home)}})
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal([]byte(out), &st)
}

// doctorLine splits a line of flocc doctor's report: a status padded to 6 columns, then
// "name: detail".
func doctorLine(line string) (name, detail string, ok bool) {
	if len(line) < 7 {
		return "", "", false
	}
	return strings.Cut(line[6:], ": ")
}

// tokenDetail picks the lifetime out of a doctor line's detail; a rejected token is a FAIL line
// and shows under Failed instead.
var tokenDetail = regexp.MustCompile(`^(?:expires (\d{4}-\d{2}-\d{2})|never expires)`)

func Summarize(ctx context.Context, e *Env) Summary {
	d := e.lastDoctor
	if d == nil {
		out, fails, ok := doctor(ctx, e)
		d = &doctorResult{out: out, fails: fails, ok: ok}
	}
	s := Summary{
		Version: e.Output(ctx, e.Real(".local/bin/flocc"), "--version"),
		Login:   claudeLogin(ctx, e),
		Failed:  withoutLogin(d.fails),
		Timer:   timerOn(ctx, e),
	}
	s.LoginPending = s.Login != "full"
	if !d.ok && len(d.fails) == 0 {
		s.Failed = []string{"FAIL  doctor: did not run cleanly; run flocc doctor on the host"}
	}
	for _, line := range strings.Split(d.out, "\n") {
		name, detail, ok := doctorLine(line)
		m := tokenDetail.FindStringSubmatch(detail)
		if !ok || !strings.HasPrefix(name, "github ") || m == nil {
			continue
		}
		var exp time.Time // never expires
		if m[1] != "" {
			exp, _ = time.Parse("2006-01-02", m[1])
		}
		if s.TokenExpiry == nil {
			s.TokenExpiry = map[string]time.Time{}
		}
		s.TokenExpiry[strings.TrimPrefix(name, "github ")] = exp
	}
	if st, err := readStatus(ctx, e); err != nil {
		s.SessionsErr = "flocc status failed"
	} else {
		s.Sessions = st.live()
	}
	return s
}

// Failing reports a FAIL line other than the Claude login's, which just login fixes.
func (s Summary) Failing() bool { return len(s.Failed) > 0 }

// Print writes the summary the owner reads; Target is the desktop's name for the host.
func (s Summary) Print(w io.Writer) {
	if s.Login == "" {
		return
	}
	row := func(k, v string) { fmt.Fprintf(w, "  %-14s %s\n", k, v) }
	fmt.Fprintf(w, "%s\n", cmp.Or(s.Version, "flocc version unknown"))
	login := s.Login
	if s.LoginPending {
		login += "; on the desktop run: just login " + cmp.Or(s.Target, "<host>")
	}
	row("claude login", login)
	for _, name := range slices.Sorted(maps.Keys(s.TokenExpiry)) {
		exp := s.TokenExpiry[name]
		switch days := int(exp.Sub(now()).Hours() / 24); {
		case exp.IsZero():
			row(name, "never expires")
		case days < 0:
			row(name, "expired "+exp.Format("2006-01-02"))
		default:
			row(name, fmt.Sprintf("expires %s (%s)", exp.Format("2006-01-02"), plural(days, "day")))
		}
	}
	timer := "off"
	switch {
	case s.Timer:
		timer = "on"
	case s.LoginPending:
		timer = "off until the Claude login"
	}
	row("timer", timer)
	switch {
	case s.SessionsErr != "":
		row("sessions", "unknown: "+s.SessionsErr)
	case len(s.Sessions) == 0:
		row("sessions", "none")
	default:
		row("sessions", strings.Join(s.Sessions, ", "))
	}
	for _, f := range s.Failed {
		fmt.Fprintf(w, "  %s\n", f)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/internal/steps/ && go vet ./installer/internal/steps/`
Expected: `ok`, no vet output.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/steps/summary.go installer/internal/steps/summary_test.go
git commit -s -m "Summarize a host at the end of setup: version, login, tokens, fails, timer, sessions"
```

---

### Task 5: Host login phase

**Files:**
- Create: `installer/internal/steps/login.go`
- Test: `installer/internal/steps/login_test.go`

**Interfaces:**
- Consumes: Task 3 (`claudeLogin`, `ClaudeLogin`, `Run`, `ErrStopped`, `ErrPending`, `systemdRun`, `loginFailLine`, `declineUI`), Task 4 (`readStatus`, `hostStatus.live`, `Summarize`, `floccBin`, `statusJSON`), `worktreesRel` (`"worktrees"` after Part 1), `plural`, `e.claude()`. Task 8 adds `tick_lock` to `flocc status --json`; this task only reads the field.
- Produces:
  - `type LoginOptions struct{ Email string; Lock func(path string) (unlock func(), err error); Consent func(ctx context.Context, c host.Cmd) error }`
  - `func Login(ctx context.Context, e *Env, o LoginOptions) error`
  - `var ErrLocked error`; `const pollSessions = 30 * time.Second`
  - unexported `flockPath`, `trustWorktrees(e) error`, `waitFor(ctx, e, d) error`, `checkStatus(hostStatus) error`, `answerConsent`, `consentWaits{connect, stop}`, `defaultConsentWaits`, `consentPrompt` and `connectedLine` (regexps)
  - `Login` order: tick lock without live sessions, `claude auth login` when the login is not full, a check that the login is full, trust for `~/worktrees`, the consent run, a second login check, the Run step.

- [ ] **Step 1: Write the failing tests**

Create `installer/internal/steps/login_test.go`:

```go
package steps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

var (
	authFull = host.Result{Stdout: `{"loggedIn":true,"authMethod":"claude.ai"}`}
	authNone = host.Result{Stdout: `{"loggedIn":false}`, Code: 1}
)

type fakeLock struct {
	busy, takes, releases int
	held                  bool
}

func (l *fakeLock) lock(string) (func(), error) {
	if l.busy > 0 {
		l.busy--
		return nil, ErrLocked
	}
	if l.held {
		panic("tick lock taken twice")
	}
	l.takes++
	l.held = true
	return func() { l.held = false; l.releases++ }, nil
}

// loginFake answers auth status in the given order (the last repeats) and the Run step.
func loginFake(auth ...host.Result) *host.Fake {
	return systemdRun(host.NewFake(), 0, "ok    tmux: server on socket flocc\n").
		OnSeq(claudeBin+" auth status", auth...).
		On(floccBin+" status --json", 0, statusJSON())
}

// noteConsent records the consent run in f.Calls instead of starting claude.
func noteConsent(f *host.Fake) func(context.Context, host.Cmd) error {
	return func(_ context.Context, c host.Cmd) error {
		f.Calls = append(f.Calls, "consent: "+c.String()+" in "+c.Dir)
		return nil
	}
}

const consentCall = "consent: " + claudeBin + " remote-control in /home/flocc/worktrees"

func TestLoginWithAFullLoginOnlyAnswersTheConsent(t *testing.T) {
	f := loginFake(authFull)
	lk := &fakeLock{}
	if err := Login(context.Background(), testEnv(t, f), LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "auth login") {
			t.Fatalf("logged in again: %v", f.Calls)
		}
	}
	if !f.Ran(consentCall) || !f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("calls: %v", f.Calls)
	}
	if lk.held || lk.takes != 1 || lk.releases != 1 {
		t.Fatalf("lock %+v", lk)
	}
}

func TestLoginSignsInThenAnswersTheConsent(t *testing.T) {
	f := loginFake(authNone, authFull)
	lk := &fakeLock{}
	err := Login(context.Background(), testEnv(t, f), LoginOptions{Email: "o@example.test", Lock: lk.lock, Consent: noteConsent(f)})
	if err != nil {
		t.Fatal(err)
	}
	login := slices.Index(f.Calls, "interactive: "+claudeBin+" auth login --claudeai --email o@example.test")
	consent := slices.Index(f.Calls, consentCall)
	if login < 0 || consent < login || !f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("calls: %v", f.Calls)
	}
}

func TestLoginStopsWhenTheSignInDidNotTake(t *testing.T) {
	f := loginFake(authNone)
	err := Login(context.Background(), testEnv(t, f), LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)})
	if err == nil || !strings.Contains(err.Error(), "reports none") || f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("err=%v calls=%v", err, f.Calls)
	}
	// Before the consent: a login that did not take must fail as that, not as a consent error.
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "consent:") {
			t.Fatalf("ran the consent without a login: %v", f.Calls)
		}
	}
}

func TestLoginTrustsTheWorktreesBeforeTheConsent(t *testing.T) {
	f := loginFake(authFull)
	e := testEnv(t, f)
	var trusted bool
	consent := func(context.Context, host.Cmd) error {
		b, _ := os.ReadFile(e.HomePath(".claude.json"))
		trusted = strings.Contains(string(b), `"/home/flocc/worktrees": {`) && strings.Contains(string(b), `"hasTrustDialogAccepted": true`)
		return nil
	}
	if err := Login(context.Background(), e, LoginOptions{Lock: (&fakeLock{}).lock, Consent: consent}); err != nil {
		t.Fatal(err)
	}
	if !trusted {
		t.Fatal("~/worktrees was not trusted when the consent ran")
	}
}

func TestTrustWorktreesKeepsTheRestOfClaudeJSON(t *testing.T) {
	e := testEnv(t, host.NewFake())
	path := e.HomePath(".claude.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	// The big number would lose digits in a float64 round trip.
	in := `{"oauthAccount":{"emailAddress":"o@example.test"},"numStartups":12345678901234567890,` +
		`"projects":{"/home/flocc/src/r":{"hasTrustDialogAccepted":true,"allowedTools":[]}}}`
	os.WriteFile(path, []byte(in), 0o600)
	if err := trustWorktrees(e); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{`"emailAddress": "o@example.test"`, "12345678901234567890", `"allowedTools": []`, `"/home/flocc/worktrees": {`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in\n%s", want, b)
		}
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %o", st.Mode().Perm())
	}
}

func TestTrustWorktreesCreatesTheFileAndLeavesATrustedOneAlone(t *testing.T) {
	e := testEnv(t, host.NewFake())
	path := e.HomePath(".claude.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := trustWorktrees(e); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	same := `{"projects":{"/home/flocc/worktrees":{"hasTrustDialogAccepted":true}}}`
	os.WriteFile(path, []byte(same), 0o600)
	if err := trustWorktrees(e); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != same {
		t.Fatalf("rewrote a file that already held the trust:\n%s", b)
	}
}

func TestLoginWaitsForARunningTick(t *testing.T) {
	f := loginFake(authFull)
	e := testEnv(t, f)
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	lk := &fakeLock{busy: 2}
	if err := Login(context.Background(), e, LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	if lk.takes != 1 || len(slept) != 2 {
		t.Fatalf("takes %d slept %v", lk.takes, slept)
	}
}

func TestLoginStopsWhenTheOwnerWillNotWaitForSessions(t *testing.T) {
	f := loginFake(authNone).On(floccBin+" status --json", 0, statusJSON("NF-1"))
	e := testEnv(t, f)
	e.UI = declineUI{&ui.Scripted{A: &answers.Answers{}, Out: io.Discard}}
	lk := &fakeLock{}
	if err := Login(context.Background(), e, LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); !errors.Is(err, ErrStopped) {
		t.Fatalf("err=%v", err)
	}
	if lk.held || lk.takes != 1 {
		t.Fatalf("lock %+v", lk)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "interactive:") || strings.HasPrefix(c, "consent:") {
			t.Fatalf("changed something: %v", f.Calls)
		}
	}
}

func TestLoginWaitsForSessionsWithTheLockLetGo(t *testing.T) {
	f := loginFake(authFull).OnSeq(floccBin+" status --json",
		host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON()})
	e := testEnv(t, f)
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	lk := &fakeLock{}
	if err := Login(context.Background(), e, LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	if lk.takes != 2 || lk.releases != 2 || !slices.Equal(slept, []time.Duration{pollSessions}) {
		t.Fatalf("lock %+v slept %v", lk, slept)
	}
}

func TestLoginShowsWhichSessionsAreStillRunningOnEachLaterPoll(t *testing.T) {
	// Two looks per poll, before and under the lock.
	f := loginFake(authFull).OnSeq(floccBin+" status --json", host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON("NF-1")},
		host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON()})
	e := testEnv(t, f)
	var out bytes.Buffer
	e.UI = &ui.Scripted{A: &answers.Answers{}, Out: &out}
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if err := Login(context.Background(), e, LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	// The first poll's list is in the question; later polls say it, so the owner sees whether it shrinks.
	if !slices.Equal(slept, []time.Duration{pollSessions, pollSessions}) || strings.Count(out.String(), "NF-1 impl") != 1 {
		t.Fatalf("slept %v output %q", slept, out.String())
	}
}

func TestLoginRefusesAnUnreadableLedgerAndAHostThatNamesNoTickLock(t *testing.T) {
	for _, c := range []struct{ status, want string }{
		{`{"tick_lock":"/x/ledger.lock","claims":[],"ledger_error":"database is locked"}`, "database is locked"},
		{`{"claims":[]}`, "ship 0.4.0"},
	} {
		f := loginFake(authFull).On(floccBin+" status --json", 0, c.status)
		lk := &fakeLock{}
		err := Login(context.Background(), testEnv(t, f), LoginOptions{Lock: lk.lock, Consent: noteConsent(f)})
		if err == nil || !strings.Contains(err.Error(), c.want) || lk.takes != 0 || f.Ran(consentCall) {
			t.Errorf("%s: err=%v takes=%d calls=%v", c.status, err, lk.takes, f.Calls)
		}
	}
}

func TestLoginStopsWaitingWhenTheContextIsCancelled(t *testing.T) {
	f := loginFake(authNone).On(floccBin+" status --json", 0, statusJSON("NF-1"))
	e := testEnv(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	defer close(release)
	e.Sleep = func(time.Duration) { cancel(); <-release }
	done := make(chan error, 1)
	go func() { done <- Login(ctx, e, LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Login kept sleeping after Ctrl-C")
	}
}

func TestFreshHostShipLeavesTheLoginPendingAndLoginStartsTheTimer(t *testing.T) {
	f := systemdRun(host.NewFake(), 1, "ok    tmux: server on socket flocc\n"+loginFailLine).
		On(claudeBin+" auth status", 1, `{"loggedIn":false}`).
		On(floccBin+" --version", 0, "flocc 0.4.0\n").
		On(floccBin+" status --json", 0, statusJSON())
	e := testEnv(t, f)
	e.SkipInteractive = true
	if err := RunAll(context.Background(), e, []Step{ClaudeLogin{}, Run{}}); err != nil {
		t.Fatal(err)
	}
	s := Summarize(context.Background(), e)
	if s.Failing() || s.Login != "none" || !s.LoginPending || s.Timer ||
		!f.Ran("systemctl --user enable --now flocc-tmux.service") || f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("summary %+v calls %v", s, f.Calls)
	}
	f.On(claudeBin+" auth status", 0, authFull.Stdout).On(doctorCmd, 0, "ok    tmux: server on socket flocc\n")
	e.Pending = nil
	if err := Login(context.Background(), e, LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("timer not started: %v", f.Calls)
	}
}

func TestFlockPathExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ledger.lock")
	unlock, err := flockPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flockPath(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second take: %v", err)
	}
	unlock()
	again, err := flockPath(path)
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func consentScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The consent runs claude under util-linux script, which every Linux host has.
func needScript(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("script", "--version").Output(); err != nil || !strings.Contains(string(out), "util-linux") {
		t.Skip("util-linux script is not installed")
	}
}

var quickWaits = consentWaits{connect: 3 * time.Second, stop: 2 * time.Second}

func TestAnswerConsentAnswersAPromptSplitAcrossWritesAndWaitsForTheConnectedLine(t *testing.T) {
	needScript(t)
	got := filepath.Join(t.TempDir(), "answer")
	bin := consentScript(t, `printf 'Enable Remote '; sleep 0.2; printf 'Control? (y/n) '
read a
echo "$a" > "$1"
echo 'Remote Control connected'
exec sleep 30
`)
	start := time.Now()
	if err := answerConsent(context.Background(), host.Cmd{Name: bin, Args: []string{got}}, io.Discard, quickWaits); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != "y\n" {
		t.Fatalf("answer %q", b)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("remote-control was not stopped")
	}
}

func TestAnswerConsentTakesAConnectedLineWithoutAPromptAsConsentGiven(t *testing.T) {
	needScript(t)
	got := filepath.Join(t.TempDir(), "answer")
	bin := consentScript(t, "echo 'Remote Control connected'\nexec sleep 30\n")
	if err := answerConsent(context.Background(), host.Cmd{Name: bin, Args: []string{got}}, io.Discard, quickWaits); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(got); err == nil {
		t.Fatal("answered a prompt that never appeared")
	}
}

// Silence is not consent: another dialog, such as the folder trust, or a slow start look the same.
func TestAnswerConsentFailsWithTheOutputWhenNothingConnects(t *testing.T) {
	needScript(t)
	bin := consentScript(t, "echo 'Do you trust the files in this folder? (y/n)'\nexec sleep 30\n")
	w := quickWaits
	w.connect = 500 * time.Millisecond
	start := time.Now()
	err := answerConsent(context.Background(), host.Cmd{Name: bin}, io.Discard, w)
	if err == nil || !strings.Contains(err.Error(), "no connected line") || !strings.Contains(err.Error(), "trust the files") ||
		!strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("remote-control was not stopped")
	}
}

func TestAnswerConsentReportsAnEarlyExit(t *testing.T) {
	needScript(t)
	bin := consentScript(t, "echo 'Error: not logged in'\nexit 1\n")
	err := answerConsent(context.Background(), host.Cmd{Name: bin}, io.Discard, quickWaits)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err=%v", err)
	}
}

func TestConsentMatchersReadPastEscapeCodes(t *testing.T) {
	w := newConsentWatch(io.Discard)
	// Ink moves the cursor right instead of printing a space.
	w.Write([]byte("\x1b[1mEnable\x1b[1CRemote\x1b[0m Control\x1b[22m? (y/n) "))
	select {
	case <-w.prompt:
	default:
		t.Fatal("prompt not seen")
	}
	select {
	case <-w.connected:
		t.Fatal("the prompt is not a connected line")
	default:
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/internal/steps/ -run 'Login|Flock|Consent|Trust'`
Expected: build failure, `undefined: Login`.

- [ ] **Step 3: Implement**

Create `installer/internal/steps/login.go`:

```go
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
```

- [ ] **Step 4: Run the tests, with the race detector**

Run: `go test -race ./installer/internal/steps/ && go vet ./installer/internal/steps/`
Expected: `ok`, no race reports. The consent tests skip with a message on a machine without util-linux `script`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/steps/login.go installer/internal/steps/login_test.go
git commit -s -m "Log a host in to Claude under the tick lock and answer the Remote Control consent"
```

---

### Task 6: Desktop ship and login

**Files:**
- Modify: `installer/internal/ship/ship.go` (`Options`, `Ship`, `ship`, `shipTo`, `checkSudo`, `sshNoTTY`, `remoteCmd`, `stageFiles`, `Main`, `ValidTarget`, `connect`, `repoRoot`, `usageError`)
- Modify: `installer/internal/ship/host.go` (`HostMain` and `hostRun` use `exitCode` and `scrub`)
- Create: `installer/internal/ship/login.go`, `installer/internal/ship/summary.go`
- Test: `installer/internal/ship/ship_test.go`, create `installer/internal/ship/login_test.go` and `installer/internal/ship/summary_test.go`

**Interfaces:**
- Consumes: Task 1 (`host.Cmd.Stdin`, `host.Cmd.Stdout`, `host.Redact`, `host.Fake.Stdins`, `OnInteractiveOut`), Task 1b (`qr.Write`), Task 2 (`answers.Parse`), Task 4 (`steps.Summary`, `steps.SummaryMarker`, `steps.ParseSummaryLine`), `steps.ServiceUser`, `serviceHome`, `sshCmd`, `resolveTarget`, `realWaiter`, `Dest`. Part 1's refusal of a host with a `linear-agents` user or timer stays where Part 1 put it in `shipTo` (between `uname -m` and staging); if its probe needs a fake response in these tests, add it to the fakes the way Part 1's own ship tests do.
- Produces (the exported ones are the interface Part 2b consumes, see the top of this plan):
  - `type Options struct{ Target string; Interactive bool; Out io.Writer }`, `func Ship(ctx context.Context, o Options, answers io.Reader) (Summary, error)`, `type Summary = steps.Summary`
  - `type LoginOptions struct{ Target, Email string; Out io.Writer }`, `func Login(ctx context.Context, o LoginOptions) error`
  - `func Main(args []string) int` (`flocc-setup ship`), `func LoginMain(args []string) int`, `func ValidTarget(t string) bool`, `func OpenURL(u string)`
  - unexported: `type shipOpts struct{ Interactive bool; Answers []byte; Out io.Writer }`, `func ship(ctx, r host.Runner, root string, dest Dest, o shipOpts) (Summary, error)` (replaces the old `ship`; scrubs the Daytona token), `func login(ctx, r host.Runner, dest Dest, email string, out io.Writer, open func(url string)) error`, `type summaryTap`, `func sshNoTTY(d Dest, remote string) host.Cmd`, `type usageError string`, `func connect(ctx, target string, secrets ...string) (host.Runner, Dest, error)` (the runner's log redacts the secrets and the Daytona token; `Ship` passes the answers document's `Secrets.Values()`, and Part 2b Task 4 makes it resolve through `Resolve` for SSH connection sharing), `func repoRoot(ctx, r host.Runner) (string, error)`, `func openAnswers(path string, stdin io.Reader) (io.ReadCloser, error)`
  - `const resetStage`, `const resetStageNoPrompt`; `stageFiles(ctx, r, root, dir, arch, configDir string, private func(string) (bool, error)) error` (no answers argument)

- [ ] **Step 1: Write the failing ship tests**

In `ship_test.go`:

- Delete `TestRemoteCmdMakesTheBinaryExecutable`, `TestShipClearsTheStageWithSudoOnATerminal` and `bundleOK`.
- Change the two `stageFiles` calls to drop the `""` answers argument (`stageFiles(ctx, f, root, dir, "amd64", cfg, …)` and `stageFiles(ctx, f, "/r", t.TempDir(), "amd64", t.TempDir(), nil)`).
- `TestShipErrorsDoNotLeakTheToken` (Part 1): `ship` now returns two values, so change its call to `_, err := ship(context.Background(), f, t.TempDir(), dest, shipOpts{})`.
- `TestShipRefusesAHostThatRunsTheOldService` (Part 1 Task 5) calls `ship(ctx, f, root, dest, "", false)` twice and stops compiling. Change both calls to `_, err := ship(context.Background(), f, t.TempDir(), dest, shipOpts{})` (the second `err =`). Its `len(f.Calls) != 2` check still holds: `refuseLegacy` runs right after `uname -m` and before `checkSudo`.

Then add:

```go
// stageOK accepts git and scp, whose arguments hold the random stage directory.
type stageOK struct{ *host.Fake }

func (s *stageOK) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	switch c.Name {
	case "git":
		if slices.Contains(c.Args, "config") {
			return host.Result{Stdout: "x@x.test\n"}, nil
		}
		return host.Result{}, nil
	case "scp":
		s.Fake.Calls = append(s.Fake.Calls, "scp")
		return host.Result{}, nil
	}
	return s.Fake.Run(ctx, c)
}

func shipRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "build"), 0o755)
	os.WriteFile(filepath.Join(root, "build", "flocc-setup-linux-amd64"), []byte("bin"), 0o755)
	cfg := t.TempDir()
	writeJSON(t, filepath.Join(cfg, "settings.json"), map[string]any{})
	writeJSON(t, filepath.Join(cfg, "plugins/known_marketplaces.json"), map[string]any{})
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	return root
}

func TestRemoteCmd(t *testing.T) {
	for _, c := range []struct {
		interactive bool
		want        string
	}{
		{false, "chmod 755 /tmp/flocc-setup/flocc-setup && sudo -n /tmp/flocc-setup/flocc-setup --answers -"},
		{true, "chmod 755 /tmp/flocc-setup/flocc-setup && sudo /tmp/flocc-setup/flocc-setup"},
	} {
		if got := remoteCmd(c.interactive); got != c.want {
			t.Errorf("interactive=%v: %q", c.interactive, got)
		}
	}
}

// okSudo is a host that passes the probes of a non-interactive ship; the ssh calls that carry
// secrets or sudo pass -T, the uname probe does not. Add `errors` and `io` to ship_test.go's imports.
func okSudo(dest string) *stageOK {
	return &stageOK{host.NewFake().
		On("ssh "+dest+" uname -m", 0, "x86_64\n").
		On("ssh -T "+dest+" sudo -n true", 0, "").
		On("ssh -T "+dest+" "+resetStageNoPrompt, 0, "")}
}

func TestShipSendsTheAnswersOnStdinWithoutATerminal(t *testing.T) {
	root := shipRoot(t)
	doc := []byte("[secrets]\nlinear_key = \"lin_api_s3cret\"\n")
	run := "ssh -T root@h " + remoteCmd(false)
	f := okSudo("root@h")
	if _, err := ship(context.Background(), f, root, Dest{SSH: "root@h"}, shipOpts{Answers: doc, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("interactive: "+run) || f.Stdins[run] != string(doc) {
		t.Fatalf("calls %v stdin %q", f.Calls, f.Stdins[run])
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "ssh -t") || strings.Contains(c, "s3cret") {
			t.Errorf("%s", c)
		}
	}
}

// A RequestTTY setting in the owner's ssh config would otherwise give the run a pty, which
// echoes the answers document back and never passes EOF.
func TestNonInteractiveShipForbidsAPtyOnEverySudoAndSecretCall(t *testing.T) {
	f := okSudo("root@h")
	if _, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, c := range f.Calls {
		c = strings.TrimPrefix(c, "interactive: ")
		// uname and Part 1's legacy-user probe carry no sudo and no secrets.
		if !strings.HasPrefix(c, "ssh ") || !strings.Contains(c, "sudo ") {
			continue
		}
		checked++
		if !strings.HasPrefix(c, "ssh -T ") {
			t.Errorf("no -T: %s", c)
		}
	}
	if checked != 3 {
		t.Fatalf("want the sudo probe, the stage reset and the run, got %d: %v", checked, f.Calls)
	}
}

func TestSSHNoTTYPutsDashTAfterTheOptions(t *testing.T) {
	c := sshNoTTY(Dest{SSH: "u@h", Opts: []string{"-o", "BatchMode=yes"}}, "true")
	if got := c.String(); got != "ssh -o BatchMode=yes -T u@h true" {
		t.Fatal(got)
	}
}

func TestShipWithoutAnswersStillClosesStdin(t *testing.T) {
	run := "ssh -T h " + remoteCmd(false)
	f := okSudo("h")
	if _, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "h"}, shipOpts{Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if s, ok := f.Stdins[run]; !ok || s != "" {
		t.Fatalf("the run must get an empty stdin, not the terminal: %q %v", s, ok)
	}
}

func TestShipStopsBeforeStagingWithoutPasswordlessSudo(t *testing.T) {
	f := &stageOK{host.NewFake().
		On("ssh ada@h uname -m", 0, "x86_64\n").
		On("ssh -T ada@h sudo -n true", 1, "").
		On("ssh -T ada@h id -un", 0, "ada\n")}
	_, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "ada@h"}, shipOpts{Out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "give ada passwordless sudo, or use root") {
		t.Fatalf("%v", err)
	}
	if f.Ran("scp") || f.Ran("ssh -T ada@h "+resetStageNoPrompt) {
		t.Fatalf("staged anyway: %v", f.Calls)
	}
}

func TestShipNamesAMissingSudo(t *testing.T) {
	f := &stageOK{host.NewFake().On("ssh root@h uname -m", 0, "x86_64\n").On("ssh -T root@h sudo -n true", 127, "")}
	_, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "install sudo") {
		t.Fatalf("%v", err)
	}
}

func TestShipInteractiveKeepsTheTerminal(t *testing.T) {
	f := &stageOK{host.NewFake().On("ssh h uname -m", 0, "x86_64\n")}
	if _, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "h"}, shipOpts{Interactive: true, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"interactive: ssh -t h " + resetStage, "interactive: ssh -t h " + remoteCmd(true)} {
		if !f.Ran(want) {
			t.Errorf("missing %s in %v", want, f.Calls)
		}
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "sudo -n true") || strings.Contains(c, " -T ") {
			t.Errorf("an interactive ship lets sudo ask on the terminal: %s", c)
		}
	}
}

func TestShipReturnsTheHostsSummaryAndDropsItsLine(t *testing.T) {
	want := Summary{Version: "flocc 0.4.0", Login: "none", LoginPending: true}
	run := "ssh -T root@h " + remoteCmd(false)
	f := okSudo("root@h")
	f.OnInteractiveOut(run, "setup chatter\r\n\r\n"+want.Line()+"\r\n")
	var out strings.Builder
	got, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: &out})
	if err != nil || got.Version != want.Version || !got.LoginPending {
		t.Fatalf("%+v %v", got, err)
	}
	if out.String() != "setup chatter\r\n\r\n" {
		t.Fatalf("output %q", out.String())
	}
}

func TestShipKeepsTheSummaryWhenTheHostExitsNonZero(t *testing.T) {
	sum := Summary{Version: "flocc 0.4.0", Login: "full", Failed: []string{"FAIL  linear: 401"}}
	run := "ssh -T root@h " + remoteCmd(false)
	f := okSudo("root@h")
	f.OnInteractiveOut(run, sum.Line()+"\n").OnInteractive(run, errors.New("exit status 1"))
	got, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: io.Discard})
	if err == nil || !got.Failing() {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestShipRefusesAnUnsafeTargetAndInteractiveAnswers(t *testing.T) {
	var u usageError
	if _, err := Ship(context.Background(), Options{Target: "-oProxyCommand=x"}, nil); !errors.As(err, &u) {
		t.Fatalf("%v", err)
	}
	if _, err := Ship(context.Background(), Options{Target: "h", Interactive: true}, strings.NewReader("")); !errors.As(err, &u) {
		t.Fatalf("%v", err)
	}
}

func TestShipRejectsABrokenAnswersDocumentBeforeConnecting(t *testing.T) {
	_, err := Ship(context.Background(), Options{Target: "h"}, strings.NewReader("[secrets]\nlinear_key = \"lin_api_s3cret\n"))
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("%v", err)
	}
}

func TestOpenAnswersReadsStdinForDashAndAFileOtherwise(t *testing.T) {
	rc, err := openAnswers("-", strings.NewReader("from stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != "from stdin" {
		t.Fatalf("%q", b)
	}
	path := filepath.Join(t.TempDir(), "a.toml")
	os.WriteFile(path, []byte("from file"), 0o600)
	rc, err = openAnswers(path, strings.NewReader("unused"))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if b, _ := io.ReadAll(rc); string(b) != "from file" {
		t.Fatalf("%q", b)
	}
}

func TestValidTarget(t *testing.T) {
	for _, ok := range []string{"root@flocc-nimble", "daytona:flocc-smoke", "box.tailnet.ts.net"} {
		if !ValidTarget(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "h;rm -rf ~", "a b", "$(id)", "h`id`"} {
		if ValidTarget(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMainRefusesInteractiveWithAnswers(t *testing.T) {
	if code := Main([]string{"--interactive", "--answers", "a.toml", "h"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
```

- [ ] **Step 2: Write the failing login tests**

Create `installer/internal/ship/login_test.go`:

```go
package ship

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const signIn = "https://claude.ai/oauth/authorize?code=true&state=abc"

func TestLoginRunsTheLoginPhaseOnATerminalOpensTheURLAndPrintsTheSummary(t *testing.T) {
	call := "ssh -t root@h sudo -iu flocc /home/flocc/.cache/flocc-setup/flocc-setup --phase login --email o@example.test"
	sum := Summary{Version: "flocc 0.4.0", Login: "full", Timer: true}
	f := host.NewFake().OnInteractiveOut(call, "Opening browser to sign in…\r\n\x1b[4m"+signIn+"\x1b[0m\r\nPaste code here if prompted > "+
		"\r\n\r\n"+sum.Line()+"\r\n")
	var out strings.Builder
	var opened []string
	err := login(context.Background(), f, Dest{SSH: "root@h"}, "o@example.test", &out, func(u string) { opened = append(opened, u) })
	if err != nil || !f.Ran("interactive: "+call) {
		t.Fatalf("%v %v", err, f.Calls)
	}
	if !slices.Equal(opened, []string{signIn}) || !strings.Contains(out.String(), "Paste code here") {
		t.Fatalf("opened %v out %q", opened, out.String())
	}
	if strings.Contains(out.String(), "flocc-summary") || !strings.Contains(out.String(), "flocc 0.4.0\n") ||
		!strings.Contains(out.String(), fmt.Sprintf("  %-14s %s\n", "timer", "on")) {
		t.Fatalf("the summary line should be replaced by the printed summary: %q", out.String())
	}
}

func TestURLWatchJoinsChunksAndOpensEachURLOnce(t *testing.T) {
	var out strings.Builder
	var opened []string
	w := &urlWatch{out: &out, open: func(u string) { opened = append(opened, u) }, seen: map[string]bool{}}
	for _, chunk := range []string{"visit https://claude.ai/oa", "uth/authorize?code=true&state=abc", "\r\nagain " + signIn + "\n"} {
		w.Write([]byte(chunk))
	}
	if !slices.Equal(opened, []string{signIn}) {
		t.Fatalf("%v", opened)
	}
}

func TestLoginScrubsTheDaytonaToken(t *testing.T) {
	dest := Dest{SSH: "tok@gw", Token: "tok"}
	f := host.NewFake().OnInteractive("ssh -t tok@gw "+loginRemote(""), errors.New("ssh tok@gw failed"))
	err := login(context.Background(), f, dest, "", &strings.Builder{}, func(string) {})
	if err == nil || strings.Contains(err.Error(), "tok@") {
		t.Fatalf("%v", err)
	}
}

func TestLoginRefusesAnUnsafeTargetAndEmail(t *testing.T) {
	var u usageError
	for _, o := range []LoginOptions{{Target: "h;id"}, {Target: "-oProxyCommand=x"}, {Target: "root@h", Email: "a@b;id"}} {
		if err := Login(context.Background(), o); !errors.As(err, &u) {
			t.Errorf("%+v: %v", o, err)
		}
	}
	if code := LoginMain([]string{"--email", "a@b;id", "root@h"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
```

Create `installer/internal/ship/summary_test.go`. The filter sees the host's output as pty chunks: the summary line can be split anywhere, and the sign-in prompt has no newline after it and must reach the owner at once.

```go
package ship

import (
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/steps"
)

func TestSummaryTapTakesOutTheSummaryLineSplitAnywhere(t *testing.T) {
	want := Summary{Version: "flocc 0.4.0", Login: "none", LoginPending: true}
	input := "setup output\r\nflocc: ok\r\n\r\n" + want.Line() + "\r\nPaste code here > "
	var out strings.Builder
	tap := &summaryTap{out: &out}
	for i := 0; i < len(input); i++ {
		tap.Write([]byte{input[i]})
	}
	tap.Flush()
	if out.String() != "setup output\r\nflocc: ok\r\n\r\nPaste code here > " {
		t.Fatalf("output %q", out.String())
	}
	if got, ok := tap.Summary(); !ok || got.Login != "none" || !got.LoginPending {
		t.Fatalf("%+v %v", got, ok)
	}
}

func TestSummaryTapPassesAPromptAtOnce(t *testing.T) {
	var out strings.Builder
	tap := &summaryTap{out: &out}
	tap.Write([]byte("Paste code here > "))
	if out.String() != "Paste code here > " {
		t.Fatalf("the prompt was held back: %q", out.String())
	}
}

func TestSummaryTapKeepsALineThatOnlyLooksLikeTheMarker(t *testing.T) {
	in := steps.SummaryMarker + "{not json\nflocc-sum is not it either\n"
	var out strings.Builder
	tap := &summaryTap{out: &out}
	tap.Write([]byte(in))
	tap.Flush()
	if out.String() != in {
		t.Fatalf("output %q", out.String())
	}
	if _, ok := tap.Summary(); ok {
		t.Fatal("a corrupt line must not count as a summary")
	}
}

func TestSummaryTapFlushesAHeldPartialLine(t *testing.T) {
	var out strings.Builder
	tap := &summaryTap{out: &out}
	tap.Write([]byte("flocc-sum"))
	if out.Len() != 0 {
		t.Fatalf("held bytes should wait for the rest of the marker: %q", out.String())
	}
	tap.Flush()
	if out.String() != "flocc-sum" {
		t.Fatalf("%q", out.String())
	}
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test ./installer/internal/ship/`
Expected: build failure, `undefined: shipOpts`, `undefined: login`, `undefined: summaryTap`.

- [ ] **Step 4: Implement ship.go**

Add `"bytes"`, `"io"`, `"regexp"`, `"slices"` and the answers package (as `answersPkg`) to the imports. Replace `resetStage`, `stageFiles`'s answers handling, `Main`, `ship`, `shipTo` and `remoteCmd`:

```go
const (
	resetStage = "sudo rm -rf /tmp/flocc-setup && mkdir -p /tmp/flocc-setup"
	// Without a terminal sudo must fail rather than wait for a password.
	resetStageNoPrompt = "sudo -n rm -rf /tmp/flocc-setup && mkdir -p /tmp/flocc-setup"
)

// Options are what Ship needs besides the stage and the answers.
type Options struct {
	// Target is the ssh target or daytona:<sandbox>, as the owner names the host.
	Target string
	// Interactive runs the host's forms on this terminal over ssh -t. Otherwise every answer,
	// secrets included, comes from the answers reader on stdin and nothing prompts.
	Interactive bool
	// Out receives the host's output, less the summary line; nil is stdout.
	Out io.Writer
}

type shipOpts struct {
	Interactive bool
	Answers     []byte
	Out         io.Writer
}

// usageError is a refusal of the arguments, which exits 2.
type usageError string

func (u usageError) Error() string { return string(u) }

var targetRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]*$`)

// ValidTarget reports whether t is safe as an ssh destination: no leading dash, which ssh would
// read as an option, and no shell metacharacters.
func ValidTarget(t string) bool { return targetRE.MatchString(t) }

func badTarget(t string) error {
	return usageError(fmt.Sprintf("%q is not an ssh target or daytona:<sandbox>", t))
}

// connect resolves target to an ssh destination and a runner whose log hides its token and
// secrets.
func connect(ctx context.Context, target string, secrets ...string) (host.Runner, Dest, error) {
	home, _ := os.UserHomeDir()
	dest, err := resolveTarget(ctx, target, filepath.Join(home, ".config/daytona/config.json"), &http.Client{Timeout: 30 * time.Second}, realWaiter(os.Stderr))
	if err != nil {
		return nil, Dest{}, err
	}
	return host.Exec{Log: host.Redact(os.Stderr, append(secrets, dest.Token)...)}, dest, nil
}

func repoRoot(ctx context.Context, r host.Runner) (string, error) {
	out, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"rev-parse", "--show-toplevel"}})
	return strings.TrimSpace(out), err
}

// sshNoTTY is sshCmd(d, false, …) with -T. A RequestTTY setting in the owner's ssh config would
// otherwise give the run a pty, which echoes the answers document back and never passes EOF.
func sshNoTTY(d Dest, remote string) host.Cmd {
	c := sshCmd(d, false, remote)
	c.Args = slices.Insert(c.Args, len(d.Opts), "-T")
	return c
}
```

In `stageFiles`, drop the `answersIn` parameter and the final `if answersIn != "" { … }` block, ending with `return plan.List.Write(filepath.Join(dir, "plugins.json"))`.

```go
func Main(args []string) int {
	fs := flag.NewFlagSet("flocc-setup ship", flag.ContinueOnError)
	interactive := fs.Bool("interactive", false, "answer the host's forms on this terminal (ssh -t) instead of from --answers")
	answersIn := fs.String("answers", "", `answers document, secrets included, sent to the host on stdin; "-" reads this terminal's stdin`)
	stageOnly := fs.String("stage-only", "", "write the stage for linux/amd64 into this directory and stop")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *interactive && *answersIn != "" {
		fmt.Fprintln(os.Stderr, "ship: --interactive asks for every answer on the host; drop --answers")
		return 2
	}
	ctx := context.Background()
	if *stageOnly != "" {
		r := host.Exec{Log: os.Stderr}
		root, err := repoRoot(ctx, r)
		if err == nil {
			if err = os.MkdirAll(*stageOnly, 0o755); err == nil {
				err = stageFiles(ctx, r, root, *stageOnly, "amd64", claudeConfigDir(), ghPrivate)
			}
		}
		return exitCode(err, "ship")
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: flocc-setup ship [--interactive | --answers FILE|-] <ssh-target|daytona:SANDBOX>")
		return 2
	}
	var in io.Reader
	if *answersIn != "" {
		rc, err := openAnswers(*answersIn, os.Stdin)
		if err != nil {
			return exitCode(err, "ship")
		}
		defer rc.Close()
		in = rc
	}
	s, err := Ship(ctx, Options{Target: fs.Arg(0), Interactive: *interactive, Out: os.Stdout}, in)
	s.Print(os.Stdout)
	return exitCode(err, "ship")
}

func openAnswers(path string, stdin io.Reader) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(stdin), nil
	}
	return os.Open(path)
}

// Ship stages the installer on the host named by o.Target and runs it, with the answers document
// (TOML, secrets included) on stdin unless o.Interactive. It returns the summary the host ended
// with, which is zero when the host never got that far; it does not print it. Errors come back
// with the SSH token scrubbed: CmdError text carries the whole command line.
func Ship(ctx context.Context, o Options, answers io.Reader) (Summary, error) {
	if !ValidTarget(o.Target) {
		return Summary{}, badTarget(o.Target)
	}
	var doc []byte
	var secrets []string
	if answers != nil {
		if o.Interactive {
			return Summary{}, usageError("--interactive asks for every answer on the host; drop the answers")
		}
		var err error
		var a *answersPkg.Answers
		if doc, err = io.ReadAll(answers); err == nil {
			// Here, so a typo fails before anything reaches the host.
			a, err = answersPkg.Parse(doc)
		}
		if err != nil {
			return Summary{}, err
		}
		// A second guard on this side: no command line carries them, but the log must not either.
		secrets = a.Secrets.Values()
	}
	r, dest, err := connect(ctx, o.Target, secrets...)
	if err != nil {
		return Summary{}, err
	}
	root, err := repoRoot(ctx, host.Exec{Log: os.Stderr})
	if err != nil {
		return Summary{}, err
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	return ship(ctx, r, root, dest, shipOpts{Interactive: o.Interactive, Answers: doc, Out: o.Out})
}

func ship(ctx context.Context, r host.Runner, root string, dest Dest, o shipOpts) (Summary, error) {
	tap := &summaryTap{out: o.Out}
	err := scrub(shipTo(ctx, r, root, dest, o, tap), dest.Token)
	tap.Flush()
	s, _ := tap.Summary()
	return s, err
}

func shipTo(ctx context.Context, r host.Runner, root string, dest Dest, o shipOpts, tap io.Writer) error {
	unameM, err := host.Must(ctx, r, sshCmd(dest, false, "uname -m"))
	if err != nil {
		return err
	}
	arch, err := archFor(unameM)
	if err != nil {
		return err
	}
	if !o.Interactive {
		if err := checkSudo(ctx, r, dest); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "flocc-ship-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := stageFiles(ctx, r, root, dir, arch, claudeConfigDir(), ghPrivate); err != nil {
		return err
	}
	// A finished run leaves the stage root:flocc 750, so only sudo can clear it.
	if o.Interactive {
		err = r.Interactive(ctx, sshCmd(dest, true, resetStage))
	} else {
		_, err = host.Must(ctx, r, sshNoTTY(dest, resetStageNoPrompt))
	}
	if err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	scp := append(append([]string{"-q"}, dest.Opts...), files...)
	if _, err := host.Must(ctx, r, host.Cmd{Name: "scp", Args: append(scp, dest.SSH+":/tmp/flocc-setup/")}); err != nil {
		return err
	}
	c := sshCmd(dest, true, remoteCmd(true))
	if !o.Interactive {
		c = sshNoTTY(dest, remoteCmd(false))
		c.Stdin = bytes.NewReader(o.Answers)
	}
	c.Stdout = tap
	return r.Interactive(ctx, c)
}

// checkSudo runs before anything is staged: without a terminal a sudo password prompt fails the
// run halfway.
func checkSudo(ctx context.Context, r host.Runner, dest Dest) error {
	res, err := r.Run(ctx, sshNoTTY(dest, "sudo -n true"))
	switch {
	case err != nil:
		return err
	case res.Code == 0:
		return nil
	case res.Code == 127:
		return errors.New("sudo is not installed on the host; install it and ship again")
	}
	user, err := host.Must(ctx, r, sshNoTTY(dest, "id -un"))
	if err != nil {
		return err
	}
	return fmt.Errorf("sudo asks for a password: give %s passwordless sudo, or use root", strings.TrimSpace(user))
}

func remoteCmd(interactive bool) string {
	// scp through the Daytona gateway drops the exec bit.
	remote := "chmod 755 /tmp/flocc-setup/flocc-setup && "
	if interactive {
		return remote + "sudo /tmp/flocc-setup/flocc-setup"
	}
	return remote + "sudo -n /tmp/flocc-setup/flocc-setup --answers -"
}
```

The `answers` parameter of `Ship` shadows the package, so import the package as `answersPkg "github.com/NovusEdge/flocc/installer/internal/answers"` in ship.go. Keep Part 1's refusal of a host with a `linear-agents` user or timer where Part 1 put it in `shipTo`, before `checkSudo`. `TestMainRefusesInteractiveWithAnswers` returns before anything runs; `Main` validates nothing else itself, since `Ship` returns a `usageError` that `exitCode` turns into status 2.

Create `installer/internal/ship/summary.go`:

```go
package ship

import (
	"bytes"
	"io"

	"github.com/NovusEdge/flocc/installer/internal/steps"
)

// Summary is what the host's service and login phases end with; Print renders it.
type Summary = steps.Summary

var marker = []byte(steps.SummaryMarker)

// summaryTap passes the host's output through and takes out the flocc-summary line. The output
// arrives in pty chunks, so the line can be split anywhere, and the sign-in prompt has no newline
// after it: a line is held back only while it could still turn into the marker.
type summaryTap struct {
	out     io.Writer
	held    []byte // the current line so far, while it is a prefix of the marker or starts with it
	passing bool   // the current line has diverged from the marker and goes straight through
	sum     Summary
	have    bool
}

func (t *summaryTap) Write(p []byte) (int, error) {
	var emit []byte
	for _, b := range p {
		if t.passing {
			emit = append(emit, b)
			t.passing = b != '\n'
			continue
		}
		t.held = append(t.held, b)
		if b == '\n' {
			if s, ok := steps.ParseSummaryLine(string(bytes.TrimRight(t.held, "\r\n"))); ok {
				t.sum, t.have = s, true
			} else {
				emit = append(emit, t.held...)
			}
			t.held = t.held[:0]
		} else if !bytes.HasPrefix(marker, t.held) && !bytes.HasPrefix(t.held, marker) {
			emit = append(emit, t.held...)
			t.held, t.passing = t.held[:0], true
		}
	}
	if len(emit) > 0 {
		if _, err := t.out.Write(emit); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush passes on a held partial line, which the stream ended in the middle of.
func (t *summaryTap) Flush() {
	if len(t.held) > 0 {
		t.out.Write(t.held)
		t.held = t.held[:0]
	}
}

func (t *summaryTap) Summary() (Summary, bool) { return t.sum, t.have }
```

- [ ] **Step 5: Implement login.go and the host.go helpers**

Create `installer/internal/ship/login.go`:

```go
package ship

import (
	"bytes"
	"context"
	"errors"
	"flag"
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

// LoginMain runs the host's login phase over ssh -t and opens the sign-in URL it prints here.
func LoginMain(args []string) int {
	fs := flag.NewFlagSet("flocc-setup login", flag.ContinueOnError)
	addr := fs.String("email", "", "the claude.ai account to sign in as")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: flocc-setup login [--email ADDR] <ssh-target|daytona:SANDBOX>")
		return 2
	}
	return exitCode(Login(context.Background(), LoginOptions{Target: fs.Arg(0), Email: *addr, Out: os.Stdout}), "login")
}

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
	if o.Email != "" && !emailRE.MatchString(o.Email) {
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
	return login(ctx, r, dest, o.Email, out, OpenURL)
}

func login(ctx context.Context, r host.Runner, dest Dest, addr string, out io.Writer, open func(url string)) error {
	tap := &summaryTap{out: &urlWatch{out: out, open: open, seen: map[string]bool{}}}
	c := sshCmd(dest, true, loginRemote(addr))
	c.Stdout = tap
	err := scrub(r.Interactive(ctx, c), dest.Token)
	tap.Flush()
	if s, ok := tap.Summary(); ok {
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

// exitCode passes on the remote command's exit status; ssh killed by a signal reports -1. A
// usageError exits 2.
func exitCode(err error, what string) int {
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

// scrub removes a Daytona SSH token from an error's text. An ExitError carries no command line
// and is kept as it is, so callers can still read its exit code.
func scrub(err error, token string) error {
	var exit *exec.ExitError
	if err == nil || token == "" || errors.As(err, &exit) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
}
```

In `host.go`, end `HostMain` with `return exitCode(err, "host")` in place of its error block, and make `hostRun` `return scrub(r.Interactive(ctx, sshCmd(dest, true, asServiceUser(command))), dest.Token)`. Remove imports that are no longer used (`errors`, `os/exec`, `strings` if unused).

- [ ] **Step 6: Run the tests**

Run: `go test -race ./installer/internal/ship/ && go vet ./installer/internal/ship/`
Expected: `ok`, no vet output.

- [ ] **Step 7: Commit**

```bash
git add installer/internal/ship
git commit -s -m "Ship answers on stdin without a terminal, check sudo first, return the host's summary, and add the desktop login"
```

---

### Task 7: Installer entry point

**Files:**
- Modify: `installer/main.go`
- Test: `installer/main_test.go`

**Interfaces:**
- Consumes: Task 1 (`host.Redact`, `Cmd.Stdin`, `Fake.Stdins`), Task 2 (`answers.Parse`, `Secrets.Values`), Task 3 (`steps.ClaudeLogin`, `steps.DoctorFailed`, `steps.ErrStopped`), Task 4 (`steps.Summarize`, `Summary.Line`, `Summary.Failing`, `steps.ParseSummaryLine`), Task 5 (`steps.Login`, `steps.LoginOptions`), Task 6 (`ship.LoginMain`). The service phase's saved answers path is Part 1's `savedAnswersPath(home)`, `~/.config/flocc/answers.toml`.
- Produces: host flags `--phase root|service|login`, `--answers -`, `--email ADDR`; desktop subcommand `flocc-setup login`. `--skip-interactive` and the host's `--target` are gone: the desktop prints the summary and knows the target. Functions `handoffArgs(fromStdin bool) []string`, `handoff(ctx, r, stage string, args []string, doc io.Reader) error`, `stageNames(stage string) ([]string, error)`, `readAnswers(r io.Reader) ([]byte, *answers.Answers, error)`, `finish(ctx, env, err, summary bool, stdout, stderr io.Writer, logPath string) int`.

- [ ] **Step 1: Write the failing tests**

In `main_test.go` (after Part 1):

- Delete `TestHandoffRemovesAStagedAnswersToml` (Part 1) and `TestHandoffRemovesTheStagedAnswersOnlyOnSuccess`: both call the old `handoff(ctx, f, stage, "answers.toml", nil)`, and nothing is staged as `answers.toml` any more.
- Keep Part 1's `TestSavedAnswersLiveOutsideTheStage`.
- Replace `TestCheckPhase`, `TestHandoffArgs`, `TestStageNamesKeepsOnlyKnownSafeFiles` and `TestHandoffNeverWritesIntoTheServiceHomeAsRoot`.
- In `TestCopyScriptHasNoExpansionAndCopiesTheStage` call `stageNames(stage)`.
- Add `bytes`, `errors`, `io`, `time` and the answers, host, steps and ui packages to the imports.

```go
func TestCheckPhase(t *testing.T) {
	if checkPhase("root", 0) != nil || checkPhase("service", 1001) != nil || checkPhase("login", 1001) != nil {
		t.Fatal("valid phases refused")
	}
	for _, c := range []struct {
		phase string
		euid  int
	}{{"root", 1000}, {"service", 0}, {"login", 0}, {"other", 0}} {
		if checkPhase(c.phase, c.euid) == nil {
			t.Errorf("%s as %d: want refusal", c.phase, c.euid)
		}
	}
}

func TestHandoffArgs(t *testing.T) {
	if got := handoffArgs(true); !slices.Equal(got, []string{"--phase", "service", "--answers", "-"}) {
		t.Fatalf("got %v", got)
	}
	if got := handoffArgs(false); !slices.Equal(got, []string{"--phase", "service"}) {
		t.Fatalf("got %v", got)
	}
}

func TestStageNamesKeepsOnlyKnownSafeFiles(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle", "plugins.json", "identity.json", "mp-X.tar", "e2e.toml",
		"setup.log", "-bad.tar", "a b.tar")
	got, err := stageNames(stage)
	want := []string{"flocc-setup", "flocc.bundle", "identity.json", "mp-X.tar", "plugins.json"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := stageNames(fillStage(t, "plugins.json")); err == nil {
		t.Fatal("a stage without flocc-setup must be refused")
	}
}

func handoffFake(stage string) *host.Fake {
	return host.NewFake().
		On("chown -R root:flocc "+stage, 0, "").
		On("chmod -R g+rX,o-rwx "+stage, 0, "").
		On("sudo -iu flocc sh -c "+copyScript(rootStage, []string{"flocc-setup", "flocc.bundle"}), 0, "")
}

func TestHandoffNeverWritesIntoTheServiceHomeAsRoot(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle")
	f := handoffFake(stage)
	if err := handoff(context.Background(), f, stage, handoffArgs(false), nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if c == "chown -R root:flocc "+stage || c == "chmod -R g+rX,o-rwx "+stage {
			continue
		}
		if !strings.HasPrefix(c, "sudo -iu flocc") && !strings.HasPrefix(c, "interactive: sudo -iu flocc") {
			t.Errorf("root ran something besides the stage chown/chmod and the service user's own commands: %s", c)
		}
	}
}

func TestHandoffPipesTheAnswersToTheServicePhase(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle")
	f := handoffFake(stage)
	doc := "[secrets]\nlinear_key = \"lin_api_s3cret\"\n"
	if err := handoff(context.Background(), f, stage, handoffArgs(true), strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
	call := "sudo -iu flocc /home/flocc/.cache/flocc-setup/flocc-setup --phase service --answers -"
	if !f.Ran("interactive: "+call) || f.Stdins[call] != doc {
		t.Fatalf("calls %v stdin %q", f.Calls, f.Stdins[call])
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "s3cret") {
			t.Errorf("secret in argv: %s", c)
		}
	}
}

func TestReadAnswersKeepsTheBytesAndLeavesSecretsOutOfErrors(t *testing.T) {
	if _, _, err := readAnswers(strings.NewReader("[secrets]\nlinear_key = \"lin_api_s3cret\n")); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("%v", err)
	}
	doc := "[secrets]\nlinear_key = \"lin_api_s3cret\"\n"
	raw, a, err := readAnswers(strings.NewReader(doc))
	if err != nil || string(raw) != doc || !slices.Equal(a.Secrets.Values(), []string{"lin_api_s3cret"}) {
		t.Fatalf("%q %+v %v", raw, a, err)
	}
}

// recorder succeeds at every command, so a step runs to its end, and keeps each command whole,
// environment included.
type recorder struct{ cmds []host.Cmd }

func (r *recorder) Run(_ context.Context, c host.Cmd) (host.Result, error) {
	r.cmds = append(r.cmds, c)
	if strings.HasSuffix(c.Name, "/claude") && slices.Equal(c.Args, []string{"auth", "status"}) {
		return host.Result{Stdout: `{"loggedIn":false}`, Code: 1}, nil
	}
	return host.Result{}, nil
}

func (r *recorder) Interactive(ctx context.Context, c host.Cmd) error {
	_, err := r.Run(ctx, c)
	return err
}

const secretsDoc = `team_key = "NF"
digest_issue = "NF-1"

[[repos]]
name = "r"
url = "https://github.com/o/r"
base_branch = "main"

[secrets]
linear_key = "lin_api_s3cretA"
agent_token = "github_pat_s3cretB"
publish_token = "github_pat_s3cretC"
`

// Redaction is the second guard; this proves the first: no step puts a secret in argv or output,
// and the agent token reaches a command only as GH_TOKEN in the environment.
func TestServiceStepsKeepSecretsOutOfArgvAndOutput(t *testing.T) {
	// Scripted answers skip repo discovery; --interactive's discovery runs gh with the agent token.
	for _, skip := range []bool{true, false} {
		_, in, err := readAnswers(strings.NewReader(secretsDoc))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		rec, root := &recorder{}, t.TempDir()
		e := &steps.Env{Run: rec, UI: &ui.Scripted{A: in, Out: &out}, Root: root, Home: "/home/flocc", Answers: in,
			AnswersPath: filepath.Join(root, "answers.toml"), SkipInteractive: skip, Sleep: func(time.Duration) {}}
		for _, s := range []steps.Step{steps.Secrets{}, steps.Config{}, steps.ClaudeLogin{}} {
			if err := s.Apply(context.Background(), e); err != nil && !errors.Is(err, steps.ErrPending) {
				t.Fatalf("skip=%v %s: %v", skip, s.Name(), err)
			}
		}
		var sawToken bool
		for _, s := range in.Secrets.Values() {
			for _, c := range rec.cmds {
				if strings.Contains(c.String(), s) {
					t.Errorf("skip=%v: secret in argv: %s", skip, c)
				}
				for _, env := range c.Env {
					switch {
					case env == "GH_TOKEN="+in.Secrets.AgentToken:
						sawToken = true
					case strings.Contains(env, s):
						t.Errorf("skip=%v: secret in the environment: %s", skip, c.Name)
					}
				}
			}
			if strings.Contains(out.String(), s) {
				t.Errorf("skip=%v: secret on stdout: %s", skip, out.String())
			}
		}
		if sawToken == skip {
			t.Errorf("skip=%v: GH_TOKEN seen=%v; discovery should run exactly when answers are not scripted", skip, sawToken)
		}
		key := filepath.Join(root, "/home/flocc/.config/flocc/linear.key")
		if b, err := os.ReadFile(key); err != nil || string(b) != "lin_api_s3cretA\n" {
			t.Fatalf("%q %v", b, err)
		}
		if st, _ := os.Stat(key); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}
}

const loginFailLine = "FAIL  claude login: none; on the desktop, run just login <ssh target of this host>\n"

func finishEnv(t *testing.T, doctorOut string) *steps.Env {
	t.Helper()
	f := host.NewFake().
		On("/home/flocc/.local/bin/flocc doctor", 1, doctorOut).
		On("/home/flocc/.local/bin/flocc --version", 0, "flocc 0.4.0\n").
		On("/home/flocc/.local/bin/claude auth status", 1, `{"loggedIn":false}`).
		On("/home/flocc/.local/bin/flocc status --json", 0, `{"tick_lock":"/x/ledger.lock","claims":[]}`)
	return &steps.Env{Run: f, UI: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}, Root: t.TempDir(), Home: "/home/flocc",
		Mode: steps.Systemd, Answers: &answers.Answers{}, Sleep: func(time.Duration) {}}
}

// The desktop exits with the host's status, so these three decide what just ship reports.
func TestFinishExitCodesAndSummaryLine(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		doctor  string
		code    int
		summary bool
	}{
		{"only the Claude login is pending", nil, loginFailLine, 0, true},
		{"doctor reports another FAIL", &steps.DoctorFailed{Fails: []string{"FAIL  linear: 401"}}, loginFailLine + "FAIL  linear: 401\n", 1, true},
		{"the owner declined to go on", steps.ErrStopped, "", 0, false},
		{"a step failed", errors.New("Claude: boom"), "", 1, false},
	} {
		var out, errOut bytes.Buffer
		got := finish(context.Background(), finishEnv(t, c.doctor), c.err, true, &out, &errOut, "/tmp/setup.log")
		var line string
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(l, steps.SummaryMarker) {
				line = l
			}
		}
		if got != c.code || (line != "") != c.summary {
			t.Errorf("%s: exit %d, summary line %q\nstdout %q\nstderr %q", c.name, got, line, out.String(), errOut.String())
			continue
		}
		if line != "" {
			if s, ok := steps.ParseSummaryLine(line); !ok || !s.LoginPending || s.Failing() != (c.code == 1) {
				t.Errorf("%s: %+v %v", c.name, s, ok)
			}
		}
	}
}

func TestRunRefusesAnAnswersFile(t *testing.T) {
	if code := run([]string{"--answers", "a.toml"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/`
Expected: build failure, `undefined: readAnswers` and wrong argument counts for `handoff`, `handoffArgs`, `stageNames`.

- [ ] **Step 3: Implement**

Replace in `installer/main.go` (keep `loopWarning`, `rootStage`, `stageFile`, `checkStage`, `copyScript` as Part 1 left them; delete `dropAnswersCopy`). Add `"bytes"` and `"io"` to the imports.

```go
func checkPhase(phase string, euid int) error {
	switch {
	case phase == "root" && euid != 0:
		return errors.New("the root phase runs as root; just ship runs it with sudo")
	case (phase == "service" || phase == "login") && euid == 0:
		return errors.New("the " + phase + " phase runs as " + steps.ServiceUser + ", not root")
	case phase != "root" && phase != "service" && phase != "login":
		return fmt.Errorf("unknown phase %q", phase)
	}
	return nil
}

func handoffArgs(fromStdin bool) []string {
	args := []string{"--phase", "service"}
	if fromStdin {
		args = append(args, "--answers", "-")
	}
	return args
}

// stageNames lists what the service user copies. Root names every file itself because the
// script must hold no shell expansion: sudo -i hands it to the login shell, which expands
// `$` before the inner sh runs.
func stageNames(stage string) ([]string, error) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range entries {
		n := ent.Name()
		known := n == "flocc-setup" || n == "flocc.bundle" || n == "plugins.json" || n == "identity.json" || strings.HasSuffix(n, ".tar")
		if ent.Type().IsRegular() && known && stageFile.MatchString(n) {
			names = append(names, n)
		}
	}
	if !slices.Contains(names, "flocc-setup") {
		return nil, fmt.Errorf("%s has no flocc-setup binary", stage)
	}
	return names, nil
}

// handoff lets the service user read the stage and copy it itself. Root never writes inside
// the service home: an agent could plant a symlink there and have root write through it. doc
// is the answers document as root read it, piped on; nil leaves the service phase the terminal.
func handoff(ctx context.Context, r host.Runner, stage string, args []string, doc io.Reader) error {
	names, err := stageNames(stage)
	if err != nil {
		return err
	}
	for _, c := range []host.Cmd{
		{Name: "chown", Args: []string{"-R", "root:" + steps.ServiceUser, stage}},
		{Name: "chmod", Args: []string{"-R", "g+rX,o-rwx", stage}},
		{Name: "sudo", Args: []string{"-iu", steps.ServiceUser, "sh", "-c", copyScript(rootStage, names)}},
	} {
		if _, err := host.Must(ctx, r, c); err != nil {
			return err
		}
	}
	return r.Interactive(ctx, host.Cmd{Name: "sudo", Args: append([]string{"-iu", steps.ServiceUser, serviceStage + "/flocc-setup"}, args...), Stdin: doc})
}

// readAnswers takes the whole document before anything runs: commands started later must find
// stdin at EOF, and the service phase needs the same bytes.
func readAnswers(r io.Reader) ([]byte, *answers.Answers, error) {
	doc, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	a, err := answers.Parse(doc)
	return doc, a, err
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "host":
			return ship.HostMain(args[1:])
		case "fleet":
			return ship.FleetMain(args[1:], os.Stdout)
		case "ship":
			return ship.Main(args[1:])
		case "login":
			return ship.LoginMain(args[1:])
		}
	}
	fs := flag.NewFlagSet("flocc-setup", flag.ContinueOnError)
	phase := fs.String("phase", "root", "root, service or login; the root phase hands off to the service phase itself")
	answersIn := fs.String("answers", "", `"-" reads the answers document, secrets included, from stdin and runs without a terminal`)
	email := fs.String("email", "", "login phase: the claude.ai account to sign in as")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *answersIn != "" && *answersIn != "-" {
		fmt.Fprintln(os.Stderr, "flocc-setup: answers travel on stdin; pass --answers - and pipe the document in")
		return 2
	}
	fromStdin := *answersIn == "-"
	var doc []byte
	in := &answers.Answers{}
	if fromStdin {
		var err error
		if doc, in, err = readAnswers(os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "flocc-setup:", err)
			return 1
		}
	}
	secrets := in.Secrets.Values()
	stdout, stderr := host.Redact(os.Stdout, secrets...), host.Redact(os.Stderr, secrets...)
	if err := checkPhase(*phase, os.Geteuid()); err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 1
	}
	stage := filepath.Dir(exe)
	if *phase == "root" {
		if err := checkStage(stage); err != nil {
			fmt.Fprintln(stderr, "flocc-setup:", err)
			return 2
		}
	}
	logPath := filepath.Join(stage, "setup.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 1
	}
	defer logf.Close()
	runner := host.Exec{Log: host.Redact(logf, secrets...)}

	pid1, _ := os.ReadFile("/proc/1/comm")
	osRelease, _ := os.ReadFile("/etc/os-release")
	distro, err := host.ParseOSRelease(string(osRelease))
	if err != nil {
		fmt.Fprintln(stderr, "flocc-setup:", err)
		return 1
	}
	a := &answers.Answers{}
	env := &steps.Env{Run: runner, Stage: stage, Mode: steps.DetectMode(string(pid1)), Distro: distro,
		Answers: a, SkipInteractive: fromStdin, UID: os.Getuid(), Sleep: time.Sleep}
	if *phase != "root" {
		env.Home, _ = os.UserHomeDir()
	}
	if *phase == "service" {
		env.AnswersPath = savedAnswersPath(env.Home)
		if saved, err := answers.Load(env.AnswersPath); err == nil {
			if err := a.OverlaySaved(saved); err != nil {
				fmt.Fprintln(stderr, "flocc-setup: the saved ntfy topic in", env.AnswersPath, "is invalid and will be asked again:", err)
			}
		}
	}
	if err := a.Overlay(in); err != nil {
		fmt.Fprintln(stderr, "flocc-setup: answers:", err)
		return 1
	}
	var u ui.UI = ui.NewHuh()
	if fromStdin {
		u = &ui.Scripted{A: a, Out: stdout}
	}
	env.UI = u
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *phase == "login" {
		err := steps.Login(ctx, env, steps.LoginOptions{Email: *email})
		if errors.Is(err, steps.ErrStopped) {
			fmt.Fprintln(stdout, "Stopped. Run just login again to finish.")
			return 1
		}
		return finish(ctx, env, err, true, stdout, stderr, logPath)
	}
	list := steps.ServiceSteps()
	if *phase == "root" {
		if env.Mode == steps.Loop {
			if ok, err := u.Confirm(loopWarning, false); err != nil || !ok {
				return 0
			}
		}
		list = steps.RootSteps()
	}
	err = steps.RunAll(ctx, env, list)
	if err == nil && *phase == "root" {
		var piped io.Reader
		if fromStdin {
			piped = bytes.NewReader(doc)
		}
		err = handoff(ctx, runner, stage, handoffArgs(fromStdin), piped)
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode() // the service phase already reported its error
		}
	}
	return finish(ctx, env, err, *phase == "service", stdout, stderr, logPath)
}

// finish ends the service or login phase's output with the summary line when it got as far as
// doctor, and prints the error otherwise. The desktop takes that line out of the stream and
// prints the summary itself, so run by hand on the host the output ends in raw JSON.
func finish(ctx context.Context, env *steps.Env, err error, summary bool, stdout, stderr io.Writer, logPath string) int {
	var failed *steps.DoctorFailed
	switch {
	case errors.Is(err, steps.ErrStopped):
		fmt.Fprintln(stdout, "Stopped. Run just ship again to continue.")
		return 0
	case err != nil && !(summary && errors.As(err, &failed)):
		fmt.Fprintf(stderr, "\n%s\n\nFull log: %s\n", err, logPath)
		return 1
	case !summary:
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "\n%s\n", err)
	}
	s := steps.Summarize(ctx, env)
	// A blank line first: the marker must start a line, and a prompt may have left a partial one.
	fmt.Fprintf(stdout, "\n%s\n", s.Line())
	if s.Failing() {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run every Go test and the linters**

Run: `go build ./... && go test -race ./... && go vet ./... && test -z "$(gofmt -l deploy installer)"`
Expected: `ok` for every package, no other output. This is the first run over the whole tree: Tasks 1 to 6 checked only their own packages.

- [ ] **Step 5: Commit**

```bash
git add installer/main.go installer/main_test.go
git commit -s -m "Read answers from stdin in the installer, pipe them to the service phase, add the login phase and the summary"
```

---

### Task 8: Python: the tick lock in status, login wording, and the lapse page

**Files:**
- Modify: `src/flocc/config.py` (`Config.tick_lock`)
- Modify: `src/flocc/cli.py` (drop `_lock_path`)
- Modify: `src/flocc/status.py` (`tick_lock` in the report)
- Modify: `src/flocc/tokens.py` (`LOGIN_REMEDY`, `claude_login`, `check_claude_login`)
- Modify: `src/flocc/doctor.py` (`claude_checks`)
- Modify: `src/flocc/tick.py` (call `check_claude_login`)
- Test: `tests/test_status.py`, `tests/test_tokens.py`, `tests/test_doctor.py`, `tests/conftest.py`

**Interfaces:**
- Consumes: `ledger.meta_get`, `ledger.meta_set`, `Escalation`, `rt.notifier`, `rt.claude_exe`, `rt.con`.
- Produces: `Config.tick_lock -> Path` (`ledger_path.with_suffix(".lock")`); `"tick_lock": str` in `flocc status --json` (read by Task 5); `tokens.claude_login(exe, run=subprocess.run) -> str | None` (`"full"`, `"token-only (<method>)"`, `"none"`, or None when it could not ask); `tokens.check_claude_login(rt, run=subprocess.run) -> None`; `tokens.LOGIN_REMEDY`. Doctor's `claude login` FAIL line keeps the prefix `FAIL  claude login:` that Task 3 filters on.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_status.py`:

```python
def test_status_names_the_tick_lock(cfg):
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert report["tick_lock"] == str(cfg.ledger_path.with_suffix(".lock"))
    assert cfg.tick_lock == cfg.ledger_path.with_suffix(".lock")
```

In `tests/test_tokens.py`, add `import subprocess` and extend the import to `from flocc.tokens import TokenRejected, check_claude_login, check_tokens, claude_login, token_expiry`, then append:

```python
def auth_run(stdout, code=0):
    def run(argv, **kw):
        assert argv[1:] == ["auth", "status"]
        return subprocess.CompletedProcess(argv, code, stdout, "")

    return run


FULL = '{"loggedIn": true, "authMethod": "claude.ai"}'
LOGGED_OUT = '{"loggedIn": false}'


@pytest.mark.parametrize(
    ("stdout", "want"),
    [
        (FULL, "full"),
        ('{"loggedIn": true, "authMethod": "oauth_token"}', "token-only (oauth_token)"),
        (LOGGED_OUT, "none"),
        ("Error: unexpected", None),
    ],
)
def test_claude_login_kinds(stdout, want):
    assert claude_login("/opt/claude", auth_run(stdout)) == want


def test_claude_login_that_cannot_run_is_unknown():
    def hangs(argv, **kw):
        raise subprocess.TimeoutExpired(argv, 30)

    assert claude_login("/opt/claude", hangs) is None


def test_lapsed_login_pages_once_and_rearms(rt):
    rt.claude_exe = "/opt/claude"
    out = auth_run(LOGGED_OUT, 1)
    check_claude_login(rt, out)
    check_claude_login(rt, out)
    [esc] = rt.notifier.sent
    assert (esc.tier, esc.scope, esc.cause) == (1, "global", "claude-login")
    assert "just login" in esc.action
    check_claude_login(rt, auth_run(FULL))
    check_claude_login(rt, out)
    assert len(rt.notifier.sent) == 2


def test_unanswered_login_probe_does_not_page(rt):
    rt.claude_exe = "/opt/claude"
    check_claude_login(rt, auth_run("not json"))
    assert rt.notifier.sent == []
```

Append to `tests/test_doctor.py`:

```python
def test_claude_login_fail_names_the_kind_and_the_desktop_command(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    token = json.dumps({"loggedIn": True, "authMethod": "oauth_token"})
    run = fake_run({"--version": ("2.1.295 (Claude Code)\n", 0), "auth status": (token, 0)})
    login = doctor.claude_checks("/opt/claude", run)[1]
    assert login.status == "fail"
    assert login.detail.startswith("token-only (oauth_token); on the desktop, run just login")
```

In `tests/conftest.py`, extend the autouse fixture:

```python
@pytest.fixture(autouse=True)
def no_token_check(monkeypatch):
    # The tick's token and login checks call api.github.com and claude; test_tokens.py covers
    # them with fakes.
    monkeypatch.setattr("flocc.tick.check_tokens", lambda rt: None)
    monkeypatch.setattr("flocc.tick.check_claude_login", lambda rt: None)
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_status.py tests/test_tokens.py tests/test_doctor.py -q`
Expected: every test in these files errors, not only the three new ones: the autouse fixture in `conftest.py` patches `flocc.tick.check_claude_login` before that function exists (`AttributeError: <module 'flocc.tick'> does not have the attribute 'check_claude_login'`). `tests/test_tokens.py` fails earlier, at collection, with `ImportError: cannot import name 'check_claude_login'`. `KeyError: 'tick_lock'` shows only once the fixture error is gone.

- [ ] **Step 3: Implement**

`src/flocc/config.py`, inside `Config` after `effort_for`:

```python
    @property
    def tick_lock(self) -> Path:
        return self.ledger_path.with_suffix(".lock")
```

`src/flocc/cli.py`: delete `_lock_path` and use `tick_lock(cfg.tick_lock)` in `cmd_tick` and `cmd_release`.

`src/flocc/status.py`, in `collect`'s returned dict after `"ledger_error"`:

```python
        # The desktop's login takes this lock with flock while it runs.
        "tick_lock": str(cfg.tick_lock),
```

`src/flocc/tokens.py`: add `import json`, `import socket` and `import subprocess`, then:

```python
LOGIN_REMEDY = "on the desktop, run just login <ssh target of this host>"


def claude_login(exe: str, run=subprocess.run) -> str | None:
    """full, token-only (<method>) or none; None when claude auth status gave no answer."""
    try:
        out = run([exe, "auth", "status"], capture_output=True, text=True, check=False, timeout=30)
        auth = json.loads(out.stdout)
    except (OSError, subprocess.SubprocessError, ValueError):
        return None
    if not isinstance(auth, dict) or not auth.get("loggedIn"):
        return "none"
    method = auth.get("authMethod") or "unknown"
    # Only a claude.ai login opens Remote Control sessions; a setup-token login cannot.
    return "full" if method == "claude.ai" else f"token-only ({method})"


def check_claude_login(rt, run=subprocess.run) -> None:
    # Every new session fails without the login and running Remote Control sessions cannot
    # recover, so the first tick that sees it lapse pages once; the flag rearms when it is back.
    login = claude_login(rt.claude_exe, run)
    if login is None:
        return
    lapsed = ledger.meta_get(rt.con, "claude_login_lapsed") == "1"
    if login == "full":
        if lapsed:
            ledger.meta_set(rt.con, "claude_login_lapsed", "0")
        return
    if lapsed:
        return
    summary = f"The Claude login on {socket.gethostname()} lapsed ({login}); new sessions fail."
    action = "On the desktop, run just login <ssh target of this host>"
    rt.notifier.send(Escalation(1, "claude-login", summary, action, scope="global"), None, None)
    ledger.meta_set(rt.con, "claude_login_lapsed", "1")
```

`src/flocc/doctor.py`: import `LOGIN_REMEDY` and `claude_login` from `flocc.tokens`, drop `json` from the imports if nothing else uses it, and replace the `status = run([exe, "auth", "status"] …` block up to the `ANTHROPIC_API_KEY` check with:

```python
    login = claude_login(exe, run)
    if login == "full":
        results.append(Result("ok", "claude login", "claude.ai subscription"))
    else:
        detail = login or "claude auth status gave no answer"
        results.append(Result("fail", "claude login", f"{detail}; {LOGIN_REMEDY}"))
```

`src/flocc/tick.py`: import `check_claude_login` beside `check_tokens` and call it right after `check_tokens(rt)`:

```python
        check_tokens(rt)
        check_claude_login(rt)
```

- [ ] **Step 4: Run the suite and the linters**

Run: `uv run pytest -q && uv run ruff check . && uv run ruff format --check . && uv run pyrefly check`
Expected: all tests pass; no lint or type findings.

- [ ] **Step 5: Commit**

```bash
git add src/flocc tests
git commit -s -m "Name the tick lock in status, point the login FAIL at just login, and page once when the login lapses"
```

---

### Task 9: Recipe, docs and version 0.4.0

**Files:**
- Modify: `justfile` (`ship` comment, new `login` recipe)
- Modify: `docs/vm-setup.md`
- Modify: `CHANGELOG.md`, `pyproject.toml`, `plugin/.claude-plugin/plugin.json`, `uv.lock`

**Interfaces:**
- Consumes: `flocc-setup ship [--interactive | --answers FILE|-] <target>`, `flocc-setup login [--email ADDR] <target>` (Tasks 6, 7).
- Produces: `just login <target> [--email ADDR]`; version 0.4.0.

- [ ] **Step 1: Update the justfile**

Replace the `ship` recipe's comment and add `login` after it:

```just
# Set up or upgrade a host without a terminal (--answers FILE sends the answers), or --interactive
[group('host')]
ship target *flags: build-setup
    build/flocc-setup ship {{flags}} {{target}}

# Log the host in to Claude, accept Remote Control and start its timer (--email picks the account)
[group('host')]
login target *flags: build-desktop
    build/flocc-setup login {{flags}} {{target}}
```

Run: `just --list --unsorted | grep -E '^ +(ship|login)'`
Expected: both recipes listed with their comments.

- [ ] **Step 2: Update docs/vm-setup.md**

Replace the paragraph that begins "The first run on a host asks you" and the paragraph that begins "The root phase must run from" with:

````markdown
`just ship <target> --answers <file>` runs without a terminal (`--answers -` reads the document
from this terminal's stdin). The answers document, secrets included, goes to the host on stdin in
one SSH session forced to have no TTY (`ssh -T`, so a `RequestTTY` setting cannot echo it back),
and nothing prompts. The SSH user must be root or have passwordless sudo: ship checks
`sudo -n true` before it stages anything and stops otherwise. No secret is written on the host
except the service's own secret files (mode 600, in `~flocc/.config/flocc/`), and none appears in a
command line, a `setup.log` or the output; the installer also replaces the secret values of the
answers document with `<token>` in its log (secrets typed into `--interactive` forms are not in
that set; no step logs them). A re-ship of a host that is set up needs no `--answers`: the host
keeps its answers, without secrets, in `~flocc/.config/flocc/answers.toml`, and its secret files.

```toml
team_key = "NF"
digest_issue = "NF-186"
ntfy_topic = "flocc-a-long-random-topic"
shared_team = false

[[repos]]
name = "nimblefox"
url = "https://github.com/NovusEdge/nimblefox"
base_branch = "main"
check = "just check"
draft = true

[secrets]
linear_key = "lin_api_…"
agent_token = "github_pat_…"
publish_token = "github_pat_…"
```

`just ship <target> --interactive` runs over `ssh -t` and asks on the host's terminal instead: the
Tailscale login, the three secrets, the config form and the ntfy test push. It takes no
`--answers`. Everything either mode runs is logged to `/tmp/flocc-setup/setup.log` (root steps)
and `~flocc/.cache/flocc-setup/setup.log` (the rest). The root phase must run from
`/tmp/flocc-setup`, where `just ship` puts it; running `flocc-setup` by hand from anywhere else is
refused.

Ship never logs in to Claude. It ends with a summary: the version on the host, the Claude login
(full, token-only or none), the GitHub token expiries, each failed doctor check, the timer and the
live sessions. It exits non-zero when a doctor check other than the Claude login fails. On a fresh
host the login is pending, so the tmux unit runs, the timer stays off, ship exits 0 and the summary
ends with the command to run next.

## Claude login

```bash
just login <target> [--email you@example.com]
```

Sessions need a full claude.ai login: a `claude setup-token` token cannot open Remote Control
sessions. `just login` runs on the host as `flocc`, over `ssh -t`:

1. It takes the tick lock for the whole login. If a tick is running it waits for it; if sessions
   are running it says so and asks whether to wait for them or stop, because running sessions
   rewrite `~/.claude.json` from memory and could drop the Remote Control consent. While it waits
   for sessions it lets the lock go, so ticks keep supervising them.
2. Without a claude.ai login it runs `claude auth login --claudeai`. The sign-in URL opens in your
   desktop browser and prints as a QR code. Sign in and paste the code at the prompt in the same
   terminal. If `claude auth status` still does not show a claude.ai login, it stops there.
3. It marks `~/worktrees` trusted (sessions trust only their own worktree, so `remote-control`
   would stop at the folder-trust dialog), then runs `claude remote-control` once under a pty
   (`script`), answers its `Enable Remote Control? (y/n)` prompt with `y`, waits for its connected
   line and stops it. If no connected line appears within 60 seconds it fails and shows the output;
   run `claude remote-control` as `flocc` by hand to see what it waits for.
4. It checks `claude auth status`, runs doctor and starts the timer, then prints the summary.

The host must have been shipped with 0.4.0 or later. When the login lapses, the next tick sends one
tier 1 push; it pages again only after a login has been seen in between. Shipping to a host whose
login has lapsed leaves its already-enabled timer on, so the tick keeps paging until you run
`just login`.
````

Also replace any remaining "`/login`" instruction for the Claude login elsewhere in the file with `just login <target>`:

Run: `grep -n "/login\|skip-interactive" docs/vm-setup.md`
Expected: no output.

- [ ] **Step 3: Bump the version and add the changelog entry**

Set `version = "0.4.0"` in `pyproject.toml` and `"version": "0.4.0"` in `plugin/.claude-plugin/plugin.json`, then run `uv lock`. Add above `## 0.3.0` in `CHANGELOG.md`, with the commit's date from `date +%F` in the heading:

```markdown
## 0.4.0 (2026-10-21)

The host side of setup: ship never asks, and the Claude login is one command.

- `just ship <target> --answers FILE` runs its setup in one SSH session without a terminal. The answers
  document, secrets included, goes on stdin; no secret lands in a file other than the service's own
  secret files, in a command line, in `setup.log` or in the output. Ship checks for passwordless
  sudo first. `--interactive` keeps the terminal forms; `--skip-interactive` is gone.
- Ship no longer logs in to Claude. `just login <target>` does a full claude.ai login with
  `claude auth login --claudeai`, answers the Remote Control consent and starts the timer, holding
  the tick lock throughout.
- Ship ends with a summary (version, Claude login, GitHub token expiries, failed doctor checks,
  timer, live sessions) and exits non-zero only when a check other than a pending login fails.
- The tick pages once, tier 1, when the Claude login lapses. `flocc status --json` names the tick
  lock.
```

- [ ] **Step 4: Run the full check**

Run: `just check`
Expected: lint passes, `uv run pytest -q` passes (including `test_the_plugin_and_the_package_share_one_version`), `go test ./...` passes.

- [ ] **Step 5: Commit**

```bash
git add justfile docs/vm-setup.md CHANGELOG.md pyproject.toml plugin/.claude-plugin/plugin.json uv.lock
git commit -s -m "Version 0.4.0: just login, document shipping without a terminal"
```

---

### Task 10: Host verification (controller only, manual)

Not for an implementer subagent: these steps use the owner's accounts and real hosts. Run them from the merged branch on the desktop; record each result in the PR description.

**Files:** none, unless a step fails and needs a fix (then a normal TDD fix with the real output as a test fixture).

**Interfaces:**
- Consumes: everything above; `flocc-nimble` set up by Part 1; a Daytona login.

The throwaway Daytona host goes first (Steps 1 to 3), so the stdin protocol, the URL watcher and the consent handling are proven on a sandbox before they touch `flocc-nimble`, which has live sessions and the production `~/.claude.json` (Steps 4 to 7).

- [ ] **Step 1: A fresh host on Daytona, shipped without a terminal**

Keep the secrets out of the repository: the answers file lives in a scratch directory, mode 600.

```bash
scratch=$(mktemp -d) && chmod 700 "$scratch"
```

Copy flocc-nimble's answers into `$scratch/smoke.toml` (mode 600) with its `[secrets]`, a throwaway `ntfy_topic`, `shared_team = true`, a `digest_issue` that names a throwaway Linear issue (the sandbox's first tick posts its digest there, and flocc-nimble's `digest_issue` would post to the real digest issue), and one `[[repos]]` whose `name` has no Linear label or project (for example `name = "smoke-docs"` with the URL of a repo the agent token can read), so the sandbox never claims an issue. Then:

```bash
just daytona-up flocc-smoke
just ship daytona:flocc-smoke --answers "$scratch/smoke.toml" >"$scratch/ship.out" 2>&1; echo "exit $?"
cat "$scratch/ship.out"
grep -cE 'lin_api_|github_pat_' "$scratch/ship.out"
just host daytona:flocc-smoke grep -cE 'lin_api_|github_pat_' /tmp/flocc-setup/setup.log
just host daytona:flocc-smoke grep -cE 'lin_api_|github_pat_' .cache/flocc-setup/setup.log
```

Pass: no prompt; the summary in `ship.out` shows `claude login   none; on the desktop run: just login daytona:flocc-smoke` and `timer          off until the Claude login`; `exit 0`; the output holds no `flocc-summary` text; all three counts are `0`. Also look in `ship.out` and the service log for the plugin steps: `claude plugin marketplace add` and `claude plugin install` must have succeeded although no Claude login exists yet (the Claude step now runs before the login). If they fail, the plugin install has to move after the login; report it to the owner. Record the result in the PR description and the spec's Unconfirmed list.

- [ ] **Step 2: Log the fresh host in, and calibrate the consent matchers**

Keep the raw terminal output for fixtures (util-linux `script`):

```bash
script -q -c 'just login daytona:flocc-smoke' "$scratch/login.typescript"
```

Pass: the sign-in page opens in the desktop browser and a QR code prints; after signing in and pasting the code, `~/worktrees` is trusted, the consent prompt is answered, the connected line is seen, and the summary shows `claude login   full` and the timer on.

Then read `$scratch/login.typescript` and check what the matchers rely on:

- The `Enable Remote Control? (y/n)` text is what `consentPrompt` matches, and the line the run stopped on is a success line, not something like "not connected". If the login failed with "printed no connected line" and shows the output, the matchers or the pty handling are wrong: put the real output into `TestAnswerConsent…` as a fixture and fix `consentPrompt` or `connectedLine` in `installer/internal/steps/login.go`.
- If the browser did not open, copy the URL line the host printed into `TestLoginRunsTheLoginPhaseOnATerminalOpensTheURLAndPrintsTheSummary` as the fixture and fix `oauthURL` in `installer/internal/ship/login.go`.
- If the QR code is skewed (stair-stepped lines), `qr.Write` lost its `\r\n` endings.

- [ ] **Step 3: Clean up the sandbox**

```bash
just host daytona:flocc-smoke claude auth logout
daytona delete flocc-smoke
rm -rf "$scratch"
```

- [ ] **Step 4: Upgrade flocc-nimble without a terminal**

```bash
scratch=$(mktemp -d) && chmod 700 "$scratch"
just ship root@flocc-nimble >"$scratch/nimble.out" 2>&1; echo "exit $?"
cat "$scratch/nimble.out"
grep -cE 'lin_api_|github_pat_' "$scratch/nimble.out"
```

Pass: no prompt; the summary shows `claude login   full`, both token expiries, `timer          on`, the live sessions, and `exit 0`; the grep count is `0`.

- [ ] **Step 5: Check the logs hold no secret**

Run: `ssh root@flocc-nimble 'grep -cE "lin_api_|github_pat_" /tmp/flocc-setup/setup.log /home/flocc/.cache/flocc-setup/setup.log'`
Pass: both counts are `0`.

- [ ] **Step 6: Log flocc-nimble in again**

Run: `just login root@flocc-nimble`
Pass: if sessions run, it names them and asks whether to wait; once none run, it skips `claude auth login` (already full), trusts `~/worktrees`, answers the consent prompt if one appears, waits for the connected line, and the summary shows the timer on.

- [ ] **Step 7: Confirm the consent is the one sessions read**

After Step 6, wait for the next implementer or fix session on flocc-nimble. Pass: it appears in the Claude app as a Remote Control session. Move the spec's Unconfirmed line "`claude remote-control`'s y/n consent is the same one-time confirmation `--remote-control` sessions read" out of "Unconfirmed", with the date.
