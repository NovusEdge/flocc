# Setup Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `just ship <target>` takes a fresh Linux host (systemd VM or Daytona container) to a running linear-agents service, resumably.

**Architecture:** One Go program, `la-setup`. On the desktop, `la-setup ship` stages a binary, git bundles and a plugin list and runs the host side over SSH. On the host, a plain `main` runs check/apply steps in order, four as root, then re-executes itself as `linear-agents` for the rest. Forms are standalone `huh` v2 forms; logins run as child processes on the terminal. Hosts without systemd run in loop mode, served by a new `linear-agents run` Python command.

**Tech Stack:** Go 1.27.1 (mise), `charm.land/huh/v2`, `charm.land/lipgloss/v2`, `github.com/pelletier/go-toml/v2`; Python 3 / pytest for the `run` command and the config golden test.

**Spec:** `docs/superpowers/specs/2026-10-10-setup-tui-design.md`

## Global Constraints

- Module `github.com/NovusEdge/linear-agents`, `go.mod` at the repo root; installer code under `installer/`.
- Minimum Claude Code version: 2.1.293.
- Service user: `linear-agents`, home `/home/linear-agents` mode 700, in no `sudo`, `wheel` or `admin` group.
- Host stage directory: `/tmp/la-setup/` (root phase); service copy: `~/.cache/la-setup/`.
- Logs: `/tmp/la-setup/setup.log` (root phase), `~/.cache/la-setup/setup.log` (service phase); error output shows the last 20 lines.
- Saved answers: `~/.cache/la-setup/answers.toml`, mode 600, never containing secrets.
- Secret files: `~/.config/linear-agents/{linear.key,gh-agent.token,gh-publish.token}`, mode 600, one line.
- tmux socket `linear-agents`; loop-mode session `la-loop`; loop interval 180 s; loop log `<state_root>/run.log`.
- Unit files and `config.example.toml` come only from the embedded `deploy` package.
- Commits: `git commit -s`, no `Co-Authored-By` trailer, no AI attribution.
- Comments carry only facts the code cannot show; no narration, no banners.

## Review Focus

1. Re-running after an aborted Claude login (claude installed, not logged in) runs only the login and plugin parts, not the reinstall. Pinned in Task 8.
2. A host checkout with local edits (the old VM had uncommitted eval edits) stops the repo step with git's own message instead of discarding the edits. Pinned in Task 7.
3. A pasted secret with a trailing newline, CRLF, inner space or a `NAME=` prefix is cleaned or refused before it is written. Pinned in Task 7.
4. An existing `linear-agents` user that is in `sudo` or `wheel` stops the run instead of being used. Pinned in Task 5.
5. The root phase run without root, or the service phase run as root, is refused before anything changes. Pinned in Task 11.

---

## File Structure

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | module root |
| `deploy/embed.go` | `deploy.Files`: the three units and `config.example.toml` |
| `installer/main.go` | flags, phase guard, wiring, root→service handoff |
| `installer/internal/host/run.go` | `Cmd`, `Result`, `Runner`, `Exec`, `Must`, `CmdError`, `Tail` |
| `installer/internal/host/fake.go` | `Fake` runner for tests |
| `installer/internal/host/distro.go` | `ParseOSRelease`, `Distro`, `Manager` |
| `installer/internal/host/version.go` | `AtLeast` |
| `installer/internal/answers/answers.go` | `Answers`, `Repo`, `Secrets`, `Load`, `Save`, `Overlay` |
| `installer/internal/plugins/plugins.go` | `List`, `Marketplace`, `Load`, `Write` |
| `installer/internal/ui/ui.go` | `UI` interface, `StepStatus`, `Scripted` |
| `installer/internal/ui/huh.go` | `Huh` implementation |
| `installer/internal/steps/step.go` | `Step`, `Env`, `Mode`, `DetectMode`, `RunAll`, `ErrStopped` |
| `installer/internal/steps/packages.go` | Packages step |
| `installer/internal/steps/tailscale.go` | Tailscale step |
| `installer/internal/steps/user.go` | Service user step, `ServiceUser` |
| `installer/internal/steps/bundle.go` | `bundleHead`, `syncFromBundle` |
| `installer/internal/steps/repo.go` | Service repo step |
| `installer/internal/steps/secrets.go` | Secrets step |
| `installer/internal/steps/claude.go` | Claude step |
| `installer/internal/steps/config.go` | Config step, `RenderConfig`, `doctor` |
| `installer/internal/steps/run.go` | Run step |
| `installer/internal/steps/list.go` | `RootSteps`, `ServiceSteps` |
| `installer/internal/ship/ship.go` | `Main`, staging, scp/ssh |
| `installer/internal/ship/daytona.go` | Daytona config, SSH token |
| `installer/internal/ship/plugins.go` | `planPlugins` |
| `src/linear_agents/tick.py` | `run_loop` |
| `src/linear_agents/cli.py` | `run` subcommand |
| `tests/test_run_loop.py`, `tests/test_installer_config.py` | Python tests |
| `mise.toml`, `justfile`, `.gitignore` | toolchain and recipes |
| `docs/vm-setup.md` | rewritten setup section |

All Go tests run with `go test ./...` from the repo root.

---

### Task 1: Go module, embedded deploy files, toolchain and recipes

**Files:**
- Create: `go.mod`, `deploy/embed.go`, `deploy/embed_test.go`, `installer/main.go` (stub)
- Modify: `mise.toml`, `justfile`, `.gitignore`

**Interfaces:**
- Produces: `deploy.Files embed.FS` holding `linear-agents.service`, `linear-agents.timer`, `linear-agents-tmux.service`, `config.example.toml`; `deploy.Units = []string{...}` (the three unit names).

- [ ] **Step 1: Pin Go and add recipes**

`mise.toml` gains under `[tools]`:

```toml
go = "1.27.1"
```

Run: `mise install` — Expected: go 1.27.1 installed.

`justfile` becomes:

```just
fmt:
    uv run ruff check --select I --fix
    uv run ruff format
    gofmt -w deploy installer

lint:
    uv run ruff format --check
    uv run ruff check
    uv run pyrefly check
    shellcheck setup
    test -z "$(gofmt -l deploy installer)"
    go vet ./...

test:
    uv run pytest -q
    go test ./...

check: lint test

build-setup:
    mkdir -p build
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/la-setup-linux-amd64 ./installer
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/la-setup-linux-arm64 ./installer
    go build -o build/la-setup ./installer

# target: an ssh destination (a Tailscale name) or daytona:<sandbox>
ship target *flags: build-setup
    build/la-setup ship {{flags}} {{target}}
```

`.gitignore` gains `build/`.

- [ ] **Step 2: Write the failing embed test**

`go mod init github.com/NovusEdge/linear-agents`, then edit `go.mod` so the `go` line reads `go 1.27.1`.

`deploy/embed_test.go`:

```go
package deploy

import "testing"

func TestFilesHoldUnitsAndExample(t *testing.T) {
	for _, name := range append(Units, "config.example.toml") {
		b, err := Files.ReadFile(name)
		if err != nil || len(b) == 0 {
			t.Fatalf("%s: %v (len %d)", name, err, len(b))
		}
	}
}
```

Run: `go test ./deploy/` — Expected: FAIL, `undefined: Units`.

- [ ] **Step 3: Implement**

`deploy/embed.go`:

```go
// Package deploy embeds the unit files and config template so the installer ships the
// copies in this commit and no others.
package deploy

import "embed"

//go:embed linear-agents.service linear-agents.timer linear-agents-tmux.service config.example.toml
var Files embed.FS

var Units = []string{"linear-agents.service", "linear-agents.timer", "linear-agents-tmux.service"}
```

`installer/main.go` (stub, replaced in Task 11):

```go
package main

func main() {}
```

- [ ] **Step 4: Verify**

Run: `go test ./deploy/ && just build-setup && ls build/`
Expected: PASS; `la-setup`, `la-setup-linux-amd64`, `la-setup-linux-arm64` listed.

- [ ] **Step 5: Commit**

```bash
git add go.mod deploy/embed.go deploy/embed_test.go installer/main.go mise.toml justfile .gitignore
git commit -s -m "Add the Go module and embed the deploy files for the installer"
```

---

### Task 2: Host runner, distro detection, version comparison

**Files:**
- Create: `installer/internal/host/run.go`, `fake.go`, `distro.go`, `version.go`, `host_test.go`

**Interfaces:**
- Produces:
  - `type Cmd struct{ Name string; Args []string; Dir string; Env []string }`, `func (c Cmd) String() string` (name and args joined by spaces).
  - `type Result struct{ Stdout, Stderr string; Code int }`, `func (r Result) Out() string` (stdout then stderr).
  - `type Runner interface{ Run(ctx, Cmd) (Result, error); Interactive(ctx, Cmd) error }`.
  - `type Exec struct{ Log io.Writer }` implementing `Runner`.
  - `func Must(ctx, Runner, Cmd) (string, error)` returning stdout; non-zero exit → `*CmdError`.
  - `type CmdError struct{ Cmd Cmd; Code int; Out string }`.
  - `func Tail(s string, n int) string`.
  - `type Fake struct{ Responses map[string]Result; Seqs map[string][]Result; Calls []string }`, `NewFake()`, `(*Fake).On(cmd string, code int, stdout string) *Fake`, `(*Fake).OnSeq(cmd string, rs ...Result) *Fake` (answers in order, the last one repeating; takes precedence over `On`), `(*Fake).Ran(cmd string) bool`. Unknown commands exit 127.
  - `type Manager string` with `Apt`, `Pacman`, `Dnf`; `type Distro struct{ ID string; Like []string; Manager Manager }`; `func ParseOSRelease(text string) (Distro, error)`; `func (d Distro) NeedsGHRepo() bool`.
  - `func AtLeast(s string, min [3]int) bool`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/host/host_test.go`:

```go
package host

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	cases := []struct {
		text    string
		manager Manager
		ghRepo  bool
	}{
		{"ID=debian\nVERSION_ID=\"13\"\n", Apt, false},
		{"ID=ubuntu\nID_LIKE=debian\n", Apt, false},
		{"ID=arch\n", Pacman, false},
		{"ID=manjaro\nID_LIKE=arch\n", Pacman, false},
		{"ID=fedora\n", Dnf, false},
		{"ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n", Dnf, true},
		{"ID=rhel\nID_LIKE=\"fedora\"\n", Dnf, true},
	}
	for _, c := range cases {
		d, err := ParseOSRelease(c.text)
		if err != nil || d.Manager != c.manager || d.NeedsGHRepo() != c.ghRepo {
			t.Errorf("%q: got %+v ghRepo=%v err=%v", c.text, d, d.NeedsGHRepo(), err)
		}
	}
	if _, err := ParseOSRelease("ID=alpine\n"); err == nil || !strings.Contains(err.Error(), "alpine") {
		t.Errorf("alpine: want an error naming it, got %v", err)
	}
}

func TestAtLeast(t *testing.T) {
	min := [3]int{2, 1, 293}
	for s, want := range map[string]bool{
		"2.1.293 (Claude Code)": true,
		"2.1.295":               true,
		"2.2.0":                 true,
		"3.0.0":                 true,
		"2.1.292":               false,
		"2.0.999":               false,
		"garbage":               false,
	} {
		if AtLeast(s, min) != want {
			t.Errorf("AtLeast(%q) != %v", s, want)
		}
	}
}

func TestMustReturnsCmdErrorWithTail(t *testing.T) {
	f := NewFake().On("git fetch", 1, "")
	f.Responses["git fetch"] = Result{Stderr: "fatal: nope\n", Code: 1}
	_, err := Must(context.Background(), f, Cmd{Name: "git", Args: []string{"fetch"}})
	var ce *CmdError
	if !errors.As(err, &ce) || ce.Code != 1 || !strings.Contains(err.Error(), "fatal: nope") {
		t.Fatalf("got %v", err)
	}
}

func TestTail(t *testing.T) {
	if got := Tail("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("got %q", got)
	}
}

func TestFakeSeqAnswersInOrderThenRepeatsTheLast(t *testing.T) {
	f := NewFake().OnSeq("x", Result{Code: 1}, Result{Stdout: "ok"})
	var got []int
	for range 3 {
		r, _ := f.Run(context.Background(), Cmd{Name: "x"})
		got = append(got, r.Code)
	}
	if got[0] != 1 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("codes %v", got)
	}
}

func TestFakeUnknownCommandExits127(t *testing.T) {
	r, err := NewFake().Run(context.Background(), Cmd{Name: "nope"})
	if err != nil || r.Code != 127 {
		t.Fatalf("got %+v %v", r, err)
	}
}
```

Run: `go test ./installer/internal/host/` — Expected: FAIL to compile (undefined symbols).

- [ ] **Step 2: Implement `run.go`**

```go
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
```

- [ ] **Step 3: Implement `fake.go`**

```go
package host

import (
	"context"
	"slices"
)

// Fake answers commands by their String() form, for tests in this module.
type Fake struct {
	Responses map[string]Result
	Seqs      map[string][]Result
	Calls     []string
}

func NewFake() *Fake { return &Fake{Responses: map[string]Result{}, Seqs: map[string][]Result{}} }

func (f *Fake) On(cmd string, code int, stdout string) *Fake {
	f.Responses[cmd] = Result{Stdout: stdout, Code: code}
	return f
}

func (f *Fake) OnSeq(cmd string, rs ...Result) *Fake {
	f.Seqs[cmd] = rs
	return f
}

func (f *Fake) Run(_ context.Context, c Cmd) (Result, error) {
	f.Calls = append(f.Calls, c.String())
	if seq := f.Seqs[c.String()]; len(seq) > 0 {
		r := seq[0]
		if len(seq) > 1 {
			f.Seqs[c.String()] = seq[1:]
		}
		return r, nil
	}
	if r, ok := f.Responses[c.String()]; ok {
		return r, nil
	}
	return Result{Stderr: "fake: no response for " + c.String(), Code: 127}, nil
}

func (f *Fake) Interactive(_ context.Context, c Cmd) error {
	f.Calls = append(f.Calls, "interactive: "+c.String())
	return nil
}

