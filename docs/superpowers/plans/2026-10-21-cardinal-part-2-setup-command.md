# cardinal Part 2: `flocc cardinal` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A desktop verb, `flocc cardinal setup|login|status|attach|restart|remove`, that sets up, keeps alive, checks and removes cardinal on a machine, plus a CARDINAL table in `flocc status -H all`.

**Architecture:** The desktop builds `flocc` and `flocc-setup` and runs four passes: Incus (the owner's ssh, through `create.Env`'s incus runner), root (one `ssh -t` and one sudo from a verified `mktemp -d` stage), user (`ssh -T <user>@<host>` with a JSON document on stdin) and host (the desktop's route to each host, running a staged `flocc-setup` as root). The machine-side work is new `flocc-setup` phases built on the existing `steps.Step`/`RunAll` framework; each pass ends with one marker line of JSON that the desktop takes out of the stream.

**Tech Stack:** Go (installer module), systemd user units, tmux, nftables, OpenSSH, Incus. No new dependency.

**Spec:** `docs/superpowers/specs/2026-10-21-cardinal-part-2-setup-command-design.md`

## Global Constraints

- No new Go module dependency.
- Root never writes inside the user's home; writes there go through `runuser -u <user> --` or the user's own ssh.
- Every ssh to the user passes `-o ForwardAgent=no -o ForwardX11=no -o ClearAllForwardings=yes`, and uses `<user>@` plus the host part of `<machine>`.
- Output from cardinal's binaries is untrusted on the desktop: control characters stripped, each string cut to 200 characters.
- The root pass never installs packages and never removes a group or a sudoers rule.
- Unit names `cardinal.service`, `cardinal-renew.service`, `cardinal-renew.timer`; tmux socket and session `-L <user>` / `<user>`; Remote Control name `<user>`.
- Saved file `~/.config/flocc/cardinal/<user>.toml`: `machine`, `user`, `hosts`, `key`, `renew_hour` (default 4), `removed`.
- Status fails on: unit not active, login not full, last turn older than 45 minutes, session older than 6 days, a host unreachable (full status only).
- Renewal restarts only when the unit is active, the session is at least 20 h old and no transcript changed for 5 minutes.
- Comments follow the repo's density: only facts the code cannot show.

## Review Focus

1. A `<machine>` with a user part (`owner@zer0-day-nimble`): every user destination, key comment and install record must use the host part only.
2. A rerun on a finished machine: no sudo prompt, no file rewritten, no restart of a running cardinal.
3. Hostile output from cardinal's `cardinal-status`: escape sequences and long strings must not reach the desktop terminal raw, and an unknown `schema` must not crash the fleet view.
4. The live hand setup (transient `cardinal-tmux` unit, tmux default socket, no saved file, hand-made `authorized_keys` line without `from=`): setup needs `--adopt` once and converges.
5. A host whose desktop `known_hosts` entry is stale: the host pass stops with the `ssh-keygen -R` fix instead of a raw ssh error.

---

### Task 1: Units, brief and embedding

**Files:**
- Create: `deploy/cardinal/cardinal.service`, `deploy/cardinal/cardinal-renew.service`, `deploy/cardinal/cardinal-renew.timer`
- Modify: `deploy/cardinal/CLAUDE.md` (placeholders `{{user}}`, `{{machine}}`; `~/state.md` pending-ask rule; the VMs line), `deploy/embed.go`
- Create: `installer/internal/steps/cardinal_files.go`, test `cardinal_files_test.go`

**Interfaces (produces):**
- `deploy.Cardinal embed.FS` holding `cardinal/*`.
- `steps.CardinalUnits(renewHour int) map[string][]byte` keyed by unit file name; the timer's `OnCalendar` is `*-*-* HH,HH+1:00/15` (wrapping at 24).
- `steps.CardinalBrief(user, machine string) []byte`.

- [ ] Tests: the units render for `renew_hour` 4 and 23 (`04,05`, `23,00`); `ExecStart` split by systemd quoting gives tmux the argv in the spec with the `/loop` prompt as one word; `systemd-analyze --user verify` passes on the rendered files (skip when missing); the brief has no `{{` left.
- [ ] Implement, run `go test ./installer/... ./deploy/...`, commit.

### Task 2: Pass documents and the report line

**Files:** Create `installer/internal/steps/cardinal_doc.go`, test.

**Interfaces (produces):**
- `const CardinalMarker = "flocc-cardinal "`
- `type CardinalDoc struct { User, Machine string; RenewHour int; Hosts []CardinalHostFile; Tailnet []string }`, `type CardinalHostFile struct { Name, Addr, KnownHost string }`
- `type CardinalReport struct { Key, Login string; Steps []ReportStep; Notes []string }`, `type ReportStep struct { Name, Detail string; Done, Pending bool }`
- `func ParseCardinalLine(line string) (CardinalReport, bool)`; `func (r CardinalReport) Line() string`
- `type HostDoc struct { Addr, Gateway, Key, Install string; OldKeys []string; Remove bool }`, `type HostReport struct { HostKey string; Installed, KeyLine bool; Notes []string }` with the same marker style (`flocc-cardinal-host `).

- [ ] Round-trip tests for both lines; a line with a different prefix is not parsed.
- [ ] Implement, test, commit.

### Task 3: Root pass (`cardinal-root`) and purge (`cardinal-purge`)

**Files:** Create `installer/internal/steps/cardinal_root.go`, test.

**Interfaces (produces):**
- `func CheckCardinalStage(dir string, sudoUID int) error` (lstat: dir, not symlink, owner, mode 700; `flocc-setup` and `desktop.pub` regular files owned by `sudoUID`; stage name matches `^/tmp/flocc-cardinal\.[A-Za-z0-9]{6,}$`).
- `func CardinalRootSteps(user string, adopt bool) []Step` = Tools, CardinalUser, DesktopKey, Egress.
- `func CardinalPurge(ctx, e, user string) error`.
- `func EgressRules(user string) string` (nft file).

- [ ] Tests (host.Fake, `Env.Root` temp dir): Tools names the missing tool; existing user without adopt refused; privileged group refused with `gpasswd -d` line; `sudo -l -U` rule refused; home mode fixed by chmod on the home path only; desktop key goes through `runuser -u <user> -- /bin/sh -c <fixed> sh <key>`; no root command names a path under the home except the home's own chmod; nft text accepts MagicDNS port 53 before both drops; egress unit reloaded only when the file changed; stage checks refuse symlink, wrong owner, wrong mode; purge order and the pgrep poll.
- [ ] Implement, test, commit.

### Task 4: User pass (`cardinal`) and prestart (`cardinal-prestart`)

**Files:** Create `installer/internal/steps/cardinal_user.go`, test.

**Interfaces (produces):** `func CardinalUserSteps(doc CardinalDoc) []Step` = ClaudeCode, Binaries, HostFiles, Brief, SSHKey, KnownHosts, ClaudeState, HostsReachable, Units; `func CardinalPrestart(e *Env) error`; `func CardinalPubKey(e *Env) string`.

- [ ] Tests: host files rendered with only `ssh`, stale ones removed, dir 700; brief rewritten when edited; key made once with comment `<user>@<host>`, never replaced; known_hosts keeps other lines and replaces the host's; prestart adds onboarding, `~` trust and the `linear` MCP entry, keeps other keys, writes nothing when present; ClaudeState only reads while the unit is active; HostsReachable fails when the tailnet probe connects; Units writes files, enables, runs reset-failed before start, never restarts an active unit, stops `cardinal-tmux` and the default-socket `cardinal` session when present.
- [ ] Implement, test, commit.

### Task 5: Renewal (`cardinal-renew`) and status (`cardinal-status`)

**Files:** Create `installer/internal/steps/cardinal_status.go`, test.

**Interfaces (produces):**
- `func ShouldRenew(active bool, sessionAge, sinceTurn time.Duration) bool`
- `type CardinalStatus struct { Schema int; Unit string; Restarts int; SessionAgeS, LastTurnAgeS *float64; Tmux, Claude bool; Login, Linear string; Hosts map[string]bool; NextRenew, Flocc, ClaudeVersion string; Quick bool }`
- `func (s CardinalStatus) Failures() []string` (with remedies)
- `func CollectCardinalStatus(ctx, e, user string, quick bool) CardinalStatus`

- [ ] Tests: ShouldRenew table; Failures for each failing row; quick skips `claude mcp list` and host ssh.
- [ ] Implement, test, commit.

### Task 6: Host phases (`cardinal-host`, `cardinal-host-remove`)

**Files:** Create `installer/internal/steps/cardinal_host.go`, test.

**Interfaces (produces):** `func CardinalHost(ctx, e, doc HostDoc) (HostReport, error)`; `func CardinalHostRemove(ctx, e, doc HostDoc) (HostReport, error)`; `func KeyLine(gateway, key string) string`; `func MergeAuthorizedKeys(existing []byte, line string, keys []string) []byte`.

- [ ] Tests: drop-in and sysctl written before `apt-get install`; Debian check; host key read in the same run; `ssh-installed` written only when installed now; restart only on drop-in change; `sshd -t` before restart; key matched by blob whatever the comment, other lines kept in order; install record; remove keeps the drop-in without the marker, removes drop-in, sysctl and disables ssh with it and no other record.
- [ ] Implement, test, commit.

### Task 7: Login phase (`cardinal-login`)

**Files:** Create `installer/internal/steps/cardinal_login.go`, test.

**Interfaces:** consumes `answerConsent`, `claudeLogin`, `CardinalPrestart`; produces `func CardinalLogin(ctx, e, o CardinalLoginOptions) error`.

- [ ] Tests: asks to stop an active unit and stops it; no login runs `auth login --claudeai --email`; token-only refused; prestart then consent in `~`; reset-failed then start; waits for `tmux -L <user> has-session`.
- [ ] Implement, test, commit.

### Task 8: flocc-setup dispatch

**Files:** Modify `installer/cmd/flocc-setup/main.go`, create `installer/cmd/flocc-setup/cardinal.go`, test.

- [ ] `checkPhase` accepts the cardinal phases (root: `cardinal-root`, `cardinal-purge`, `cardinal-host`, `cardinal-host-remove`; user: the rest); cardinal phases parse their own flags (`--user`, `--adopt`, `--answers -`, `--email`, `--quick`, `--min-age`) and print their marker line; the root phases run `CheckCardinalStage`; the log sits next to the binary.
- [ ] Tests for phase/uid pairs and flag parsing; implement; commit.

### Task 9: Desktop package `cardinal`

**Files:** Create `installer/internal/cardinal/{saved.go,ssh.go,build.go,incus.go,passes.go,setup.go,commands.go,render.go}` and tests.

**Interfaces (produces):** `func Main(ctx, args []string, d Deps) int` with `Deps{Out, Err io.Writer; Home string; Run host.Runner; TTY bool; Open func(string); RepoRoot func() (string, error)}`; `func Load(dir, user string) (Saved, error)`, `Save`; `func UserDest(machine, user string) ship.Dest`; `func Sanitize(s string) string`; `func RenderStatus(s steps.CardinalStatus) (string, int)`.

- [ ] Tests: saved file round-trip and `removed`; `UserDest("owner@m", "cardinal")` → `cardinal@m` with the pinned options; Incus pass picks address and gateway from an `incus list -f json` fixture and chooses `device set` or `device override`; root pass skipped when outcomes hold and refused without a TTY; key change reruns host and user passes; stale host key error names `ssh-keygen -R`; remove marks `removed`, `--purge` deletes the file; `attach` uses `-r` unless `--write`.
- [ ] Implement, test, commit.

### Task 10: CLI wiring, fleet view and urlWatch

**Files:** Modify `installer/internal/cli/cli.go` (usage, `case "cardinal"`, refuse `-H`), `installer/internal/ship/fleet.go` (CardinalRow, discover, collect, render), `installer/internal/ship/login.go` (allow-list), tests.

- [ ] Tests: `flocc cardinal -H x status` exits 2; fleet with a saved file renders the CARDINAL table, failure lines for a failing row, unreachable machine and unknown schema, strips a hostile fixture, no table without a file or with `removed`; urlWatch with an allow-list does not open `https://evil.example/oauth`.
- [ ] Implement, test, commit.

### Task 11: Docs and release

- [ ] `docs/vm-setup.md`: a cardinal section with the live checklist from the spec; README command list; version bump as earlier releases did.
- [ ] `go vet ./... && go test ./...`; one whole-branch review; PR.