func (f *Fake) Ran(cmd string) bool { return slices.Contains(f.Calls, cmd) }
```

- [ ] **Step 4: Implement `distro.go` and `version.go`**

```go
package host

import (
	"fmt"
	"strings"
)

type Manager string

const (
	Apt    Manager = "apt"
	Pacman Manager = "pacman"
	Dnf    Manager = "dnf"
)

type Distro struct {
	ID      string
	Like    []string
	Manager Manager
}

func ParseOSRelease(text string) (Distro, error) {
	kv := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			kv[k] = strings.Trim(v, `"'`)
		}
	}
	d := Distro{ID: kv["ID"], Like: strings.Fields(kv["ID_LIKE"])}
	for _, id := range append([]string{d.ID}, d.Like...) {
		switch id {
		case "debian", "ubuntu":
			d.Manager = Apt
		case "arch":
			d.Manager = Pacman
		case "fedora", "rhel", "centos":
			d.Manager = Dnf
		}
		if d.Manager != "" {
			return d, nil
		}
	}
	return d, fmt.Errorf("unsupported distribution %q (ID_LIKE %q): needs apt, pacman or dnf", d.ID, kv["ID_LIKE"])
}

// NeedsGHRepo: RHEL and its rebuilds have no gh package; Fedora does.
func (d Distro) NeedsGHRepo() bool { return d.Manager == Dnf && d.ID != "fedora" }
```

```go
package host

import (
	"regexp"
	"strconv"
)

var semver = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// AtLeast reports whether the first x.y.z in s is at least min.
func AtLeast(s string, min [3]int) bool {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	for i := range 3 {
		n, _ := strconv.Atoi(m[i+1])
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}
```

- [ ] **Step 5: Verify and commit**

Run: `go test ./installer/internal/host/` — Expected: PASS.

```bash
git add installer/internal/host
git commit -s -m "Add the installer's command runner and distro detection"
```

---

### Task 3: Answers and plugin list files

**Files:**
- Create: `installer/internal/answers/answers.go`, `answers_test.go`, `installer/internal/plugins/plugins.go`, `plugins_test.go`

**Interfaces:**
- Produces:
  - `type Repo struct{ Name, URL, BaseBranch, Check string; Draft bool }` (toml keys `name`, `url`, `base_branch`, `check`, `draft`).
  - `type Secrets struct{ LinearKey, AgentToken, PublishToken string }` (toml `linear_key`, `agent_token`, `publish_token`).
  - `type Answers struct{ Hostname, TeamKey, DigestIssue, NtfyTopic string; Repos []Repo; Secrets *Secrets }`.
  - `func Load(path string) (*Answers, error)` (missing file → empty Answers), `func (a *Answers) Save(path string) error`, `func (a *Answers) Overlay(o *Answers)`.
  - `type Marketplace struct{ Name, GitHub, Bundle string }` (json `name`, `github,omitempty`, `bundle,omitempty`); `type List struct{ Marketplaces []Marketplace; Plugins []string }` (json `marketplaces`, `plugins`); `func Load(path string) (List, error)`; `func (l List) Write(path string) error`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/answers/answers_test.go`:

```go
package answers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveNeverWritesSecretsAndIsMode600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "answers.toml")
	a := &Answers{TeamKey: "NF", Repos: []Repo{{Name: "r", URL: "https://github.com/o/r", BaseBranch: "main", Draft: false, Check: "just check"}},
		Secrets: &Secrets{LinearKey: "lin_api_x"}}
	if err := a.Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "lin_api_x") || strings.Contains(string(b), "secrets") {
		t.Fatalf("secrets written: %s", b)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	back, err := Load(p)
	if err != nil || back.TeamKey != "NF" || back.Repos[0].Draft || back.Secrets != nil {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	a, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil || a.TeamKey != "" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestOverlayKeepsUnsetFields(t *testing.T) {
	a := &Answers{TeamKey: "NF", DigestIssue: "NF-186"}
	a.Overlay(&Answers{DigestIssue: "NF-1", Secrets: &Secrets{AgentToken: "t"}})
	if a.TeamKey != "NF" || a.DigestIssue != "NF-1" || a.Secrets.AgentToken != "t" {
		t.Fatalf("%+v", a)
	}
}
```

`installer/internal/plugins/plugins_test.go`:

```go
package plugins

import (
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plugins.json")
	l := List{Marketplaces: []Marketplace{{Name: "NovusEdge", Bundle: "mp-NovusEdge.bundle"}, {Name: "curt", GitHub: "o/curt"}},
		Plugins: []string{"docket@NovusEdge", "curt@curt"}}
	if err := l.Write(p); err != nil {
		t.Fatal(err)
	}
	back, err := Load(p)
	if err != nil || len(back.Plugins) != 2 || back.Marketplaces[0].Bundle != "mp-NovusEdge.bundle" {
		t.Fatalf("%+v %v", back, err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(l.Plugins) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
}
```

Run: `go get github.com/pelletier/go-toml/v2@latest && go test ./installer/internal/answers/ ./installer/internal/plugins/` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `answers.go`**

```go
package answers

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type Repo struct {
	Name       string `toml:"name"`
	URL        string `toml:"url"`
	BaseBranch string `toml:"base_branch"`
	Check      string `toml:"check"`
	Draft      bool   `toml:"draft"`
}

type Secrets struct {
	LinearKey    string `toml:"linear_key"`
	AgentToken   string `toml:"agent_token"`
	PublishToken string `toml:"publish_token"`
}

type Answers struct {
	Hostname    string `toml:"hostname,omitempty"`
	TeamKey     string `toml:"team_key,omitempty"`
	DigestIssue string `toml:"digest_issue,omitempty"`
	NtfyTopic   string `toml:"ntfy_topic,omitempty"`
	Repos       []Repo `toml:"repos,omitempty"`
	// Read only from an --answers file, for runs without a terminal; Save drops it.
	Secrets *Secrets `toml:"secrets,omitempty"`
}

func Load(path string) (*Answers, error) {
	a := &Answers{}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	return a, toml.Unmarshal(b, a)
}

func (a *Answers) Save(path string) error {
	c := *a
	c.Secrets = nil
	b, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (a *Answers) Overlay(o *Answers) {
	for _, f := range []struct{ dst *string; src string }{
		{&a.Hostname, o.Hostname}, {&a.TeamKey, o.TeamKey}, {&a.DigestIssue, o.DigestIssue}, {&a.NtfyTopic, o.NtfyTopic},
	} {
		if f.src != "" {
			*f.dst = f.src
		}
	}
	if len(o.Repos) > 0 {
		a.Repos = o.Repos
	}
	if o.Secrets != nil {
		a.Secrets = o.Secrets
	}
}
```

- [ ] **Step 3: Implement `plugins.go`**

```go
// Package plugins is the plugin list ship writes and the Claude step installs.
package plugins

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

type Marketplace struct {
	Name   string `json:"name"`
	GitHub string `json:"github,omitempty"` // owner/repo, for public marketplaces
	Bundle string `json:"bundle,omitempty"` // git bundle file in the stage dir, for private ones
}

type List struct {
	Marketplaces []Marketplace `json:"marketplaces"`
	Plugins      []string      `json:"plugins"` // name@marketplace
}

func Load(path string) (List, error) {
	var l List
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	return l, json.Unmarshal(b, &l)
}

func (l List) Write(path string) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
```

- [ ] **Step 4: Verify and commit**

Run: `go test ./installer/internal/answers/ ./installer/internal/plugins/` — Expected: PASS.

```bash
git add go.mod go.sum installer/internal/answers installer/internal/plugins
git commit -s -m "Add the installer's answers file and plugin list"
```

---

### Task 4: UI interface, scripted UI, step runner

**Files:**
- Create: `installer/internal/ui/ui.go`, `installer/internal/steps/step.go`, `installer/internal/steps/step_test.go`

**Interfaces:**
- Consumes: `host.Runner`, `host.Must`, `host.Cmd`, `host.Distro`, `answers.Answers`.
- Produces:
  - `type StepStatus struct{ Name, Detail string; Done bool }`.
  - `type UI interface { Status([]StepStatus); Note(title, body string); Confirm(title string, def bool) (bool, error); Text(title, def string) (string, error); Secret(id, title, help string) (string, error); ConfigForm(*answers.Answers) error; PickRedo(done []string) ([]string, error); Spin(title string, fn func() error) error }`.
  - `type Scripted struct{ A *answers.Answers; Out io.Writer }` implementing `UI`: `Confirm` returns true, `Text` returns `def`, `Secret` returns the matching `A.Secrets` field (`"linear_key"`, `"agent_token"`, `"publish_token"`) or "" when none, `ConfigForm` errors when `TeamKey`, `DigestIssue` or `Repos` is empty, `PickRedo` returns nil, `Spin` calls `fn`.
  - `type Mode string` with `Systemd`, `Loop`; `func DetectMode(pid1Comm string) Mode`.
  - `type Env struct{ Run host.Runner; UI ui.UI; Root, Home, Stage string; Mode Mode; Distro host.Distro; Answers *answers.Answers; AnswersPath string; SkipInteractive bool; UID int; Sleep func(time.Duration) }`.
  - `func (e *Env) Path(p string) string` (Root-prefixed absolute path for file access), `HomePath(rel string) string` (Root + Home + rel), `Real(rel string) string` (Home + rel, for command arguments), `Must(ctx, name string, args ...string) (string, error)`, `MustCmd(ctx, host.Cmd) (string, error)`, `Ok(ctx, name string, args ...string) bool`, `Output(ctx, name string, args ...string) string` (trimmed stdout, "" on failure), `SaveAnswers() error` (no-op when `AnswersPath` is "").
  - `type Step interface{ Name() string; Check(ctx, *Env) (bool, string, error); Apply(ctx, *Env) error }`.
  - `var ErrStopped = errors.New("stopped")`.
  - `func RunAll(ctx context.Context, e *Env, steps []Step) error`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/step_test.go`:

```go
package steps

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

type fakeStep struct {
	name      string
	done      bool
	applied   int
	applyErr  error
	staysUndone bool
}

func (s *fakeStep) Name() string { return s.name }
func (s *fakeStep) Check(context.Context, *Env) (bool, string, error) {
	return s.done, "detail " + s.name, nil
}
func (s *fakeStep) Apply(context.Context, *Env) error {
	s.applied++
	if s.applyErr != nil {
		return s.applyErr
	}
	s.done = !s.staysUndone
	return nil
}

type redoUI struct {
	*ui.Scripted
	redo []string
}

func (r redoUI) PickRedo([]string) ([]string, error) { return r.redo, nil }

func newEnv(u ui.UI) *Env {
	return &Env{Run: host.NewFake(), UI: u, Answers: &answers.Answers{}}
}

func scripted() ui.UI { return &ui.Scripted{A: &answers.Answers{}, Out: io.Discard} }

func TestRunAllSkipsDoneStepsAndAppliesTheRest(t *testing.T) {
	a, b := &fakeStep{name: "a", done: true}, &fakeStep{name: "b"}
	if err := RunAll(context.Background(), newEnv(scripted()), []Step{a, b}); err != nil {
		t.Fatal(err)
	}
	if a.applied != 0 || b.applied != 1 {
		t.Fatalf("applied a=%d b=%d", a.applied, b.applied)
	}
}

func TestRunAllRedoesPickedSteps(t *testing.T) {
	a := &fakeStep{name: "a", done: true}
	u := redoUI{Scripted: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}, redo: []string{"a"}}
	if err := RunAll(context.Background(), newEnv(u), []Step{a}); err != nil || a.applied != 1 {
		t.Fatalf("applied=%d err=%v", a.applied, err)
	}
}

func TestRunAllStopsAtFirstFailureAndNamesTheStep(t *testing.T) {
	a, b := &fakeStep{name: "a", applyErr: errors.New("boom")}, &fakeStep{name: "b"}
	err := RunAll(context.Background(), newEnv(scripted()), []Step{a, b})
	if err == nil || !strings.HasPrefix(err.Error(), "a: boom") || b.applied != 0 {
		t.Fatalf("err=%v b.applied=%d", err, b.applied)
	}
}

func TestRunAllFailsWhenCheckStillFailsAfterApply(t *testing.T) {
	a := &fakeStep{name: "a", staysUndone: true}
	err := RunAll(context.Background(), newEnv(scripted()), []Step{a})
	if err == nil || !strings.Contains(err.Error(), "check still fails: detail a") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunAllPassesErrStoppedThrough(t *testing.T) {
	a := &fakeStep{name: "a", applyErr: ErrStopped}
	if err := RunAll(context.Background(), newEnv(scripted()), []Step{a}); !errors.Is(err, ErrStopped) {
		t.Fatalf("err=%v", err)
	}
}

func TestDetectMode(t *testing.T) {
	if DetectMode("systemd\n") != Systemd || DetectMode("daytona\n") != Loop || DetectMode("") != Loop {
		t.Fatal("DetectMode")
	}
}
```

Run: `go test ./installer/internal/steps/` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `ui/ui.go`**

```go
package ui

import (
	"errors"
	"fmt"
	"io"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
)

type StepStatus struct {
	Name, Detail string
	Done         bool
}

type UI interface {
	Status(steps []StepStatus)
	Note(title, body string)
	Confirm(title string, def bool) (bool, error)
	Text(title, def string) (string, error)
	// Secret returns "" when the user leaves it empty.
	Secret(id, title, help string) (string, error)
	ConfigForm(a *answers.Answers) error
	PickRedo(done []string) ([]string, error)
	Spin(title string, fn func() error) error
}

// Scripted answers from an answers file and never reads the terminal.
type Scripted struct {
	A   *answers.Answers
	Out io.Writer
}

func (s *Scripted) Status(steps []StepStatus) {
	for _, st := range steps {
		mark := "·"
		if st.Done {
			mark = "✓"
		}
		fmt.Fprintf(s.Out, "%s %-14s %s\n", mark, st.Name, st.Detail)
	}
}

func (s *Scripted) Note(title, body string)             { fmt.Fprintf(s.Out, "\n%s\n%s\n", title, body) }
func (s *Scripted) Confirm(string, bool) (bool, error)  { return true, nil }
func (s *Scripted) Text(_, def string) (string, error)  { return def, nil }
func (s *Scripted) PickRedo([]string) ([]string, error) { return nil, nil }
func (s *Scripted) Spin(_ string, fn func() error) error { return fn() }

func (s *Scripted) Secret(id, _, _ string) (string, error) {
	if s.A.Secrets == nil {
		return "", nil
	}
	switch id {
	case "linear_key":
		return s.A.Secrets.LinearKey, nil
	case "agent_token":
		return s.A.Secrets.AgentToken, nil
	case "publish_token":
		return s.A.Secrets.PublishToken, nil
	}
	return "", fmt.Errorf("unknown secret %q", id)
}

func (s *Scripted) ConfigForm(a *answers.Answers) error {
	if a.TeamKey == "" || a.DigestIssue == "" || len(a.Repos) == 0 {
		return errors.New("the answers file needs team_key, digest_issue and at least one [[repos]]")
	}
	return nil
}
```

- [ ] **Step 3: Implement `steps/step.go`**

```go
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

func (e *Env) Path(p string) string        { return filepath.Join(e.Root, p) }
func (e *Env) HomePath(rel string) string  { return e.Path(filepath.Join(e.Home, rel)) }
func (e *Env) Real(rel string) string      { return filepath.Join(e.Home, rel) }

func (e *Env) MustCmd(ctx context.Context, c host.Cmd) (string, error) { return host.Must(ctx, e.Run, c) }

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
```

- [ ] **Step 4: Verify and commit**

Run: `go test ./installer/internal/...` — Expected: PASS.

```bash
git add installer/internal/ui/ui.go installer/internal/steps/step.go installer/internal/steps/step_test.go
git commit -s -m "Add the installer's step runner and scripted UI"
```

---

### Task 5: Root steps: packages, Tailscale, service user

**Files:**
- Create: `installer/internal/steps/packages.go`, `tailscale.go`, `user.go`, `root_test.go`

**Interfaces:**
- Consumes: `Env`, `Step`, `host.Apt/Pacman/Dnf`, `host.Cmd`.
- Produces: `type Packages struct{}`, `type Tailscale struct{}`, `type User struct{}` implementing `Step`; `const ServiceUser = "linear-agents"`.

Test helper used here and in later tasks — put it in `root_test.go`:

```go
func testEnv(t *testing.T, f *host.Fake) *Env {
	t.Helper()
	return &Env{Run: f, UI: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}, Root: t.TempDir(),
		Home: "/home/linear-agents", Answers: &answers.Answers{}, Mode: Systemd, Sleep: func(time.Duration) {}}
}
```

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/root_test.go` (imports: `context`, `io`, `os`, `path/filepath`, `strings`, `testing`, `time`, plus `answers`, `host`, `ui`):

```go
func TestPackagesCheckNamesMissingTools(t *testing.T) {
	f := host.NewFake()
	for _, tool := range []string{"git", "tmux", "curl", "node"} {
		f.On("sh -c command -v "+tool, 0, "/usr/bin/"+tool)
	}
	done, detail, _ := Packages{}.Check(context.Background(), testEnv(t, f))
	if done || detail != "missing: gh" {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
}

func TestPackagesApplyAddsGHRepoOnRHELOnly(t *testing.T) {
	for _, c := range []struct {
		distro host.Distro
		repo   bool
	}{{host.Distro{ID: "rocky", Manager: host.Dnf}, true}, {host.Distro{ID: "fedora", Manager: host.Dnf}, false}} {
		f := host.NewFake().
			On("dnf install -y dnf-plugins-core", 0, "").
			On("dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo", 0, "").
			On("dnf install -y git tmux curl nodejs gh", 0, "")
		e := testEnv(t, f)
		e.Distro = c.distro
		if err := (Packages{}).Apply(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if f.Ran("dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo") != c.repo {
			t.Fatalf("%s: repo added = %v", c.distro.ID, !c.repo)
		}
	}
}

func TestTailscaleCheck(t *testing.T) {
	f := host.NewFake().
		On("tailscale status --json", 0, `{"BackendState":"Running"}`).
		On("tailscale debug prefs", 0, `{"RunSSH":true}`)
	if done, _, _ := (Tailscale{}).Check(context.Background(), testEnv(t, f)); !done {
		t.Fatal("running with SSH should be done")
	}
	f.On("tailscale debug prefs", 0, `{"RunSSH":false}`)
	if done, detail, _ := (Tailscale{}).Check(context.Background(), testEnv(t, f)); done || detail != "Tailscale SSH off" {
		t.Fatalf("detail=%q", detail)
	}
}

func TestTailscaleApplyInLoopModeStartsDaemonAndLogsIn(t *testing.T) {
	daemon := "sh -c mkdir -p /var/lib/tailscale && nohup tailscaled --state=/var/lib/tailscale/tailscaled.state >/var/log/tailscaled.log 2>&1 &"
	f := host.NewFake().
		On("sh -c command -v tailscale", 0, "/usr/bin/tailscale").
		On(daemon, 0, "").
		On("tailscale set --ssh", 0, "").
		OnSeq("tailscale status --json", host.Result{Code: 1}, host.Result{Stdout: `{"BackendState":"NeedsLogin"}`})
	e := testEnv(t, f)
	e.Mode = Loop
	e.Answers.Hostname = "la-smoke"
	if err := (Tailscale{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(daemon) || !f.Ran("interactive: tailscale up --hostname=la-smoke") || !f.Ran("tailscale set --ssh") {
		t.Fatalf("calls: %v", f.Calls)
	}
	if f.Ran("systemctl enable --now tailscaled") {
		t.Fatal("loop mode must not call systemctl")
	}
}

func TestUserCheckRefusesPrivilegedGroups(t *testing.T) {
	f := host.NewFake().
		On("id -u linear-agents", 0, "1001").
		On("id -nG linear-agents", 0, "linear-agents wheel").
		On("chmod 700 /home/linear-agents", 0, "").
		On("loginctl show-user linear-agents -p Linger --value", 0, "yes")
	e := testEnv(t, f)
	os.MkdirAll(e.Path("/home/linear-agents"), 0o700)
	os.Chmod(e.Path("/home/linear-agents"), 0o700)
	done, detail, _ := (User{}).Check(context.Background(), e)
	if done || !strings.Contains(detail, "wheel") {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
	if err := (User{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "gpasswd -d linear-agents wheel") {
		t.Fatalf("apply err=%v", err)
	}
}

func TestUserApplyCreatesUserAndSkipsLingerInLoopMode(t *testing.T) {
	f := host.NewFake().
		On("useradd --create-home --shell /bin/bash linear-agents", 0, "").
		On("chmod 700 /home/linear-agents", 0, "").
		On("id -nG linear-agents", 0, "linear-agents")
	e := testEnv(t, f)
	e.Mode = Loop
	if err := (User{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("useradd --create-home --shell /bin/bash linear-agents") || f.Ran("loginctl enable-linger linear-agents") {
		t.Fatalf("calls: %v", f.Calls)
	}
}
```

Run: `go test ./installer/internal/steps/` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `packages.go`**

```go
package steps

import (
	"context"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

var tools = []string{"git", "tmux", "curl", "gh", "node"}

type Packages struct{}

func (Packages) Name() string { return "Packages" }

func missingTools(ctx context.Context, e *Env) []string {
	var missing []string
	for _, t := range tools {
		if !e.Ok(ctx, "sh", "-c", "command -v "+t) {
			missing = append(missing, t)
		}
	}
	return missing
}

func (Packages) Check(ctx context.Context, e *Env) (bool, string, error) {
	if m := missingTools(ctx, e); len(m) > 0 {
		return false, "missing: " + strings.Join(m, ", "), nil
	}
	return true, strings.Join(tools, ", "), nil
}

func (Packages) Apply(ctx context.Context, e *Env) error {
	var cmds []host.Cmd
	switch e.Distro.Manager {
	case host.Apt:
		env := []string{"DEBIAN_FRONTEND=noninteractive"}
		cmds = []host.Cmd{
			{Name: "apt-get", Args: []string{"update"}, Env: env},
			{Name: "apt-get", Args: []string{"install", "-y", "git", "tmux", "curl", "ca-certificates", "nodejs", "gh"}, Env: env},
		}
	case host.Pacman:
		cmds = []host.Cmd{{Name: "pacman", Args: []string{"-Syu", "--noconfirm", "--needed", "git", "tmux", "curl", "nodejs", "github-cli"}}}
	case host.Dnf:
		if e.Distro.NeedsGHRepo() {
			cmds = append(cmds,
				host.Cmd{Name: "dnf", Args: []string{"install", "-y", "dnf-plugins-core"}},
				host.Cmd{Name: "dnf", Args: []string{"config-manager", "--add-repo", "https://cli.github.com/packages/rpm/gh-cli.repo"}})
		}
		cmds = append(cmds, host.Cmd{Name: "dnf", Args: []string{"install", "-y", "git", "tmux", "curl", "nodejs", "gh"}})
	}
	for _, c := range cmds {
		if err := e.UI.Spin(c.String(), func() error { _, err := e.MustCmd(ctx, c); return err }); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 3: Implement `tailscale.go`**

```go
package steps

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

type Tailscale struct{}

func (Tailscale) Name() string { return "Tailscale" }

// tsState is "" when tailscaled does not answer.
func tsState(ctx context.Context, e *Env) string {
	var s struct{ BackendState string }
	if json.Unmarshal([]byte(e.Output(ctx, "tailscale", "status", "--json")), &s) != nil {
		return ""
	}
	return s.BackendState
}

func tsSSH(ctx context.Context, e *Env) bool {
	var p struct{ RunSSH bool }
	return json.Unmarshal([]byte(e.Output(ctx, "tailscale", "debug", "prefs")), &p) == nil && p.RunSSH
}

func (Tailscale) Check(ctx context.Context, e *Env) (bool, string, error) {
	state := tsState(ctx, e)
	switch {
	case state == "":
		return false, "tailscaled not running", nil
	case e.SkipInteractive:
		return true, "tailscaled running (login skipped)", nil
	case state != "Running":
		return false, "state " + state, nil
	case !tsSSH(ctx, e):
		return false, "Tailscale SSH off", nil
	}
	return true, "logged in, SSH on", nil
}

func (Tailscale) Apply(ctx context.Context, e *Env) error {
	if !e.Ok(ctx, "sh", "-c", "command -v tailscale") {
		install := []string{"sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh"}
		if e.Distro.Manager == host.Pacman {
			install = []string{"pacman", "-S", "--noconfirm", "--needed", "tailscale"}
		}
		if err := e.UI.Spin("Installing Tailscale", func() error { _, err := e.Must(ctx, install[0], install[1:]...); return err }); err != nil {
			return err
		}
	}
	if tsState(ctx, e) == "" {
		var err error
		if e.Mode == Systemd {
			_, err = e.Must(ctx, "systemctl", "enable", "--now", "tailscaled")
		} else {
			// No init to supervise it; a sandbox stop ends it and the next ship starts it again.
			_, err = e.Must(ctx, "sh", "-c", "mkdir -p /var/lib/tailscale && nohup tailscaled --state=/var/lib/tailscale/tailscaled.state >/var/log/tailscaled.log 2>&1 &")
		}
		if err != nil {
			return err
		}
		for range 20 {
			if tsState(ctx, e) != "" {
				break
			}
			e.Sleep(500 * time.Millisecond)
		}
		if tsState(ctx, e) == "" {
			return errors.New("tailscaled did not start; see /var/log/tailscaled.log or journalctl -u tailscaled")
		}
	}
	if e.SkipInteractive {
		return nil
	}
	if tsState(ctx, e) != "Running" {
		def := e.Answers.Hostname
		if def == "" {
			def = "linear-agents"
		}
		name, err := e.UI.Text("Tailscale hostname for this host", def)
		if err != nil {
			return err
		}
		e.Answers.Hostname = name
		if err := e.Run.Interactive(ctx, cmd("tailscale", "up", "--hostname="+name)); err != nil {
			return err
		}
		e.UI.Note("Tailscale key expiry", "Open https://login.tailscale.com/admin/machines, find "+name+
			" and choose Disable key expiry. An untagged node's key otherwise expires after 180 days.")
	}
	_, err := e.Must(ctx, "tailscale", "set", "--ssh")
	return err
}
```

Add to `step.go`:

```go
func cmd(name string, args ...string) host.Cmd { return host.Cmd{Name: name, Args: args} }
```

- [ ] **Step 4: Implement `user.go`**

```go
package steps

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
)

const ServiceUser = "linear-agents"

var privileged = []string{"sudo", "wheel", "admin"}

type User struct{}

func (User) Name() string { return "Service user" }

func privilegedGroup(ctx context.Context, e *Env) string {
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", ServiceUser)) {
		if slices.Contains(privileged, g) {
			return g
		}
	}
	return ""
}

func (User) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.Ok(ctx, "id", "-u", ServiceUser) {
		return false, "no " + ServiceUser + " user", nil
	}
	st, err := os.Stat(e.Path("/home/" + ServiceUser))
	if err != nil {
		return false, "no home directory", nil
	}
	if p := st.Mode().Perm(); p != 0o700 {
		return false, fmt.Sprintf("home is mode %o", p), nil
	}
	if g := privilegedGroup(ctx, e); g != "" {
		return false, "in group " + g, nil
	}
	if e.Mode == Systemd && e.Output(ctx, "loginctl", "show-user", ServiceUser, "-p", "Linger", "--value") != "yes" {
		return false, "lingering off", nil
	}
	return true, "exists, home 700, no sudo", nil
}

func (User) Apply(ctx context.Context, e *Env) error {
	if !e.Ok(ctx, "id", "-u", ServiceUser) {
		if _, err := e.Must(ctx, "useradd", "--create-home", "--shell", "/bin/bash", ServiceUser); err != nil {
			return err
		}
	}
	if _, err := e.Must(ctx, "chmod", "700", "/home/"+ServiceUser); err != nil {
		return err
	}
	if g := privilegedGroup(ctx, e); g != "" {
		return fmt.Errorf("%s is in group %s, which gives agents sudo; remove it with gpasswd -d %s %s and run again", ServiceUser, g, ServiceUser, g)
	}
	if e.Mode == Systemd {
		if _, err := e.Must(ctx, "loginctl", "enable-linger", ServiceUser); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Verify and commit**

Run: `go test ./installer/internal/steps/` — Expected: PASS.

```bash
git add installer/internal/steps
git commit -s -m "Add the installer's root steps: packages, Tailscale, service user"
```

---

### Task 6: `linear-agents run` loop for hosts without systemd

**Files:**
- Modify: `src/linear_agents/tick.py` (append `run_loop`), `src/linear_agents/cli.py` (`cmd_run`, parser, `COMMANDS`)
- Test: `tests/test_run_loop.py`

**Interfaces:**
- Produces: `run_loop(argv: list[str], log: Path, interval_s: float, run=subprocess.run, sleep=time.sleep, now=time.time, iterations: int | None = None) -> None`; CLI `linear-agents run`.

- [ ] **Step 1: Write the failing test**

`tests/test_run_loop.py`:

```python
import subprocess

from linear_agents.tick import run_loop


def test_a_failing_tick_is_logged_and_the_loop_goes_on(tmp_path):
    codes = iter([1, 0])
    calls, sleeps = [], []

    def run(argv, stdout, stderr, check):
        calls.append(argv)
        stdout.write("tick output\n")
        return subprocess.CompletedProcess(argv, next(codes))

    log = tmp_path / "state" / "run.log"
    run_loop(["linear-agents", "tick"], log, 180, run=run, sleep=sleeps.append, now=lambda: 0, iterations=2)

    assert calls == [["linear-agents", "tick"], ["linear-agents", "tick"]]
    assert sleeps == [180]
    text = log.read_text()
    assert text.count("--- tick 1970-01-01T00:00:00Z") == 2
    assert "--- exit 1" in text and "--- exit 0" in text
    assert text.count("tick output") == 2
```

Run: `uv run pytest tests/test_run_loop.py -q` — Expected: FAIL, `ImportError: cannot import name 'run_loop'`.

- [ ] **Step 2: Implement**

Append to `src/linear_agents/tick.py` (add `import subprocess` and `import time` at the top if absent, and `from pathlib import Path` if absent):

```python
def run_loop(
    argv: list[str],
    log: Path,
    interval_s: float,
    run=subprocess.run,
    sleep=time.sleep,
    now=time.time,
    iterations: int | None = None,
) -> None:
    """Tick every interval_s on hosts without systemd, where the timer cannot."""
    log.parent.mkdir(parents=True, exist_ok=True)
    n = 0
    while iterations is None or n < iterations:
        # A subprocess per tick, as under the timer: one that crashes does not end the loop.
        with log.open("a") as f:
            f.write(f"--- tick {time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime(now()))}Z\n")
            f.flush()
            code = run(argv, stdout=f, stderr=subprocess.STDOUT, check=False).returncode
            f.write(f"--- exit {code}\n")
        n += 1
        if iterations is None or n < iterations:
            sleep(interval_s)
```

In `src/linear_agents/cli.py`, after `cmd_tick`:

```python
def cmd_run(args) -> int:
    from linear_agents.tick import run_loop

    cfg, _ = _config(args)
    config = ["--config", args.config] if args.config else []
    run_loop([sys.argv[0], *config, "tick"], cfg.state_root / "run.log", 180)
    return 0
```

Add `"run": cmd_run,` to `COMMANDS` and `sub.add_parser("run")` in `build_parser`.

- [ ] **Step 3: Verify and commit**

Run: `uv run pytest -q && uv run ruff check && uv run pyrefly check` — Expected: all pass.

```bash
git add src/linear_agents/tick.py src/linear_agents/cli.py tests/test_run_loop.py
git commit -s -m "Add linear-agents run, a tick loop for hosts without systemd"
```

---

### Task 7: Service steps: repo from bundle, secrets

**Files:**
- Create: `installer/internal/steps/bundle.go`, `repo.go`, `secrets.go`, `service_test.go`

**Interfaces:**
- Consumes: `Env`, `cmd`, `host.Cmd`.
- Produces:
  - `func bundleHead(ctx, e *Env, bundle string) (string, error)`; `func syncFromBundle(ctx, e *Env, bundle, dir string) error` (dir is a real path; init when `<dir>/.git` is absent, then `git -C dir fetch -q <bundle> HEAD` and `git -C dir checkout -q --detach FETCH_HEAD`).
  - `type Repo struct{}`, `type Secrets struct{}` implementing `Step`.
  - `const repoRel = "agents/linear-agents"`, `const worktreesRel = "agents/worktrees"`.
  - `var secretSpecs []secretSpec` with ids `linear_key`, `agent_token`, `publish_token` and files `linear.key`, `gh-agent.token`, `gh-publish.token` under `.config/linear-agents`; `func cleanSecret(v string) (string, error)`; `func validSecret(path string) (bool, string)`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/service_test.go`:

```go
package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

const sha = "1111111111111111111111111111111111111111"

func repoFake(head string) *host.Fake {
	return host.NewFake().
		On("git bundle list-heads /stage/linear-agents.bundle", 0, sha+" HEAD\n").
		On("git -C /home/linear-agents/agents/linear-agents rev-parse HEAD", 0, head+"\n")
}

func TestRepoCheckComparesBundleHeadAndInstall(t *testing.T) {
	e := testEnv(t, repoFake(sha))
	e.Stage = "/stage"
	if done, detail, _ := (Repo{}).Check(context.Background(), e); done || detail != "linear-agents not installed" {
		t.Fatalf("detail=%q", detail)
	}
	os.MkdirAll(e.HomePath(".local/bin"), 0o755)
	os.WriteFile(e.HomePath(".local/bin/linear-agents"), nil, 0o755)
	if done, _, _ := (Repo{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
	e.Run = repoFake("2222222222222222222222222222222222222222")
	if done, detail, _ := (Repo{}).Check(context.Background(), e); done || !strings.Contains(detail, "bundle at 1111111") {
		t.Fatalf("detail=%q", detail)
	}
}

func TestRepoApplyStopsOnLocalEditsWithGitsMessage(t *testing.T) {
	f := repoFake("").
		On("git -C /home/linear-agents/agents/linear-agents fetch -q /stage/linear-agents.bundle HEAD", 0, "")
	f.Responses["git -C /home/linear-agents/agents/linear-agents checkout -q --detach FETCH_HEAD"] = host.Result{
		Stderr: "error: Your local changes to the following files would be overwritten by checkout:\n\tplugin/evals/x.json\n", Code: 1}
	e := testEnv(t, f)
	e.Stage = "/stage"
	os.MkdirAll(e.HomePath(repoRel+"/.git"), 0o755)
	err := (Repo{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "would be overwritten") {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("./setup") {
		t.Fatal("setup ran after a failed checkout")
	}
}

func TestCleanSecret(t *testing.T) {
	for in, want := range map[string]string{"lin_api_x\n": "lin_api_x", "  ghp_y\r\n": "ghp_y"} {
		if got, err := cleanSecret(in); err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"LINEAR_KEY=lin_api_x", "a b", "a\nb"} {
		if _, err := cleanSecret(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestSecretsApplyKeepsValidFilesOnEmptyInputAndWritesMode600(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.Answers = &answers.Answers{}
	dir := e.HomePath(".config/linear-agents")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "linear.key"), []byte("lin_old\n"), 0o600)
	scriptedSecrets(e, &answers.Secrets{AgentToken: "ghp_agent\n", PublishToken: "ghp_pub"})
	if err := (Secrets{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "linear.key")); string(b) != "lin_old\n" {
		t.Fatalf("linear.key overwritten: %q", b)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "gh-agent.token"))
	st, _ := os.Stat(filepath.Join(dir, "gh-agent.token"))
	if string(b) != "ghp_agent\n" || st.Mode().Perm() != 0o600 {
		t.Fatalf("%q %o", b, st.Mode().Perm())
	}
	if done, _, _ := (Secrets{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
}

func TestSecretsApplyRequiresMissingSecrets(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if err := (Secrets{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "Linear API key is required") {
		t.Fatalf("err=%v", err)
	}
}
```

Add to `root_test.go`:

```go
func scriptedSecrets(e *Env, s *answers.Secrets) {
	e.Answers.Secrets = s
	e.UI = &ui.Scripted{A: e.Answers, Out: io.Discard}
}
```

Run: `go test ./installer/internal/steps/` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `bundle.go`**

```go
package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func bundleHead(ctx context.Context, e *Env, bundle string) (string, error) {
	out, err := e.Must(ctx, "git", "bundle", "list-heads", bundle)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if sha, ref, ok := strings.Cut(strings.TrimSpace(line), " "); ok && ref == "HEAD" {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%s has no HEAD", bundle)
}

// syncFromBundle checks out the bundle's HEAD in dir. checkout refuses to overwrite local
// edits, so a hand-edited host checkout stops the run instead of losing them.
func syncFromBundle(ctx context.Context, e *Env, bundle, dir string) error {
	if _, err := os.Stat(filepath.Join(e.Root, dir, ".git")); err != nil {
		if _, err := e.Must(ctx, "git", "init", "-q", dir); err != nil {
			return err
		}
	}
	if _, err := e.Must(ctx, "git", "-C", dir, "fetch", "-q", bundle, "HEAD"); err != nil {
		return err
	}
	_, err := e.Must(ctx, "git", "-C", dir, "checkout", "-q", "--detach", "FETCH_HEAD")
	return err
}
```

- [ ] **Step 3: Implement `repo.go`**

```go
package steps

import (
	"context"
	"os"
	"path/filepath"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

const (
	repoRel      = "agents/linear-agents"
	worktreesRel = "agents/worktrees"
)

type Repo struct{}

func (Repo) Name() string { return "Service repo" }

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (Repo) Check(ctx context.Context, e *Env) (bool, string, error) {
	want, err := bundleHead(ctx, e, filepath.Join(e.Stage, "linear-agents.bundle"))
	if err != nil {
		return false, "", err
	}
	got := e.Output(ctx, "git", "-C", e.Real(repoRel), "rev-parse", "HEAD")
	if got != want {
		return false, "checkout at " + short(got) + ", bundle at " + short(want), nil
	}
	if _, err := os.Stat(e.HomePath(".local/bin/linear-agents")); err != nil {
		return false, "linear-agents not installed", nil
	}
	return true, "at " + short(want), nil
}

func (Repo) Apply(ctx context.Context, e *Env) error {
	dir := e.Real(repoRel)
	if err := syncFromBundle(ctx, e, filepath.Join(e.Stage, "linear-agents.bundle"), dir); err != nil {
		return err
	}
	for _, c := range []host.Cmd{
		{Name: "./setup", Dir: dir},
		{Name: e.Real(".local/bin/mise"), Args: []string{"exec", "--", "uv", "tool", "install", "--force", "--editable", "."}, Dir: dir},
	} {
		if err := e.UI.Spin(c.String(), func() error { _, err := e.MustCmd(ctx, c); return err }); err != nil {
			return err
		}
	}
	return os.MkdirAll(e.HomePath(worktreesRel), 0o755)
}
```

- [ ] **Step 4: Implement `secrets.go`**

```go
package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type secretSpec struct{ id, file, title, help string }

var secretSpecs = []secretSpec{
	{"linear_key", "linear.key", "Linear API key",
		"Linear → Settings → Security & access → Personal API keys. Limit it to team NF."},
	{"agent_token", "gh-agent.token", "GitHub agent token (read-only)",
		"github.com/settings/personal-access-tokens, fine-grained. Resource owner: the mapped repos' owner; " +
			"repository access: the mapped repos; permissions, all Read-only: Contents, Pull requests, Checks, Issues. " +
			"Agents use it through gh, so it must not be able to push."},
	{"publish_token", "gh-publish.token", "GitHub publish token",
		"Fine-grained, same owner and repos. Contents: Read and write; Pull requests: Read and write. " +
			"Only the service uses it, to push branches and open PRs."},
}

var envPrefix = regexp.MustCompile(`^[A-Z_]+=`)

// cleanSecret strips what a paste adds and refuses what doctor would reject.
func cleanSecret(v string) (string, error) {
	v = strings.TrimSpace(v)
	switch {
	case strings.ContainsAny(v, "\r\n"):
		return "", errors.New("paste a single line")
	case envPrefix.MatchString(v):
		return "", errors.New("paste only the value, not NAME=value")
	case strings.ContainsAny(v, " \t"):
		return "", errors.New("it contains spaces")
	}
	return v, nil
}

func validSecret(path string) (bool, string) {
	st, err := os.Stat(path)
	if err != nil {
		return false, "missing"
	}
	b, _ := os.ReadFile(path)
	if _, err := cleanSecret(string(b)); err != nil {
		return false, err.Error()
	}
	if strings.TrimSpace(string(b)) == "" {
		return false, "empty"
	}
	if p := st.Mode().Perm(); p&0o077 != 0 {
		return false, fmt.Sprintf("mode %o", p)
	}
	return true, ""
}

type Secrets struct{}

func (Secrets) Name() string { return "Secrets" }

func secretPath(e *Env, s secretSpec) string {
	return e.HomePath(filepath.Join(".config/linear-agents", s.file))
}

func (Secrets) Check(_ context.Context, e *Env) (bool, string, error) {
	for _, s := range secretSpecs {
		if ok, why := validSecret(secretPath(e, s)); !ok {
			return false, s.file + ": " + why, nil
		}
	}
	return true, "3 secrets, mode 600", nil
}

func (Secrets) Apply(_ context.Context, e *Env) error {
	if err := os.MkdirAll(e.HomePath(".config/linear-agents"), 0o700); err != nil {
		return err
	}
	for _, s := range secretSpecs {
		path := secretPath(e, s)
		valid, _ := validSecret(path)
		title := s.title
		if valid {
			title += " (leave empty to keep the current one)"
		}
		raw, err := e.UI.Secret(s.id, title, s.help)
		if err != nil {
			return err
		}
		if strings.TrimSpace(raw) == "" {
			if valid {
				continue
			}
			return errors.New(s.title + " is required")
		}
		v, err := cleanSecret(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", s.title, err)
		}
		if err := os.WriteFile(path, []byte(v+"\n"), 0o600); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Verify and commit**

Run: `go test ./installer/internal/steps/` — Expected: PASS.

```bash
git add installer/internal/steps
git commit -s -m "Add the installer's repo and secrets steps"
```

---

### Task 8: Claude step

**Files:**
- Create: `installer/internal/steps/claude.go`, `claude_test.go`

**Interfaces:**
- Consumes: `plugins.List`, `plugins.Load`, `syncFromBundle`, `host.AtLeast`, `worktreesRel`.
- Produces: `type Claude struct{}` implementing `Step`; `var minClaude = [3]int{2, 1, 293}`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/claude_test.go`:

```go
package steps

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/plugins"
)

const claudeBin = "/home/linear-agents/.local/bin/claude"

func claudeEnv(t *testing.T, f *host.Fake, l plugins.List) *Env {
	e := testEnv(t, f)
	e.Stage = t.TempDir()
	if err := l.Write(filepath.Join(e.Stage, "plugins.json")); err != nil {
		t.Fatal(err)
	}
	return e
}

func writeInstalled(t *testing.T, e *Env, json string) {
	os.MkdirAll(e.HomePath(".claude/plugins"), 0o755)
	os.WriteFile(e.HomePath(".claude/plugins/installed_plugins.json"), []byte(json), 0o644)
}

func TestClaudeCheck(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295 (Claude Code)\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := claudeEnv(t, f, plugins.List{Plugins: []string{"curt@curt"}})
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "plugins missing: curt@curt" {
		t.Fatalf("detail=%q", detail)
	}
	writeInstalled(t, e, `{"version":2,"plugins":{"curt@curt":[{"scope":"user"}]}}`)
	if done, _, _ := (Claude{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
	f.On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"apiKey"}`)
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "not logged in to a claude.ai subscription" {
		t.Fatalf("detail=%q", detail)
	}
}

func TestClaudeApplyAfterAbortedLoginSkipsReinstall(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`).
		On(claudeBin+" plugin marketplace add o/curt", 0, "").
		On(claudeBin+" plugin install curt@curt", 0, "")
	e := claudeEnv(t, f, plugins.List{Marketplaces: []plugins.Marketplace{{Name: "curt", GitHub: "o/curt"}}, Plugins: []string{"curt@curt"}})
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if f.Ran("sh -c curl -fsSL https://claude.ai/install.sh | bash") {
		t.Fatal("reinstalled a current claude")
	}
	if !f.Ran(claudeBin+" plugin install curt@curt") {
		t.Fatalf("calls: %v", f.Calls)
	}
}

func TestClaudeApplyRegistersBundledMarketplaceAsDirectory(t *testing.T) {
	dir := "/home/linear-agents/agents/marketplaces/NovusEdge"
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`).
		On("git init -q "+dir, 0, "")
	e := claudeEnv(t, f, plugins.List{Marketplaces: []plugins.Marketplace{{Name: "NovusEdge", Bundle: "mp-NovusEdge.bundle"}},
		Plugins: []string{"docket@NovusEdge"}})
	bundle := filepath.Join(e.Stage, "mp-NovusEdge.bundle")
	f.On("git -C "+dir+" fetch -q "+bundle+" HEAD", 0, "").
		On("git -C "+dir+" checkout -q --detach FETCH_HEAD", 0, "").
		On(claudeBin+" plugin marketplace add "+dir, 0, "").
		On(claudeBin+" plugin install docket@NovusEdge", 0, "")
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(claudeBin + " plugin marketplace add " + dir) {
		t.Fatalf("calls: %v", f.Calls)
	}
}
```

Run: `go test ./installer/internal/steps/ -run Claude` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `claude.go`**

```go
package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/plugins"
)

// The --name and --remote-control flags the launcher passes need this version.
var minClaude = [3]int{2, 1, 293}

const loginNote = `Claude Code opens next, in ~/agents/worktrees.
1. Type /login and finish it in the browser with the subscription account.
2. In /config, turn on "Push when actions required" and turn off "Push when Claude decides".
3. Accept "Enable Remote Control" if asked.
4. Leave with /exit.`

type Claude struct{}

func (Claude) Name() string { return "Claude" }

func (e *Env) claude() string { return e.Real(".local/bin/claude") }

func claudeVersion(ctx context.Context, e *Env) (bool, string) {
	v := e.Output(ctx, e.claude(), "--version")
	if v == "" {
		return false, "not installed"
	}
	return host.AtLeast(v, minClaude), v
}

func loggedIn(ctx context.Context, e *Env) bool {
	if e.SkipInteractive {
		return true
	}
	var s struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	return json.Unmarshal([]byte(e.Output(ctx, e.claude(), "auth", "status")), &s) == nil &&
		s.LoggedIn && s.AuthMethod == "claude.ai"
}

func missingPlugins(e *Env, l plugins.List) []string {
	var installed struct {
		Plugins map[string][]struct{ Scope string } `json:"plugins"`
	}
	b, _ := os.ReadFile(e.HomePath(".claude/plugins/installed_plugins.json"))
	json.Unmarshal(b, &installed)
	var missing []string
	for _, p := range l.Plugins {
		user := false
		for _, inst := range installed.Plugins[p] {
			user = user || inst.Scope == "user"
		}
		if !user {
			missing = append(missing, p)
		}
	}
	return missing
}

func knownMarketplaces(e *Env) map[string]json.RawMessage {
	known := map[string]json.RawMessage{}
	b, _ := os.ReadFile(e.HomePath(".claude/plugins/known_marketplaces.json"))
	json.Unmarshal(b, &known)
	return known
}

func (Claude) Check(ctx context.Context, e *Env) (bool, string, error) {
	l, err := plugins.Load(filepath.Join(e.Stage, "plugins.json"))
	if err != nil {
		return false, "", err
	}
	if ok, v := claudeVersion(ctx, e); !ok {
		return false, "claude " + v + "; needs 2.1.293", nil
	}
	if !loggedIn(ctx, e) {
		return false, "not logged in to a claude.ai subscription", nil
	}
	if m := missingPlugins(e, l); len(m) > 0 {
		return false, "plugins missing: " + strings.Join(m, ", "), nil
	}
	_, v := claudeVersion(ctx, e)
	return true, v + ", logged in, " + plural(len(l.Plugins), "plugin"), nil
}

func (Claude) Apply(ctx context.Context, e *Env) error {
	l, err := plugins.Load(filepath.Join(e.Stage, "plugins.json"))
	if err != nil {
		return err
	}
	if ok, _ := claudeVersion(ctx, e); !ok {
		if err := e.UI.Spin("Installing Claude Code", func() error {
			_, err := e.Must(ctx, "sh", "-c", "curl -fsSL https://claude.ai/install.sh | bash")
			return err
		}); err != nil {
			return err
		}
	}
	if !loggedIn(ctx, e) {
		e.UI.Note("Claude login", loginNote)
		if err := os.MkdirAll(e.HomePath(worktreesRel), 0o755); err != nil {
			return err
		}
		if err := e.Run.Interactive(ctx, host.Cmd{Name: e.claude(), Dir: e.Real(worktreesRel)}); err != nil {
			return err
		}
		if !loggedIn(ctx, e) {
			return errors.New("still not logged in to a claude.ai subscription (claude auth status)")
		}
	}
	known := knownMarketplaces(e)
	for _, m := range l.Marketplaces {
		source := m.GitHub
		if m.Bundle != "" {
			// Synced on every run: a directory marketplace loads its plugins in place.
			source = e.Real(filepath.Join("agents/marketplaces", m.Name))
			if err := syncFromBundle(ctx, e, filepath.Join(e.Stage, m.Bundle), source); err != nil {
				return err
			}
		}
		if _, ok := known[m.Name]; ok {
			continue
		}
		if _, err := e.Must(ctx, e.claude(), "plugin", "marketplace", "add", source); err != nil {
			return err
		}
	}
	for _, p := range missingPlugins(e, l) {
		if _, err := e.Must(ctx, e.claude(), "plugin", "install", p); err != nil {
			return err
		}
	}
	return nil
}
```

Add to `step.go`:

```go
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
```

The bundled-marketplace test's fake has no `known_marketplaces.json`, so `add` runs; the abort-login test's `auth status` reports logged in, so no interactive call happens. Both pin Review Focus item 1: the version check gates the install.

- [ ] **Step 3: Verify and commit**

Run: `go test ./installer/internal/steps/` — Expected: PASS.

```bash
git add installer/internal/steps
git commit -s -m "Add the installer's Claude step with bundled private marketplaces"
```

---

### Task 9: Config step, rendering and its golden file

**Files:**
- Create: `installer/internal/steps/config.go`, `config_test.go`, `installer/internal/steps/testdata/config.golden.toml` (generated), `tests/test_installer_config.py`

**Interfaces:**
- Consumes: `deploy.Files`, `answers.Answers`, `answers.Repo`.
- Produces:
  - `func RenderConfig(example []byte, a *answers.Answers) ([]byte, error)`.
  - `func doctor(ctx, e *Env) (out string, fails []string)` — runs `<home>/.local/bin/linear-agents doctor`, returns stdout and the lines starting with `FAIL`.
  - `type Config struct{}` implementing `Step`.
  - `const credentialHelper string`.

- [ ] **Step 1: Write the failing Go tests**

`installer/internal/steps/config_test.go`:

```go
package steps

import (
	"context"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/deploy"
	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenAnswers() *answers.Answers {
	return &answers.Answers{TeamKey: "NF", DigestIssue: "NF-186", NtfyTopic: "la-test",
		Repos: []answers.Repo{{Name: "nimblefox-template", URL: "https://github.com/nimble-fox-ai/nimblefox-template",
			BaseBranch: "main", Check: "mise exec -- just check", Draft: false}}}
}

func TestRenderConfigMatchesGolden(t *testing.T) {
	example, _ := deploy.Files.ReadFile("config.example.toml")
	got, err := RenderConfig(example, goldenAnswers())
	if err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/config.golden.toml"
	if *update {
		os.WriteFile(golden, got, 0o644)
	}
	want, _ := os.ReadFile(golden)
	if string(got) != string(want) {
		t.Fatalf("render differs from %s; run go test ./installer/internal/steps -update\n%s", golden, got)
	}
	for _, s := range []string{"mcp_servers", "backend", "sentry"} {
		if strings.Contains(string(got), s) {
			t.Errorf("example placeholder %q kept", s)
		}
	}
}

func TestConfigCheckUsesDoctorConfigAndRepoLines(t *testing.T) {
	bin := "/home/linear-agents/.local/bin/linear-agents doctor"
	f := host.NewFake().On(bin, 1, "ok    config: /x\nFAIL  repo r: /home/linear-agents/src/r is not a git clone\nFAIL  claude login: none\n")
	done, detail, _ := (Config{}).Check(context.Background(), testEnv(t, f))
	if done || detail != "repo r: /home/linear-agents/src/r is not a git clone" {
		t.Fatalf("detail=%q", detail)
	}
	f.On(bin, 1, "ok    config: /x\nFAIL  claude login: none\n")
	if done, _, _ := (Config{}).Check(context.Background(), testEnv(t, f)); !done {
		t.Fatal("non-repo FAIL lines belong to other steps")
	}
	f.On(bin, 1, "FAIL  config: digest_issue is required\n")
	if done, _, _ := (Config{}).Check(context.Background(), testEnv(t, f)); done {
		t.Fatal("config FAIL must not be done")
	}
}
```

Run: `go test ./installer/internal/steps/ -run Config` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `config.go`**

```go
package steps

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"

	"github.com/NovusEdge/linear-agents/deploy"
	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/pelletier/go-toml/v2"
)

// Read on every fetch, so a replaced token takes effect without touching the clone.
const credentialHelper = `!f() { test "$1" = get && printf 'username=x-access-token\npassword=%s\n' "$(cat "$HOME/.config/linear-agents/gh-agent.token")"; }; f`

var (
	repoName  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	githubURL = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+?(\.git)?/?$`)
)

func RenderConfig(example []byte, a *answers.Answers) ([]byte, error) {
	var m map[string]any
	if err := toml.Unmarshal(example, &m); err != nil {
		return nil, err
	}
	m["team_key"] = a.TeamKey
	m["digest_issue"] = a.DigestIssue
	m["ntfy_url"] = "https://ntfy.sh/" + a.NtfyTopic
	// The example's [repos.backend] and [mcp_servers.sentry] are placeholders.
	delete(m, "mcp_servers")
	repos := map[string]any{}
	for _, r := range a.Repos {
		repos[r.Name] = map[string]any{"path": "~/src/" + r.Name, "base_branch": r.BaseBranch, "check": r.Check, "draft": r.Draft}
	}
	m["repos"] = repos
	return toml.Marshal(m)
}

func doctor(ctx context.Context, e *Env) (string, []string) {
	r, _ := e.Run.Run(ctx, cmd(e.Real(".local/bin/linear-agents"), "doctor"))
	var fails []string
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.HasPrefix(line, "FAIL") {
			fails = append(fails, line)
		}
	}
	return r.Stdout, fails
}

func randomTopic() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 24)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return "la-" + string(b)
}

type Config struct{}

func (Config) Name() string { return "Config" }

func (Config) Check(ctx context.Context, e *Env) (bool, string, error) {
	out, fails := doctor(ctx, e)
	if !strings.Contains("\n"+out, "\nok    config:") {
		return false, "no valid config", nil
	}
	for _, f := range fails {
		if strings.HasPrefix(f, "FAIL  repo ") {
			return false, strings.TrimPrefix(f, "FAIL  "), nil
		}
	}
	return true, "config loads, repos cloned", nil
}

func validate(a *answers.Answers) error {
	if a.TeamKey == "" || a.DigestIssue == "" || len(a.Repos) == 0 {
		return errors.New("team key, digest issue and at least one repo are required")
	}
	for _, r := range a.Repos {
		if !repoName.MatchString(r.Name) {
			return fmt.Errorf("repo name %q: use letters, digits, '.', '_' or '-', matching its Linear label", r.Name)
		}
		if !githubURL.MatchString(r.URL) {
			return fmt.Errorf("repo %s: %q is not an https://github.com/<owner>/<repo> URL", r.Name, r.URL)
		}
	}
	return nil
}

func (Config) Apply(ctx context.Context, e *Env) error {
	a := e.Answers
	if a.TeamKey == "" {
		a.TeamKey = "NF"
	}
	if a.NtfyTopic == "" {
		a.NtfyTopic = randomTopic()
	}
	if len(a.Repos) == 0 {
		a.Repos = []answers.Repo{{BaseBranch: "main", Check: "just check", Draft: true}}
	}
	if err := e.UI.ConfigForm(a); err != nil {
		return err
	}
	if err := validate(a); err != nil {
		return err
	}
	if err := e.SaveAnswers(); err != nil {
		return err
	}
	example, _ := deploy.Files.ReadFile("config.example.toml")
	rendered, err := RenderConfig(example, a)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.HomePath(".config/linear-agents"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(e.HomePath(".config/linear-agents/config.toml"), rendered, 0o600); err != nil {
		return err
	}
	ntfy := "https://ntfy.sh/" + a.NtfyTopic
	if _, err := e.Must(ctx, "curl", "-fsS", "-H", "Priority: 5", "-H", "Title: linear-agents", "-d", "test push from la-setup", ntfy); err != nil {
		return err
	}
	arrived, err := e.UI.Confirm("Subscribe to "+ntfy+" in the ntfy app. Did the priority 5 test push arrive?", true)
	if err != nil {
		return err
	}
	if !arrived {
		return errors.New("the ntfy test push did not arrive at " + ntfy + "; subscribe to it in the app and run again")
	}
	for _, r := range a.Repos {
		dir := e.Real("src/" + r.Name)
		if _, err := os.Stat(e.HomePath("src/" + r.Name + "/.git")); err != nil {
			if _, err := e.Must(ctx, "git", "-c", "credential.helper=", "-c", "credential.helper="+credentialHelper, "clone", "-q", r.URL, dir); err != nil {
				return err
			}
		}
		if _, err := e.Must(ctx, "git", "-C", dir, "config", "--replace-all", "credential.helper", ""); err != nil {
			return err
		}
		if _, err := e.Must(ctx, "git", "-C", dir, "config", "--add", "credential.helper", credentialHelper); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 3: Generate the golden file and write the Python test**

Run: `go test ./installer/internal/steps/ -run RenderConfig -update && go test ./installer/internal/steps/`
Expected: PASS; `installer/internal/steps/testdata/config.golden.toml` exists. Read it and confirm it has `team_key = 'NF'`, `digest_issue = 'NF-186'`, a `[repos.nimblefox-template]` table with `draft = false`, and no `mcp_servers`.

`tests/test_installer_config.py`:

```python
from pathlib import Path

from linear_agents.config import load

GOLDEN = Path(__file__).parent.parent / "installer/internal/steps/testdata/config.golden.toml"


def test_the_installers_rendered_config_loads():
    cfg = load(GOLDEN)
    assert cfg.team_key == "NF"
    assert cfg.digest_issue == "NF-186"
    assert cfg.ntfy_url == "https://ntfy.sh/la-test"
    repo = cfg.repos["nimblefox-template"]
    assert repo.draft is False
    assert repo.check == "mise exec -- just check"
    assert repo.path == Path("~/src/nimblefox-template").expanduser()
    assert cfg.mcp_servers == {}
```

Run: `uv run pytest tests/test_installer_config.py -q` — Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add installer/internal/steps tests/test_installer_config.py
git commit -s -m "Add the installer's config step, checked against the Python loader"
```

---

### Task 10: Run step

**Files:**
- Create: `installer/internal/steps/run.go`, `run_test.go`, `installer/internal/steps/list.go`

**Interfaces:**
- Consumes: `deploy.Files`, `deploy.Units`, `doctor`, `ErrStopped`.
- Produces: `type Run struct{}` implementing `Step`; `func RootSteps() []Step` (`Packages`, `Tailscale`, `User`); `func ServiceSteps() []Step` (`Repo`, `Claude`, `Secrets`, `Config`, `Run`).

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/run_test.go`:

```go
package steps

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

const doctorCmd = "/home/linear-agents/.local/bin/linear-agents doctor"

type declineUI struct{ *ui.Scripted }

func (declineUI) Confirm(string, bool) (bool, error) { return false, nil }

func TestRunLoopModeStartsTmuxBeforeDoctor(t *testing.T) {
	f := host.NewFake().
		On("tmux -L linear-agents new-session -d -s la-loop sleep infinity", 0, "").
		On(doctorCmd, 0, "ok    tmux: server on socket linear-agents\n").
		On("tmux -L linear-agents respawn-pane -k -t =la-loop "+loopCommand("/home/linear-agents"), 0, "")
	e := testEnv(t, f)
	e.Mode = Loop
	if err := (Run{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	start, doc := -1, -1
	for i, c := range f.Calls {
		if strings.Contains(c, "new-session") {
			start = i
		}
		if c == doctorCmd {
			doc = i
		}
	}
	if start < 0 || doc < start {
		t.Fatalf("tmux must start before doctor: %v", f.Calls)
	}
}

func TestRunStopsOnDoctorFailLines(t *testing.T) {
	f := host.NewFake().
		On("tmux -L linear-agents new-session -d -s la-loop sleep infinity", 0, "").
		On(doctorCmd, 1, "FAIL  linear: 401\nFAIL  claude login: none\n")
	e := testEnv(t, f)
	e.Mode = Loop
	err := (Run{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "2 FAIL lines") {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("tmux -L linear-agents respawn-pane -k -t =la-loop " + loopCommand("/home/linear-agents")) {
		t.Fatal("started ticking despite FAIL lines")
	}
}

func TestRunDeclineIsErrStopped(t *testing.T) {
	f := host.NewFake().
		On("tmux -L linear-agents new-session -d -s la-loop sleep infinity", 0, "").
		On(doctorCmd, 0, "ok    tmux: x\n")
	e := testEnv(t, f)
	e.Mode = Loop
	e.UI = declineUI{&ui.Scripted{A: &answers.Answers{}, Out: io.Discard}}
	if err := (Run{}).Apply(context.Background(), e); !errors.Is(err, ErrStopped) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunLoopCheckLooksAtThePaneCommand(t *testing.T) {
	f := host.NewFake().On("tmux -N -L linear-agents list-panes -t =la-loop -F #{pane_start_command}", 0, "sleep infinity\n")
	e := testEnv(t, f)
	e.Mode = Loop
	if done, _, _ := (Run{}).Check(context.Background(), e); done {
		t.Fatal("an idle la-loop is not ticking")
	}
	f.On("tmux -N -L linear-agents list-panes -t =la-loop -F #{pane_start_command}", 0, loopCommand("/home/linear-agents")+"\n")
	if done, _, _ := (Run{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
}
```

Run: `go test ./installer/internal/steps/ -run Run` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `run.go`**

```go
package steps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NovusEdge/linear-agents/deploy"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

type Run struct{}

func (Run) Name() string { return "Run" }

func (e *Env) userctl(args ...string) host.Cmd {
	// sudo -iu gives no XDG_RUNTIME_DIR, and systemctl --user needs it to find the manager.
	return host.Cmd{Name: "systemctl", Args: append([]string{"--user"}, args...),
		Env: []string{"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(e.UID)}}
}

// loopCommand matches the PATH the tick unit sets, which sessions and checks inherit.
func loopCommand(home string) string {
	return fmt.Sprintf("env PATH=%[1]s/.local/bin:%[1]s/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin linear-agents run", home)
}

func (e *Env) loopPane(ctx context.Context) string {
	return e.Output(ctx, "tmux", "-N", "-L", "linear-agents", "list-panes", "-t", "=la-loop", "-F", "#{pane_start_command}")
}

func (Run) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode == Loop {
		if strings.Contains(e.loopPane(ctx), "linear-agents run") {
			return true, "la-loop ticking", nil
		}
		return false, "loop not running", nil
	}
	for _, u := range deploy.Units {
		want, _ := deploy.Files.ReadFile(u)
		got, err := os.ReadFile(e.HomePath(filepath.Join(".config/systemd/user", u)))
		if err != nil || !bytes.Equal(got, want) {
			return false, u + " not installed or out of date", nil
		}
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", "linear-agents-tmux.service")); err != nil || r.Code != 0 {
		return false, "tmux service not running", nil
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-enabled", "--quiet", "linear-agents.timer")); err != nil || r.Code != 0 {
		return false, "timer not enabled", nil
	}
	return true, "timer enabled", nil
}

func (Run) Apply(ctx context.Context, e *Env) error {
	if e.Mode == Systemd {
		dir := e.HomePath(".config/systemd/user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for _, u := range deploy.Units {
			b, _ := deploy.Files.ReadFile(u)
			if err := os.WriteFile(filepath.Join(dir, u), b, 0o644); err != nil {
				return err
			}
		}
		for _, c := range []host.Cmd{e.userctl("daemon-reload"), e.userctl("enable", "--now", "linear-agents-tmux.service")} {
			if _, err := e.MustCmd(ctx, c); err != nil {
				return err
			}
		}
	} else if e.loopPane(ctx) == "" {
		// Idle until doctor passes; doctor's tmux check needs a server, and ticks never start one.
		if _, err := e.Must(ctx, "tmux", "-L", "linear-agents", "new-session", "-d", "-s", "la-loop", "sleep infinity"); err != nil {
			return err
		}
	}
	out, fails := doctor(ctx, e)
	e.UI.Note("linear-agents doctor", out)
	if len(fails) > 0 {
		return fmt.Errorf("doctor reports %d FAIL lines; fix them and run just ship again", len(fails))
	}
	start, err := e.UI.Confirm("Doctor reports no FAIL line. Start ticking every 3 minutes?", true)
	if err != nil {
		return err
	}
	if !start {
		return ErrStopped
	}
	if e.Mode == Systemd {
		_, err = e.MustCmd(ctx, e.userctl("enable", "--now", "linear-agents.timer"))
	} else {
		_, err = e.Must(ctx, "tmux", "-L", "linear-agents", "respawn-pane", "-k", "-t", "=la-loop", loopCommand(e.Home))
	}
	return err
}
```

`installer/internal/steps/list.go`:

```go
package steps

func RootSteps() []Step    { return []Step{Packages{}, Tailscale{}, User{}} }
func ServiceSteps() []Step { return []Step{Repo{}, Claude{}, Secrets{}, Config{}, Run{}} }
```

Claude comes before Secrets so the interactive login happens early, while I am at the terminal anyway; neither needs the other.

- [ ] **Step 3: Verify and commit**

Run: `go test ./installer/internal/...` — Expected: PASS.

```bash
git add installer/internal/steps
git commit -s -m "Add the installer's run step for both modes"
```

---

### Task 11: huh UI, main wiring, phase guard and handoff

**Files:**
- Create: `installer/internal/ui/huh.go`, `installer/main_test.go`
- Modify: `installer/main.go`

**Interfaces:**
- Consumes: everything in `steps`, `ui`, `answers`, `host`; `ship.Main(args []string) int` (Task 12; until then `main.go` prints "ship: not built yet" — remove that line in Task 12).
- Produces: `func checkPhase(phase string, euid int) error`; `func handoffArgs(answersIn string, skip bool, dest string) []string`; `ui.NewHuh() *Huh`.

- [ ] **Step 1: Write the failing tests**

`installer/main_test.go`:

```go
package main

import (
	"slices"
	"testing"
)

func TestCheckPhase(t *testing.T) {
	if checkPhase("root", 0) != nil || checkPhase("service", 1001) != nil {
		t.Fatal("valid phases refused")
	}
	for _, c := range []struct {
		phase string
		euid  int
	}{{"root", 1000}, {"service", 0}, {"other", 0}} {
		if checkPhase(c.phase, c.euid) == nil {
			t.Errorf("%s as %d: want refusal", c.phase, c.euid)
		}
	}
}

func TestHandoffArgs(t *testing.T) {
	got := handoffArgs("/tmp/la-setup/e2e.toml", true, "/home/linear-agents/.cache/la-setup")
	want := []string{"--phase", "service", "--answers", "/home/linear-agents/.cache/la-setup/e2e.toml", "--skip-interactive"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := handoffArgs("", false, "/d"); !slices.Equal(got, []string{"--phase", "service"}) {
		t.Fatalf("got %v", got)
	}
}
```

Run: `go test ./installer/` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `ui/huh.go`**

Check the v2 APIs on pkg.go.dev for `charm.land/huh/v2` (`NewInput`, `EchoMode`, `NewConfirm`, `NewMultiSelect`, `NewNote`), `charm.land/huh/v2/spinner` and `charm.land/lipgloss/v2` before writing; adjust names if they differ, keeping the behavior below.

Run: `go get charm.land/huh/v2@latest charm.land/lipgloss/v2@latest`

```go
package ui

import (
	"fmt"
	"strconv"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
)

type Huh struct{}

func NewHuh() *Huh { return &Huh{} }

var (
	doneMark = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("✓")
	todoMark = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("•")
	dim      = lipgloss.NewStyle().Faint(true)
	heading  = lipgloss.NewStyle().Bold(true)
)

func (*Huh) Status(steps []StepStatus) {
	for _, s := range steps {
		mark := todoMark
		if s.Done {
			mark = doneMark
		}
		lipgloss.Println(fmt.Sprintf("%s %-14s %s", mark, s.Name, dim.Render(s.Detail)))
	}
}

func (*Huh) Note(title, body string) {
	huh.NewForm(huh.NewGroup(huh.NewNote().Title(title).Description(body))).Run()
}

func (*Huh) Confirm(title string, def bool) (bool, error) {
	v := def
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Value(&v))).Run()
	return v, err
}

func (*Huh) Text(title, def string) (string, error) {
	v := def
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title(title).Value(&v))).Run()
	return v, err
}

func (*Huh) Secret(_, title, help string) (string, error) {
	var v string
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title(title).Description(help).
		EchoMode(huh.EchoModePassword).Value(&v))).Run()
	return v, err
}

func (*Huh) PickRedo(done []string) ([]string, error) {
	if len(done) == 0 {
		return nil, nil
	}
	var picked []string
	err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
		Title("Already done. Pick any to redo, or press enter to continue.").
		Options(huh.NewOptions(done...)...).Value(&picked))).Run()
	return picked, err
}

func (*Huh) Spin(title string, fn func() error) error {
	var err error
	if serr := spinner.New().Title(title).Action(func() { err = fn() }).Run(); serr != nil {
		return serr
	}
	return err
}

func (*Huh) ConfigForm(a *answers.Answers) error {
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Linear team key").Value(&a.TeamKey),
		huh.NewInput().Title("Digest issue key").Description("The pinned issue tier 3 reports go to, such as NF-186.").Value(&a.DigestIssue),
		huh.NewInput().Title("ntfy topic").Description("Subscribe to https://ntfy.sh/<topic> in the ntfy app. Keep it long and random.").Value(&a.NtfyTopic),
	)).Run(); err != nil {
		return err
	}
	for i := 0; ; i++ {
		if i == len(a.Repos) {
			more := false
			if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Map another repo?").Value(&more))).Run(); err != nil || !more {
				return err
			}
			a.Repos = append(a.Repos, answers.Repo{BaseBranch: "main", Check: "just check", Draft: true})
		}
		r := &a.Repos[i]
		if err := huh.NewForm(huh.NewGroup(
			huh.NewNote().Title(heading.Render("Repo " + strconv.Itoa(i+1))),
			huh.NewInput().Title("Name").Description("Exactly the Linear label that routes issues to it.").Value(&r.Name),
			huh.NewInput().Title("GitHub URL").Description("https://github.com/<owner>/<repo>").Value(&r.URL),
			huh.NewInput().Title("Base branch").Value(&r.BaseBranch),
			huh.NewInput().Title("Check command").Value(&r.Check),
			huh.NewConfirm().Title("Open draft PRs?").Description("Private repos of a GitHub Free organization cannot have drafts.").Value(&r.Draft),
		)).Run(); err != nil {
			return err
		}
	}
}
```

- [ ] **Step 3: Implement `main.go`**

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/steps"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

const serviceStage = "/home/" + steps.ServiceUser + "/.cache/la-setup"

const loopWarning = `PID 1 is not systemd, so this host runs in loop mode:
- linear-agents run in tmux session la-loop ticks every 3 minutes, in place of the timer;
- processes a timed-out check daemonized are not killed when its tick ends;
- after the host stops, run just ship again to restart tailscaled and the loop.
Continue?`

func main() { os.Exit(run(os.Args[1:])) }

func checkPhase(phase string, euid int) error {
	switch {
	case phase == "root" && euid != 0:
		return errors.New("the root phase runs as root; just ship runs it with sudo")
	case phase == "service" && euid == 0:
		return errors.New("the service phase runs as " + steps.ServiceUser + ", not root")
	case phase != "root" && phase != "service":
		return fmt.Errorf("unknown phase %q", phase)
	}
	return nil
}

func handoffArgs(answersIn string, skip bool, dest string) []string {
	args := []string{"--phase", "service"}
	if answersIn != "" {
		args = append(args, "--answers", filepath.Join(dest, filepath.Base(answersIn)))
	}
	if skip {
		args = append(args, "--skip-interactive")
	}
	return args
}

func handoff(ctx context.Context, r host.Runner, stage string, args []string) error {
	owner := steps.ServiceUser + ":" + steps.ServiceUser
	for _, c := range []host.Cmd{
		{Name: "install", Args: []string{"-d", "-o", steps.ServiceUser, "-g", steps.ServiceUser, "-m", "700", filepath.Dir(serviceStage), serviceStage}},
		{Name: "cp", Args: []string{"-a", stage + "/.", serviceStage + "/"}},
		{Name: "chown", Args: []string{"-R", owner, serviceStage}},
	} {
		if _, err := host.Must(ctx, r, c); err != nil {
			return err
		}
	}
	return r.Interactive(ctx, host.Cmd{Name: "sudo", Args: append([]string{"-iu", steps.ServiceUser, serviceStage + "/la-setup"}, args...)})
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "ship" {
		fmt.Fprintln(os.Stderr, "ship: not built yet")
		return 2
	}
	fs := flag.NewFlagSet("la-setup", flag.ContinueOnError)
	phase := fs.String("phase", "root", "root or service; the root phase hands off to the service phase itself")
	answersIn := fs.String("answers", "", "answers file supplying every form value, for runs without a terminal")
	skip := fs.Bool("skip-interactive", false, "skip the Claude and Tailscale logins and read answers from --answers")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := checkPhase(*phase, os.Geteuid()); err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 1
	}
	stage := filepath.Dir(exe)
	logPath := filepath.Join(stage, "setup.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 1
	}
	defer logf.Close()
	runner := host.Exec{Log: logf}

	pid1, _ := os.ReadFile("/proc/1/comm")
	osRelease, _ := os.ReadFile("/etc/os-release")
	distro, err := host.ParseOSRelease(string(osRelease))
	if err != nil {
		fmt.Fprintln(os.Stderr, "la-setup:", err)
		return 1
	}
	a := &answers.Answers{}
	env := &steps.Env{Run: runner, Stage: stage, Mode: steps.DetectMode(string(pid1)), Distro: distro,
		Answers: a, SkipInteractive: *skip, UID: os.Getuid(), Sleep: time.Sleep}
	if *phase == "service" {
		env.Home, _ = os.UserHomeDir()
		env.AnswersPath = filepath.Join(stage, "answers.toml")
		if saved, err := answers.Load(env.AnswersPath); err == nil {
			a.Overlay(saved)
		}
	}
	if *answersIn != "" {
		in, err := answers.Load(*answersIn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "la-setup:", err)
			return 1
		}
		a.Overlay(in)
	}
	var u ui.UI = ui.NewHuh()
	if *skip {
		u = &ui.Scripted{A: a, Out: os.Stdout}
	}
	env.UI = u
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

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
		err = handoff(ctx, runner, stage, handoffArgs(*answersIn, *skip, serviceStage))
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode() // the service phase already reported its error
		}
	}
	switch {
	case errors.Is(err, steps.ErrStopped):
		fmt.Println("Stopped. Run just ship again to continue.")
		return 0
	case err != nil:
		fmt.Fprintf(os.Stderr, "\n%s\n\nFull log: %s\n", err, logPath)
		return 1
	}
	if *phase == "service" {
		fmt.Println("Done.")
	}
	return 0
}
```

- [ ] **Step 4: Verify and commit**

Run: `go vet ./... && go test ./... && just build-setup` — Expected: PASS, binaries built.

```bash
git add go.mod go.sum installer
git commit -s -m "Wire the installer's phases, huh forms and root-to-service handoff"
```

---

### Task 12: Ship from the desktop

**Files:**
- Create: `installer/internal/ship/ship.go`, `daytona.go`, `plugins.go`, `ship_test.go`
- Modify: `installer/main.go` (dispatch to `ship.Main`)

**Interfaces:**
- Consumes: `plugins.List`, `plugins.Marketplace`, `host.Exec`, `host.Must`.
- Produces:
  - `func Main(args []string) int` — flags `--skip-interactive`, `--answers FILE`, `--stage-only DIR`; positional target (not needed with `--stage-only`).
  - `type planned struct{ List plugins.List; BundleFrom map[string]string; Warnings []string }`; `func planPlugins(configDir string, private func(repo string) (bool, error)) (planned, error)`.
  - `type Dest struct{ SSH string; Opts []string }`; `func resolveTarget(ctx, target, daytonaConfig string, client *http.Client) (Dest, error)`.
  - `func archFor(unameM string) (string, error)` (`x86_64`→`amd64`, `aarch64`/`arm64`→`arm64`).

- [ ] **Step 1: Write the failing tests**

`installer/internal/ship/ship_test.go`:

```go
package ship

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.Marshal(v)
	os.WriteFile(path, b, 0o644)
}

func TestPlanPluginsBundlesPrivateAndSkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{
		"docket@NovusEdge": true, "curt@curt": true, "off@curt": false, "ghost@nowhere": true}})
	writeJSON(t, filepath.Join(dir, "plugins/known_marketplaces.json"), map[string]any{
		"NovusEdge": map[string]any{"source": map[string]string{"source": "github", "repo": "NovusEdge/docket"}, "installLocation": "/m/NovusEdge"},
		"curt":      map[string]any{"source": map[string]string{"source": "github", "repo": "o/curt"}, "installLocation": "/m/curt"},
	})
	p, err := planPlugins(dir, func(repo string) (bool, error) { return repo == "NovusEdge/docket", nil })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.List.Plugins, []string{"curt@curt", "docket@NovusEdge"}) {
		t.Fatalf("plugins %v", p.List.Plugins)
	}
	if p.BundleFrom["NovusEdge"] != "/m/NovusEdge" || len(p.BundleFrom) != 1 {
		t.Fatalf("bundles %v", p.BundleFrom)
	}
	for _, m := range p.List.Marketplaces {
		if m.Name == "curt" && m.GitHub != "o/curt" || m.Name == "NovusEdge" && m.Bundle != "mp-NovusEdge.bundle" {
			t.Fatalf("marketplace %+v", m)
		}
	}
	if len(p.Warnings) != 1 {
		t.Fatalf("warnings %v", p.Warnings)
	}
}

func TestResolveTargetPlainIsPassedThrough(t *testing.T) {
	d, err := resolveTarget(context.Background(), "la-smoke", "/none", nil)
	if err != nil || d.SSH != "la-smoke" || len(d.Opts) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestResolveTargetDaytonaGetsAToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer acc" || r.Header.Get("X-Daytona-Organization-ID") != "org1" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/sandbox/la-smoke":
			json.NewEncoder(w).Encode(map[string]string{"id": "sb1"})
		case r.Method == "POST" && r.URL.Path == "/api/sandbox/sb1/ssh-access" && r.URL.Query().Get("expiresInMinutes") == "60":
			json.NewEncoder(w).Encode(map[string]string{"token": "tok", "sshCommand": "ssh tok@ssh.example.test"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "config.json")
	writeJSON(t, cfg, map[string]any{"activeProfile": "initial", "profiles": []any{map[string]any{
		"id": "initial", "activeOrganizationId": "org1",
		"api": map[string]any{"url": srv.URL + "/api", "key": "k", "token": map[string]string{"accessToken": "acc"}}}}})
	d, err := resolveTarget(context.Background(), "daytona:la-smoke", cfg, srv.Client())
	if err != nil || d.SSH != "tok@ssh.example.test" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestArchFor(t *testing.T) {
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64": "arm64"} {
		if got, err := archFor(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := archFor("riscv64"); err == nil {
		t.Error("riscv64: want error")
	}
}
```

Run: `go test ./installer/internal/ship/` — Expected: FAIL to compile.

- [ ] **Step 2: Implement `plugins.go`**

```go
package ship

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/plugins"
)

type planned struct {
	List       plugins.List
	BundleFrom map[string]string // marketplace name → its local clone
	Warnings   []string
}

func planPlugins(configDir string, private func(repo string) (bool, error)) (planned, error) {
	p := planned{BundleFrom: map[string]string{}}
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	var known map[string]struct {
		Source struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
		} `json:"source"`
		InstallLocation string `json:"installLocation"`
	}
	for path, v := range map[string]any{"settings.json": &settings, "plugins/known_marketplaces.json": &known} {
		b, err := os.ReadFile(filepath.Join(configDir, path))
		if err != nil {
			return p, err
		}
		if err := json.Unmarshal(b, v); err != nil {
			return p, fmt.Errorf("%s: %w", path, err)
		}
	}
	var names []string
	for name, on := range settings.EnabledPlugins {
		if on {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	added := map[string]bool{}
	for _, name := range names {
		_, mp, _ := strings.Cut(name, "@")
		k, ok := known[mp]
		if !ok || k.Source.Source != "github" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: marketplace %q is not a known GitHub marketplace; skipped", name, mp))
			continue
		}
		p.List.Plugins = append(p.List.Plugins, name)
		if added[mp] {
			continue
		}
		added[mp] = true
		priv, err := private(k.Source.Repo)
		if err != nil {
			return p, fmt.Errorf("%s visibility: %w", k.Source.Repo, err)
		}
		m := plugins.Marketplace{Name: mp, GitHub: k.Source.Repo}
		if priv {
			m = plugins.Marketplace{Name: mp, Bundle: "mp-" + mp + ".bundle"}
			p.BundleFrom[mp] = k.InstallLocation
		}
		p.List.Marketplaces = append(p.List.Marketplaces, m)
	}
	return p, nil
}
```

- [ ] **Step 3: Implement `daytona.go`**

```go
package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type Dest struct {
	SSH  string
	Opts []string
}

type daytonaAuth struct{ url, bearer, org string }

func loadDaytona(path string) (daytonaAuth, error) {
	var c struct {
		ActiveProfile string `json:"activeProfile"`
		Profiles      []struct {
			ID                   string `json:"id"`
			ActiveOrganizationID string `json:"activeOrganizationId"`
			API                  struct {
				URL   string          `json:"url"`
				Key   string          `json:"key"`
				Token json.RawMessage `json:"token"`
			} `json:"api"`
		} `json:"profiles"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return daytonaAuth{}, fmt.Errorf("%w; log the Daytona CLI in with daytona login", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return daytonaAuth{}, err
	}
	for _, p := range c.Profiles {
		if p.ID != c.ActiveProfile {
			continue
		}
		// A browser login stores a token object; the API key alone is refused for SSH access.
		var tok struct {
			AccessToken string `json:"accessToken"`
		}
		bearer := p.API.Key
		if json.Unmarshal(p.API.Token, &tok) == nil && tok.AccessToken != "" {
			bearer = tok.AccessToken
		}
		return daytonaAuth{url: p.API.URL, bearer: bearer, org: p.ActiveOrganizationID}, nil
	}
	return daytonaAuth{}, errors.New("no active Daytona profile; run daytona login")
}

func (a daytonaAuth) do(ctx context.Context, client *http.Client, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, a.url+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.bearer)
	req.Header.Set("X-Daytona-Organization-ID", a.org)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Daytona API %s %s: %s", method, path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func resolveTarget(ctx context.Context, target, daytonaConfig string, client *http.Client) (Dest, error) {
	name, ok := strings.CutPrefix(target, "daytona:")
	if !ok {
		return Dest{SSH: target}, nil
	}
	auth, err := loadDaytona(daytonaConfig)
	if err != nil {
		return Dest{}, err
	}
	var sb struct{ ID string }
	if err := auth.do(ctx, client, "GET", "/sandbox/"+url.PathEscape(name), &sb); err != nil {
		return Dest{}, err
	}
	var access struct {
		Token      string `json:"token"`
		SSHCommand string `json:"sshCommand"`
	}
	if err := auth.do(ctx, client, "POST", "/sandbox/"+sb.ID+"/ssh-access?expiresInMinutes=60", &access); err != nil {
		return Dest{}, err
	}
	host := "ssh.app.daytona.io"
	if f := strings.Fields(access.SSHCommand); len(f) > 0 {
		if _, h, ok := strings.Cut(f[len(f)-1], "@"); ok {
			host = h
		}
	}
	return Dest{SSH: access.Token + "@" + host, Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}, nil
}
```

- [ ] **Step 4: Implement `ship.go`**

```go
// Package ship stages the installer, bundles and plugin list and runs it on a host.
package ship

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

func archFor(unameM string) (string, error) {
	switch strings.TrimSpace(unameM) {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported architecture %q", strings.TrimSpace(unameM))
}

func sshCmd(d Dest, tty bool, remote string) host.Cmd {
	args := append([]string{}, d.Opts...)
	if tty {
		args = append(args, "-t")
	}
	return host.Cmd{Name: "ssh", Args: append(args, d.SSH, remote)}
}

func ghPrivate(repo string) (bool, error) {
	out, err := exec.Command("gh", "repo", "view", repo, "--json", "visibility", "-q", ".visibility").Output()
	return strings.TrimSpace(string(out)) == "PRIVATE", err
}

func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// stageFiles writes the host binary for arch, the bundles and plugins.json into dir.
func stageFiles(ctx context.Context, r host.Runner, root, dir, arch, answersIn string) error {
	bin, err := os.ReadFile(filepath.Join(root, "build", "la-setup-linux-"+arch))
	if err != nil {
		return fmt.Errorf("%w; run just build-setup", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "la-setup"), bin, 0o755); err != nil {
		return err
	}
	if _, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", root, "bundle", "create", filepath.Join(dir, "linear-agents.bundle"), "HEAD"}}); err != nil {
		return err
	}
	plan, err := planPlugins(claudeConfigDir(), ghPrivate)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		fmt.Fprintln(os.Stderr, "ship:", w)
	}
	for name, from := range plan.BundleFrom {
		if _, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"-C", from, "bundle", "create", filepath.Join(dir, "mp-"+name+".bundle"), "HEAD"}}); err != nil {
			return err
		}
	}
	if err := plan.List.Write(filepath.Join(dir, "plugins.json")); err != nil {
		return err
	}
	if answersIn != "" {
		b, err := os.ReadFile(answersIn)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(answersIn)), b, 0o600)
	}
	return nil
}

func Main(args []string) int {
	fs := flag.NewFlagSet("la-setup ship", flag.ContinueOnError)
	skip := fs.Bool("skip-interactive", false, "pass --skip-interactive to the host")
	answersIn := fs.String("answers", "", "answers file to copy and pass to the host")
	stageOnly := fs.String("stage-only", "", "write the stage for linux/amd64 into this directory and stop")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	r := host.Exec{Log: os.Stderr}
	rootOut, err := host.Must(ctx, r, host.Cmd{Name: "git", Args: []string{"rev-parse", "--show-toplevel"}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ship:", err)
		return 1
	}
	root := strings.TrimSpace(rootOut)
	if *stageOnly != "" {
		if err := os.MkdirAll(*stageOnly, 0o755); err == nil {
			err = stageFiles(ctx, r, root, *stageOnly, "amd64", *answersIn)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ship:", err)
			return 1
		}
		return 0
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: la-setup ship [--skip-interactive] [--answers FILE] <ssh-target|daytona:SANDBOX>")
		return 2
	}
	if err := ship(ctx, r, root, fs.Arg(0), *answersIn, *skip); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode() // the host already reported its error
		}
		fmt.Fprintln(os.Stderr, "ship:", err)
		return 1
	}
	return 0
}

func ship(ctx context.Context, r host.Runner, root, target, answersIn string, skip bool) error {
	home, _ := os.UserHomeDir()
	dest, err := resolveTarget(ctx, target, filepath.Join(home, ".config/daytona/config.json"), http.DefaultClient)
	if err != nil {
		return err
	}
	unameM, err := host.Must(ctx, r, sshCmd(dest, false, "uname -m"))
	if err != nil {
		return err
	}
	arch, err := archFor(unameM)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "la-ship-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := stageFiles(ctx, r, root, dir, arch, answersIn); err != nil {
		return err
	}
	if _, err := host.Must(ctx, r, sshCmd(dest, false, "rm -rf /tmp/la-setup && mkdir -p /tmp/la-setup")); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	scp := append(append([]string{"-q"}, dest.Opts...), files...)
	if _, err := host.Must(ctx, r, host.Cmd{Name: "scp", Args: append(scp, dest.SSH+":/tmp/la-setup/")}); err != nil {
		return err
	}
	remote := "sudo /tmp/la-setup/la-setup"
	if answersIn != "" {
		remote += " --answers /tmp/la-setup/" + filepath.Base(answersIn)
	}
	if skip {
		remote += " --skip-interactive"
	}
	return r.Interactive(ctx, sshCmd(dest, true, remote))
}
```

Add `"errors"` to `ship.go`'s imports.

In `installer/main.go`, replace the `ship: not built yet` branch with:

```go
	if len(args) > 0 && args[0] == "ship" {
		return ship.Main(args[1:])
	}
```

importing `github.com/NovusEdge/linear-agents/installer/internal/ship`.

- [ ] **Step 5: Verify and commit**

Run: `go vet ./... && go test ./... && just build-setup && build/la-setup ship --stage-only build/stage && ls build/stage && jq . build/stage/plugins.json`
Expected: PASS; `build/stage` holds `la-setup`, `linear-agents.bundle`, `mp-NovusEdge.bundle`, `plugins.json`; the plugin list names `docket@NovusEdge` with `"bundle"` and the public marketplaces with `"github"`.

```bash
git add installer
git commit -s -m "Add la-setup ship: stage bundles and the plugin list, then run on the host"
```

---

### Task 13: Documentation

**Files:**
- Modify: `docs/vm-setup.md` (replace the part above `## Upgrading`)

- [ ] **Step 1: Rewrite the setup section**

Replace everything from `# VM setup` down to the line before `## Upgrading` with:

````markdown
# Host setup

`just ship <target>` sets a host up and is safe to run again: it checks every step, skips the
finished ones and resumes at the first that is not. `<target>` is an SSH destination, such as
the host's Tailscale name, or `daytona:<sandbox>`. It needs `go` (from `mise install`), `gh`
logged in on the desktop, and, for Daytona, the Daytona CLI logged in with `daytona login`.

The first run on a host asks you to log in to Tailscale and to Claude (`/login`, the two push
settings in `/config`, Remote Control), to paste the Linear key and the two GitHub tokens, to
fill in the config form and to confirm an ntfy test push. It ends by running
`linear-agents doctor` and starts ticking only when doctor reports no FAIL line. Everything it
runs is logged to `/tmp/la-setup/setup.log` (root steps) and
`~linear-agents/.cache/la-setup/setup.log` (the rest).

Upgrading the service on a host is `just ship <target>` again: it ships the current commit as
a git bundle, since the repo is private and the host has no credential for it.

## Hosts

Debian, Ubuntu, Arch, Fedora and RHEL VMs run in systemd mode: a timer ticks every 3 minutes
and a user unit keeps the tmux server up. Any host whose PID 1 is not systemd runs in loop mode:
`linear-agents run` in tmux session `la-loop` ticks instead, processes a timed-out check
daemonized are not killed when its tick ends, and after the host stops `just ship` restarts
tailscaled and the loop.

A Daytona container for a smoke test, about $0.17 an hour while running:

```bash
daytona create --snapshot daytona-medium --name la-smoke --auto-stop 0
just ship daytona:la-smoke
# when done
daytona stop la-smoke
```

`--auto-stop 0` matters: a running tick loop does not count as activity, so the default
15-minute auto-stop ends it mid-run. A stopped sandbox bills only its disk. If
`daytona-medium` has less than 2 vCPU and 4 GiB, pass `--cpu 2 --memory 4096` instead of
`--snapshot`.

## What the installer cannot do

- Linear: the labels `claude`, `escalation`, `needs-decision` and one per mapped repo (named
  exactly as its `[repos.<name>]` key), and a pinned digest issue, must exist in the team.
  Doctor checks them.
- GitHub tokens are made in the browser. The installer shows the permissions each needs next to
  its input.
- In the Tailscale admin console, disable key expiry for the host; an untagged node's key
  otherwise expires after 180 days.
- In each mapped repo, gitignore everything `./setup` and the check write, or every issue fails
  at prepare. If the mapped repos need tools outside `~/.local/bin`, the mise shims and the
  system directories, add them to `Environment=PATH` in `deploy/linear-agents.service` and to
  `loopCommand` in `installer/internal/steps/run.go`.

Watch it with `journalctl --user -u linear-agents -f` (systemd mode) or
`tail -f ~/.local/state/linear-agents/run.log` (loop mode), and attach to a session with
`tmux -L linear-agents attach -t NF-123`.

````

- [ ] **Step 2: Fix references and commit**

Run: `grep -rn "vm-setup.md\|VM setup" README.md docs | grep -v "^docs/superpowers"` and update any link text that says "VM setup" to "Host setup"; the file path stays `docs/vm-setup.md`.

In the smoke test steps, step 2's sentence about `systemctl --user start linear-agents.service` and step 10 apply to systemd mode only; add "(systemd mode)" after "Check that one failed tick does not stop the timer" in step 10.

```bash
git add docs README.md
git commit -s -m "Document host setup with just ship"
```

---

### Task 14: End-to-end in local VMs

**Files:**
- Create: `installer/testdata/e2e-answers.toml`
- Modify: whatever the runs show to be wrong, each fix with its own test where the logic allows

**Interfaces:**
- Consumes: `la-setup ship --stage-only`, `--answers`, `--skip-interactive`.

- [ ] **Step 1: Write the answers file**

`installer/testdata/e2e-answers.toml`:

```toml
team_key = "NF"
digest_issue = "NF-186"
ntfy_topic = "la-e2e-throwaway-topic-do-not-subscribe"

[[repos]]
name = "hello-world"
url = "https://github.com/octocat/Hello-World"
base_branch = "master"
check = "true"
draft = true

[secrets]
linear_key = "lin_api_e2e_invalid"
agent_token = "github_pat_e2e_invalid"
publish_token = "github_pat_e2e_invalid"
```

The repo is public, so the clone needs no valid token; the secrets are invalid on purpose.

- [ ] **Step 2: Stage**

Run: `just build-setup && rm -rf build/stage && build/la-setup ship --stage-only build/stage --answers installer/testdata/e2e-answers.toml`

- [ ] **Step 3: Run in each VM**

With the stoat MCP tools (`list_images` to find Debian 13, Arch and Fedora images; `create`, `copy_to`, `exec`, `destroy`), for each image:

1. Create a VM with 2 vCPU, 4 GiB, network on.
2. `copy_to` `build/stage/` to `/tmp/la-setup/`.
3. `exec`: `sudo /tmp/la-setup/la-setup --skip-interactive --answers /tmp/la-setup/e2e-answers.toml` (timeout 30 min).
4. Pass: exit status 1 with "doctor reports N FAIL lines", and the FAIL lines in `/home/linear-agents/.cache/la-setup/setup.log` are only `linear`, `github agent token`, `github publish token` and `claude login`. Any other FAIL line, or a failure before the Run step, is a bug: fix it on the desktop, re-stage, and rerun step 3 on the same VM.
5. Rerun step 3. Pass: the step list shows every step before Run as done (`✓`) and the only applied step is Run.
6. `destroy` the VM.

- [ ] **Step 4: Commit**

```bash
git add installer
git commit -s -m "Add the installer's end-to-end answers file and fixes from VM runs"
```

---

### Task 15: Bring up the Daytona smoke host

This task needs me at the terminal for the logins and secrets; it is the loop-mode end-to-end test.

- [ ] **Step 1: Create the sandbox**

```bash
daytona organization use Engrammic
daytona create --snapshot daytona-medium --name la-smoke --auto-stop 0
daytona exec la-smoke -- sh -c 'nproc; free -g | head -2'
```

Expected: at least 2 vCPU and 4 GiB; otherwise delete it and recreate with `--cpu 2 --memory 4096`.

- [ ] **Step 2: Ship**

Run: `just ship daytona:la-smoke`

Expected: loop-mode warning, then the root steps, the Tailscale login URL, the handoff, the Claude login, secrets, config form, ntfy push, doctor with no FAIL line, and `la-loop` started. Then `ssh linear-agents@la-smoke tmux -L linear-agents ls` over Tailscale lists `la-loop`.

- [ ] **Step 3: Hand over to the smoke test**

Continue with the smoke test in `docs/vm-setup.md` from step 2 (`linear-agents tick` by hand). When it is finished or paused: `daytona stop la-smoke`.
