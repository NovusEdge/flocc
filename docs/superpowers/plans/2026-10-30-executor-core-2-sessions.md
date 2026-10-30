# Executor Core, Plan 2: Sessions, the Run Sequence and Two Users

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every session an unattended `bypassPermissions` run that cannot stop on a dialog or a question, driven by the issue's `Run:` sequence and the four archetype skills, and move the sessions, worktrees and the repo's check to a second Unix user, `flocc-agent`, that cannot reach the service's secrets, ledger or plugin directory.

**Architecture:** Part A (Tasks 1–7) changes how sessions behave and needs no new user: bypass mode, a deny-only PreToolUse guard, a refuse-once Stop hook, a smaller supervisor, `Run:` parsing, `flocc done` for every archetype, and four skills. Part B (Tasks 8–12) splits the host: the service talks to `flocc-agent` only through `sudo` and the fixed-subcommand wrapper `/usr/local/libexec/flocc-agent-run`, which runs `flocc-agent` (new module `flocc.agentrun`); files the agent writes are read through `flocc.agentfs`, which never follows a link; files the agent needs travel in the `start` request; the publisher fetches the branch from the agent's worktree into `refs/flocc/<KEY>` of the service's clone and pushes from there. Part C (Tasks 13–15) is the Go installer: users and sudoers in the root phase, a new `agent` phase run as `flocc-agent`, and the service phase publishing `/opt/flocc`. Every task keeps both test suites green; Part B runs in tests with `agent_run = (python, -m, flocc.agentrun)`, so no sudo is needed.

**Tech Stack:** Python 3.13 stdlib, pytest, ruff, pyrefly; Go 1.27 (installer); POSIX sh (wrapper); sudo, systemd user units, tmux; rootless Podman for checks that start containers; Claude Code 2.1.295 or later (the installer's floor).

**Spec:** `docs/superpowers/specs/2026-10-29-executor-core-design.md` ("Order of work" steps 4–5). Read plan 1 (`docs/superpowers/plans/2026-10-29-executor-core-1-removals.md`) sections "Interfaces plan 1 leaves for plans 2 and 3" and "Where this plan departs from the spec" first.

## Global Constraints

- Work in `/home/novusedge/Projects/Personal/worktrees/flocc-executor-core` on `feat/executor-core`. Each task is one commit (one sub-PR when the owner asks for PRs). Each task ends with its targeted tests green; Task 16 runs `just check` once.
- Commits use `git commit -s`. No `Co-Authored-By` trailer, no AI attribution line.
- Read files with the Read tool, change them with Edit or Write. No `sed -i`, no heredoc or `cat >` into repo files. Deleting files with `git rm` is fine.
- Comments carry only facts the code cannot show. Delete a comment that only made sense for removed code; never append a correction to one. No comment names a plan, a task or a ticket.
- No new dependency (Python or Go).
- Nothing in this plan starts a VM, touches `zer0-day-nimble`, `nimble-dev`, `flocc-nimble` or Linear. Steps that do are listed under "Owner steps" and are not run by implementers.
- Python tests: `uv run pytest -q <paths>`. Go tests: `go test ./<pkg>/...`. Lint a touched Python file with `uv run ruff check <file> && uv run ruff format --check <file>`. `shellcheck` is missing on the desktop: use `uvx --from shellcheck-py shellcheck <file>`.
- Implementers run on Sonnet. A task's implementer sees only that task; the **Interfaces** block of each task is the contract with its neighbours.
- Decisions d171–d180 govern. Do not reintroduce anything d174 removed (escalation tiers, ntfy, flocc's own Linear labels, path claims).
- Ledger phase values stay `test`, `impl`, `fix`, `review`. Archetype names (`tester`, `executor`, `fixer`, `reviewer`) map onto them through `flocc.sequence.PHASE`; branch recovery reads the `Flocc: <KEY> test` trailer, so the trailer word for the tester stays `test`.
- Fixed host layout, used by Python, the wrapper and the installer alike:
  - users `flocc` (service) and `flocc-agent` (sessions); group `flocc-work`, whose only member is `flocc`
  - `/home/flocc-agent` mode `0710` group `flocc-work`; `worktrees/`, `src/`, `state/` under it mode `2750` owner `flocc-agent` group `flocc-work`
  - `/opt/flocc` owner `flocc` mode `0755`: `bin/flocc`, `bin/flocc-agent`, `plugin/`, `deploy/agent-plugins.txt`, `.flocc-commit`
  - `/usr/local/libexec/flocc-agent-run` root-owned `0755`; `/etc/sudoers.d/flocc-agent` root-owned `0440`
  - wrapper argv from the service: `sudo -n -H -u flocc-agent /usr/local/libexec/flocc-agent-run <subcommand> ...`

## Claude Code facts this plan relies on (code.claude.com/docs, read 2026-10-30)

PreToolUse input carries `tool_name`, `tool_input` (`command` for Bash, `file_path` for Edit/Write), `permission_mode`, `cwd`. A hook denies a call by printing, with exit 0:

```json
{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "..."}}
```

Matchers with characters other than letters, digits, `_`, `-`, spaces, `,` and `|` are unanchored JavaScript regexes. Deny rules in `permissions.deny` still block in `bypassPermissions`; allow rules do nothing there. `AskUserQuestion` still prompts in every mode, and bypass mode does not enforce plan-mode blocks, so the guard must deny all three dialog tools.

Stop input adds `stop_hook_active` (true when Claude Code is already continuing because of a stop hook), `last_assistant_message`, `background_tasks` and `session_crons`. A Stop hook keeps the session going by printing, with exit 0:

```json
{"decision": "block", "reason": "..."}
```

Claude Code caps stop-hook continuations at 8 in a row. `StopFailure` runs instead of Stop when a turn ends on an API error; its input has `error` (`rate_limit`, `overloaded`, `authentication_failed`, ..., `unknown`) and `last_assistant_message` (the error text); its output is ignored.

`--permission-mode bypassPermissions` starts bypass mode. The first interactive start shows a warning dialog unless `skipDangerousModePermissionPrompt: true` is set; interactive sessions accept that key from a `--settings` file. Claude Code refuses bypass mode as root or under `sudo` (the pane's `env -i` drops sudo's `SUDO_*` variables). A recognised critical-path `rm` still prompts in bypass mode but the terminal prompt denies on its own after two minutes, so it cannot hold a session.

Managed settings at `/etc/claude-code/managed-settings.json` apply to every user on the machine, sit above user, project, local and `--settings` files, and cannot be overridden by them. Their `permissions.deny` rules block in every mode, `bypassPermissions` included. Hooks defined there run alongside plugin hooks. `allowManagedHooksOnly` would turn the plugin's hooks off, so it is not set. The docs do not say outright that a PreToolUse hook's deny holds in bypass mode, which is why the three dialog tools are also managed deny rules: those are documented to hold.

## Rootless Podman facts this plan relies on (read 2026-10-30)

Testcontainers documents rootless Podman: `DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock` with the user's `podman.socket` unit running, and `TESTCONTAINERS_RYUK_DISABLED=true`, because Ryuk needs a privileged container. Rootless Podman needs `/etc/subuid` and `/etc/subgid` ranges for the user (Debian and Ubuntu's `useradd` assigns them) and `XDG_RUNTIME_DIR` set, which `sudo` and `env -i` drop. Without Ryuk, a container survives a check that is killed before its teardown.

## Where this plan departs from the spec, and why

- **Worktrees are `git worktree`s of a `flocc-agent`-owned clone (`~flocc-agent/src/<repo>`), not separate clones.** They are flocc-agent's files either way; the shared object store keeps disk use where it is today, and `prepare_worktree` keeps its tested logic.
- **The service's clone stays the normal clone in `~flocc/src/<repo>`, not a bare one.** It no longer has worktrees and nothing ever checks out there; fetched agent branches live under `refs/flocc/<KEY>`, so a bare conversion would change nothing but the doctor's clone check.
- **The wrapper has eleven subcommands, not six:** `prepare`, `start`, `send`, `kill`, `check`, `claude` are the spec's (with `claude` covering the login state, the sign-in and the Remote Control consent); `alive`, `capture`, `attach`, `remove` and `probe` are what the service also needs from the agent's tmux server and files (session liveness, `flocc peek`, `flocc attach`, cleanup, the doctor's boundary check). The base-branch check and `./setup` canary run through `check`, because they run the repo's untrusted code too.
- **The agent's GitHub token reaches `flocc-agent` in the `prepare` and `start` requests** (written to `~flocc-agent/.config/flocc/gh-agent.token`, mode 600), not through an installer step. A replaced token then takes effect on the next session with no ship.
- **`flocc peek` and `flocc attach` stay, through the wrapper; `flocc press`, `flocc say` and the idle and stall notices go.** `press` and the stall notice exist to answer dialogs: bypass mode removes the permission dialogs, the guard denies `AskUserQuestion`, `EnterPlanMode` and `ExitPlanMode`, the launcher pre-accepts trust, onboarding and the bypass warning, and the critical-path `rm` prompt denies itself after two minutes. The idle notice is replaced by the Stop hook: a turn ending without `flocc done` is refused once and then ends the run blocked. `say` duplicates Linear comment steering and Remote Control. `peek` stays because the desktop dashboard's issue panel calls it (`installer/internal/dash/output.go:97`).
- **A `StopFailure` hook marks the session idle on any API error but `rate_limit`.** Without it, a turn ended by an API error leaves no marker and the run waits out its wall clock; usage limits are left alone because `autoContinueAtUsageLimit` resumes them.
- **`Run:` is validated for order, not just names:** `executor` exactly once, `tester` only before it, `reviewer` only after it. Anything else is ineligible with the same comment path as an unknown name. Loosening this later costs one set literal.
- **"Needs the owner" blocks the run except in the review-fix round.** d161 sends a review fixer's owner items to the final Linear comment while the PR stays open, and the spec's fixer row allows listing what it could not fix; blocking there would leave a reviewed, open PR stuck in `blocked`.
- **A third setup phase, `--phase agent`, runs as `flocc-agent`** between the root and service phases: Claude Code, its plugins, the git identity, mise and its tools, and the session tmux server are flocc-agent's now. The login phase stays `flocc`'s and reaches Claude through the wrapper's `claude` subcommand, because it needs the tick lock and `flocc status`, which only `flocc` can read.
- **Checks that start containers use rootless Podman as `flocc-agent`, not the Docker daemon.** The `docker` group is root-equivalent and would undo the two-user boundary, so `flocc-agent` may not be in it (the agent account check refuses every group in `PrivilegedGroups`) and doctor fails if it is. With `docker = true` (the host file key keeps its name), the root phase installs Podman and removes `flocc` from the `docker` group, the agent phase starts `flocc-agent`'s `podman.socket`, and `flocc-agent-run` points `DOCKER_HOST` at it for the checks and the panes. An existing host keeps its Docker package and daemon: the installer never stops or restarts `dockerd`, and with nobody in the group it serves no one. The `docker` CLI is not installed on a new host; a repo whose check shells out to `docker` itself needs it added by hand.
- **The guard hook and the dialog-tool deny rules live in root-owned managed settings**, written by the root phase from `deploy/managed-settings.json`. In the plugin, or in flocc-agent's own settings, the agent could turn them off with `disableAllHooks` or by editing the file; managed settings are root's. The plugin keeps its other hooks.

## Interfaces plan 2 leaves for plan 3

| Interface | Where | Used by plan 3 for |
|---|---|---|
| `poller._bad_sequence(rt, issue, exc)`: the only place an unusable `Run:` line is reported | `src/flocc/poller.py` | the `bad-sequence` event |
| `supervisor._block(rt, claim, cause, summary, action)`, `supervisor._end_done(rt, claim, **values)`, `supervisor._timed_out(rt, claim, name)`: every way a run ends | `src/flocc/supervisor.py` | the `ended` event |
| `supervisor.LAST_SUMMARY = "last-summary.md"` in the service state dir, rewritten whenever a non-review step finishes | `src/flocc/supervisor.py` | `flocc run-summary` |
| `Claim.sequence: list[str]` (archetype names), `sequence.parse_sequence`, `sequence.PHASE`, `sequence.ARCHETYPE` | `src/flocc/ledger.py`, `src/flocc/sequence.py` | `flocc status`, `flocc fix` |
| `agent.Agent(cfg)`: `prepare`, `check_worktree`, `check_base`, `remove`, `probe`, `claude_argv()`, `sessions()`; wrapper subcommands in `agentrun.SUBCOMMANDS` and the `case` list of `deploy/flocc-agent-run` | `src/flocc/agent.py`, `src/flocc/agentrun.py`, `deploy/flocc-agent-run` | `flocc cleanup` adds one subcommand to both lists |
| `agentfs.read_text(dir, name)`, `agentfs.exists`, `agentfs.mtime`, `agentfs.entries` | `src/flocc/agentfs.py` | `run-summary` log tails, `cleanup` |
| `poller.remove_issue(rt, claim, keep_dirty=False) -> bool`: worktree, agent state and service state gone (the caller deletes the claim) | `src/flocc/poller.py` | `flocc cleanup` |
| `Runtime.agent` | `src/flocc/runtime.py` | every new command that touches agent files |
| `agentrun.container_env()`; `remove` prunes only stopped containers | `src/flocc/agentrun.py` | `flocc cleanup` removes testcontainers containers still running after their issue ended (Ryuk is off) |

## Review Focus

1. **An agent plants a symlink at `done.json`, `review.md` or `idle` pointing at `~flocc/.config/flocc/linear.key`**: the service must never read through it, so a secret can never reach a PR comment or the issue. Pinned in Task 8 (`test_a_link_planted_anywhere_on_the_path_is_refused`, `test_review_md_that_is_a_link_is_never_posted`).
2. **A host upgraded in place whose `config.toml` still has `worktree_root = "~/worktrees"`, `plugin_dir = "~/flocc/plugin"`, `idle_escalate_s` or a `permissions.toml` beside it**: the tick must keep loading the config and use the fixed two-user paths. Pinned in Task 1 (`test_a_leftover_permissions_file_is_ignored_with_a_warning`), Task 4 (`test_retired_notice_keys_are_ignored`) and Task 8 (`test_layout_keys_in_an_old_config_are_ignored_with_a_warning`).
3. **A turn that ends while the session's own background check or a scheduled wakeup is still pending**: the Stop hook must neither refuse nor write `idle`, or the run blocks mid-work (d167). Pinned in Task 3 (`test_stop_lets_a_session_waiting_on_background_work_stop`, `test_a_scheduled_wakeup_counts_as_background`).
4. **The reviewer writes `review.md` with the Write tool**: the guard must allow that path inside `$FLOCC_STATE` while it denies edits to the worktree; and `gh api -X GET -f q=...` is a read and must pass while `gh api repos/o/r/issues -f title=x` is denied. Pinned in Task 2 (`test_the_reviewer_may_write_only_in_its_state_directory`, `test_gh_api_fields_without_a_method_are_a_post`).
5. **A `Run:` line written loosely (`run: Tester , EXECUTOR`) or twice, or a line `Run: just check` in the description**: the first must parse, the others must skip the issue with exactly one comment, not crash the tick or start the default sequence silently. Pinned in Task 5 (`test_names_are_trimmed_and_case_insensitive`, `test_two_run_lines_are_refused`, `test_an_unknown_archetype_skips_the_issue_with_one_comment`).

## Owner steps (not run by implementers)

1. **Before shipping plan 2 to flocc-nimble:** confirm the host is drained (`flocc status -H flocc-nimble` shows no claims; plan 1 released them). The service phase's "Old worktrees" step refuses to run otherwise.
2. **Ship once:** `flocc ship -H flocc-nimble`. The root phase creates `flocc-agent`, `flocc-work`, the wrapper, the sudoers rule and `/opt/flocc`, moves `~flocc/.claude` and `~flocc/.claude.json` to `flocc-agent` (so the Claude login and Remote Control consent move with them), writes `/etc/claude-code/managed-settings.json`, installs Podman and takes `flocc` out of the `docker` group, and restarts `flocc`'s user manager so it gains `flocc-work`. The agent phase installs Claude Code and the plugins for `flocc-agent`, starts its tmux server and its `podman.socket`. The service phase publishes `/opt/flocc`, removes `~flocc/worktrees` and the old `flocc-tmux.service`, and runs doctor.
3. **If doctor reports the Claude login missing** (a host whose login did not move): `flocc login -H flocc-nimble`.
4. **Check the boundary on the host:** doctor's `agent boundary`, `agent docker`, `managed settings` and `containers` lines are `ok`; `sudo -u flocc-agent cat /home/flocc/.config/flocc/linear.key` fails with "Permission denied"; `sudo -iu flocc-agent podman run --rm docker.io/library/alpine true` exits 0.
5. **One supervised run** on a test issue with `Run: executor, reviewer`, on a repo whose check starts a testcontainers container, watched in the Claude app: the session starts in bypass mode with no dialog, the check's container starts under Podman, the PR opens, the review posts, the worktree goes when the issue is moved to Done. In the same session, ask it through Remote Control to run `git push` and to use `AskUserQuestion`: both must be denied with the guard's reason. Then add `"disableAllHooks": true` to `~flocc-agent/.claude/settings.json`, start another run, and repeat: the managed guard must still deny. Remove the key afterwards. The spec's full live run (cardinal included) is after plan 3.

---

### Task 1: Run sessions in bypassPermissions; remove auto mode and permissions.toml

Sessions switch from `--permission-mode auto` to `bypassPermissions`. `permissions.toml`, the allow rules, the auto-mode environment lines, the judge keys and the installer's Permissions step go (deferred from plan 1). The deny rules stay: they still block in bypass mode.

**Files:**
- Modify: `src/flocc/sessions.py` (`claude_argv`, `trust_folders`)
- Modify: `src/flocc/config.py` (remove `INTERPRETERS` through `_permissions`, four `Config` fields, `load`)
- Delete: `installer/internal/steps/permissions.go`, `installer/internal/steps/permissions_test.go`
- Modify: `installer/internal/steps/list.go`, `installer/internal/answers/answers.go`, `installer/cmd/flocc-setup/main.go:240`
- Test: `tests/test_sessions.py`, `tests/test_config.py`, Go tests that name `Permissions`

**Interfaces:**
- Consumes: nothing new.
- Produces: `claude_argv(...)` unchanged signature; its argv has `--permission-mode bypassPermissions` and its `--settings` JSON has `"permissions": {"deny": [...]}` and `"skipDangerousModePermissionPrompt": true`. `Config` has no `permission_allow`, `permission_environment`, `judge_model`, `judge_max_per_run`. `answers.Answers` has no `Permissions` field.

- [ ] **Step 1: Write the failing tests**

In `tests/test_sessions.py`, add:

```python
def test_sessions_run_in_bypass_mode_without_the_warning_dialog():
    argv = claude_argv(CFG, "NF-1", IssueSettings(), "p", env=STATE, claude_exe="c", exe="x")
    assert argv[argv.index("--permission-mode") + 1] == "bypassPermissions"
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["skipDangerousModePermissionPrompt"] is True
    assert set(settings["permissions"]) == {"deny"} and "autoMode" not in settings


def test_trust_no_longer_writes_the_auto_mode_dialog_key(tmp_path):
    path = tmp_path / ".claude.json"
    trust_folders(path, [tmp_path / "wt"])
    assert "autoModeEnvSetup" not in json.loads(path.read_text())
```

In `tests/test_config.py`, add (it already imports `load` and `EXAMPLE`; add the imports if a name is missing):

```python
def test_a_leftover_permissions_file_is_ignored_with_a_warning(tmp_path, capsys):
    path = tmp_path / "config.toml"
    path.write_text(EXAMPLE.read_text())
    (tmp_path / "permissions.toml").write_text('allow = ["Bash(just:*)"]\n')
    cfg = load(path)
    assert not hasattr(cfg, "permission_allow")
    assert "permissions.toml is no longer read" in capsys.readouterr().err
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_sessions.py tests/test_config.py -k "bypass or auto_mode_dialog or leftover_permissions"`
Expected: FAIL (`auto` is still the mode; `permission_allow` still exists).

- [ ] **Step 3: Change `claude_argv` and `trust_folders`**

In `src/flocc/sessions.py`, replace:

```python
    servers = {s: cfg.mcp_servers[s] for s in settings.mcp}
    permissions: dict = {"deny": list(_DENY), "allow": [*cfg.permission_allow]}
    session_settings: dict = {
        "statusLine": {"type": "command", "command": f"{exe} statusline"},
        "autoContinueAtUsageLimit": True,
        "attribution": False,
        "permissions": permissions,
        "disableWorkflows": True,
    }
    if disabled_plugins:
        session_settings["enabledPlugins"] = {p: False for p in disabled_plugins}
    if cfg.permission_environment:
        # Without $defaults the host's lines would replace the classifier's built-in context.
        session_settings["autoMode"] = {"environment": ["$defaults", *cfg.permission_environment]}
```

with:

```python
    servers = {s: cfg.mcp_servers[s] for s in settings.mcp}
    session_settings: dict = {
        "statusLine": {"type": "command", "command": f"{exe} statusline"},
        "autoContinueAtUsageLimit": True,
        "attribution": False,
        # Deny rules still block in bypassPermissions; allow rules do nothing there.
        "permissions": {"deny": list(_DENY)},
        "disableWorkflows": True,
        # Interactive sessions take the bypass warning's acceptance from a --settings file;
        # without it the first session on a host sits at that dialog.
        "skipDangerousModePermissionPrompt": True,
    }
    if disabled_plugins:
        session_settings["enabledPlugins"] = {p: False for p in disabled_plugins}
```

and replace `"--permission-mode",\n        "auto",` with `"--permission-mode",\n        "bypassPermissions",`.

In `trust_folders`, delete the two comment lines that start `# After five auto-mode denials` and `# mid-turn; \`dismissed\``, and replace:

```python
    env_setup = data.get("autoModeEnvSetup")
    env_setup = env_setup if isinstance(env_setup, dict) else {}
    if (
        not missing
        and data.get("hasCompletedOnboarding") is True
        and env_setup.get("dismissed") is True
    ):
        return
    for p in missing:
        projects.setdefault(p, {})["hasTrustDialogAccepted"] = True
    data["hasCompletedOnboarding"] = True
    data["autoModeEnvSetup"] = {**env_setup, "dismissed": True}
```

with:

```python
    if not missing and data.get("hasCompletedOnboarding") is True:
        return
    for p in missing:
        projects.setdefault(p, {})["hasTrustDialogAccepted"] = True
    data["hasCompletedOnboarding"] = True
```

- [ ] **Step 4: Remove the permissions loader from config**

In `src/flocc/config.py`: delete `INTERPRETERS`, `_BASH_RULE`, `_ENV_ASSIGN`, `_PROGRAM`, `_PERMISSION_KEYS`, `wildcards_an_interpreter` and `_permissions` (and the comments above them); drop `import re` if nothing else uses it. Delete the `Config` fields `permission_allow`, `permission_environment`, `judge_model`, `judge_max_per_run` and their comments. In `load`, replace:

```python
    cfg = Config(repos=repos, **raw, **_permissions(path.with_name("permissions.toml")))
```

with:

```python
    stale = path.with_name("permissions.toml")
    if stale.exists():
        print(
            f"{stale} is no longer read: sessions run in bypassPermissions; delete it",
            file=sys.stderr,
        )
    cfg = Config(repos=repos, **raw)
```

- [ ] **Step 5: Delete the tests of what went**

Run `grep -n -E "permission_allow|permission_environment|judge_|wildcards_an_interpreter|permissions.toml|autoModeEnvSetup|auto_mode" tests/*.py`. Delete every test it lists except the three added in Step 1 (in `tests/test_sessions.py` these include `test_trust_dismisses_the_auto_mode_setup_dialog_and_keeps_its_counts`, `test_the_hosts_rules_and_environment_reach_the_session`, `test_no_rules_leave_auto_mode_alone_and_allow_nothing_extra`). In `test_trust_leaves_an_already_trusted_file_untouched`, drop `autoModeEnvSetup` from the fixture JSON if present.

- [ ] **Step 6: Remove the installer's Permissions step**

```bash
git rm installer/internal/steps/permissions.go installer/internal/steps/permissions_test.go
```

In `installer/internal/steps/list.go`, delete the two comment lines above `ServiceSteps` and change its list to `[]Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Tools{}, Config{}, Run{}}`.

In `installer/internal/answers/answers.go`, delete the `Permissions` type with its comment, the `Permissions *Permissions` field, and the `if o.Permissions != nil { ... }` block in `Overlay`. go-toml ignores unknown tables, so a host file that still has `[permissions]` keeps parsing.

In `installer/cmd/flocc-setup/main.go`, delete `a.Permissions = in.Permissions`.

Run `grep -rn --include='*.go' Permissions installer` and delete the test cases that still construct `answers.Permissions` or call the step.

- [ ] **Step 7: Run the tests**

Run: `uv run pytest -q tests/test_sessions.py tests/test_config.py && uv run pyrefly check && go test ./installer/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests && gofmt -l installer
git add -A src/flocc/sessions.py src/flocc/config.py tests installer
git commit -s -m "Run sessions in bypassPermissions; drop auto mode and permissions.toml"
```

---

### Task 2: The PreToolUse guard hook replaces the pre-commit refusal

`flocc guard` denies the three dialog tools, `git push`, GitHub writes through `gh` (including `gh api` with fields and no method, which `gh` sends as POST), Linear MCP tools, and, for the reviewer, edits outside its state directory and commits. The pre-commit hook's review refusal goes; the hook entry stays a no-op for worktrees whose hooks were written before.

**Files:**
- Create: `src/flocc/guard.py`
- Modify: `src/flocc/cli.py` (`cmd_guard`, `cmd_hook`, `COMMANDS`, parser)
- Modify: `src/flocc/worktree.py` (`_OWN_HOOKS`, `install_hook`)
- Modify: `src/flocc/poller.py` (`install_hook` call)
- Modify: `src/flocc/sessions.py` (`_DENY` comment)
- Modify: `plugin/hooks/hooks.json`
- Test: `tests/test_guard.py` (new), `tests/test_plugin.py::test_hooks`, `tests/test_cli.py`, `tests/test_worktree.py`

**Interfaces:**
- Consumes: env `FLOCC_PHASE` (`test`, `impl`, `fix`, `review`) and `FLOCC_STATE`, as set by `supervisor.launch`.
- Produces: `guard.decide(payload: dict, phase: str, state: str) -> str | None` (deny reason or None); `guard.KEEP_MOVING: str`; `install_hook(worktree, hooks_dir, exe, trailer=True)` (no `config_path`).

- [ ] **Step 1: Write the failing tests**

Create `tests/test_guard.py`:

```python
import io
import json

import pytest

from flocc.cli import main
from flocc.guard import KEEP_MOVING, decide


def bash(command):
    return {"tool_name": "Bash", "tool_input": {"command": command}}


@pytest.mark.parametrize("tool", ["AskUserQuestion", "EnterPlanMode", "ExitPlanMode"])
def test_dialog_tools_are_denied_with_the_way_forward(tool):
    reason = decide({"tool_name": tool, "tool_input": {}}, "impl", "/s")
    assert reason is not None and KEEP_MOVING in reason and "Needs the owner" in reason


@pytest.mark.parametrize(
    "command",
    [
        "git push origin HEAD",
        "cd x && git -C . push",
        "git -c core.x=y push --force",
        "echo ok; git push",
        "gh pr create --fill",
        "gh pr merge 3",
        "gh issue comment 4 -b hi",
        "gh api -X POST repos/o/r/issues",
        "gh api --method=PATCH repos/o/r",
        "gh api repos/o/r/issues -f title=x",
        "gh api repos/o/r/issues --raw-field title=x",
        "gh api graphql -F query=@q.graphql",
        "GH_TOKEN=x gh release create v1",
    ],
)
def test_pushes_and_github_writes_are_denied(command):
    assert decide(bash(command), "impl", "/s") is not None


@pytest.mark.parametrize(
    "command",
    [
        "git status && git log --oneline",
        "git commit -m 'x' -- a.py",
        "gh pr view 3 --json state",
        "gh pr diff 3",
        "gh api repos/o/r/pulls/3",
        "gh api -X GET search/issues -f q=repo:o/r",
        "gh search code foo",
        "just check",
        "echo 'git push is denied'",
    ],
)
def test_reads_and_ordinary_work_pass(command):
    assert decide(bash(command), "impl", "/s") is None


def test_gh_api_fields_without_a_method_are_a_post():
    assert decide(bash("gh api repos/o/r/issues -f title=x"), "impl", "/s") is not None
    assert decide(bash("gh api -X GET repos/o/r/issues -f state=open"), "impl", "/s") is None


def test_linear_mcp_tools_are_denied():
    payload = {"tool_name": "mcp__linear-nimblefox__save_issue", "tool_input": {}}
    assert decide(payload, "impl", "/s") is not None
    assert decide({"tool_name": "mcp__sentry__search", "tool_input": {}}, "impl", "/s") is None


def test_the_reviewer_may_write_only_in_its_state_directory(tmp_path):
    state = str(tmp_path / "state" / "NF-1")
    inside = {"tool_name": "Write", "tool_input": {"file_path": f"{state}/review.md"}}
    outside = {"tool_name": "Edit", "tool_input": {"file_path": str(tmp_path / "wt" / "a.py")}}
    sneaky = {"tool_name": "Write", "tool_input": {"file_path": f"{state}/../../wt/a.py"}}
    assert decide(inside, "review", state) is None
    assert decide(outside, "review", state) is not None
    assert decide(sneaky, "review", state) is not None
    assert decide(outside, "impl", state) is None


def test_the_reviewer_may_not_commit():
    assert decide(bash("git commit -am x"), "review", "/s") is not None
    assert decide(bash("flocc commit -m x -- a.py"), "review", "/s") is not None
    assert decide(bash("git log"), "review", "/s") is None


def test_guard_prints_the_deny_decision(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    payload = {"tool_name": "AskUserQuestion", "tool_input": {"questions": []}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["guard"]) == 0
    out = json.loads(capsys.readouterr().out)["hookSpecificOutput"]
    assert out["hookEventName"] == "PreToolUse" and out["permissionDecision"] == "deny"
    assert KEEP_MOVING in out["permissionDecisionReason"]


def test_guard_prints_nothing_for_an_allowed_call_or_bad_input(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    for raw in (json.dumps(bash("ls")), "not json"):
        monkeypatch.setattr("sys.stdin", io.StringIO(raw))
        assert main(["guard"]) == 0
        assert capsys.readouterr().out == ""
```

In `tests/test_plugin.py::test_hooks`, replace:

```python
    assert [h["command"] for e in hooks["PreToolUse"] for h in e["hooks"]] == [clear]
```

with:

```python
    (clearing, guarding) = hooks["PreToolUse"]
    assert "matcher" not in clearing and [h["command"] for h in clearing["hooks"]] == [clear]
    assert [h["command"] for h in guarding["hooks"]] == ["flocc guard"]
    for tool in ("AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "Bash", "Edit", "Write"):
        assert re.search(guarding["matcher"], tool)
    assert re.search(guarding["matcher"], "mcp__linear__save_issue")
    assert not re.search(guarding["matcher"], "Read")
```

and add `import re` at the top of `tests/test_plugin.py`.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_guard.py tests/test_plugin.py::test_hooks`
Expected: FAIL with `ModuleNotFoundError: No module named 'flocc.guard'`.

- [ ] **Step 3: Write `src/flocc/guard.py`**

```python
import os
import re
import shlex

KEEP_MOVING = (
    "Nobody answers this session. Take the spec's default and carry on, or write what needs the"
    ' owner under a line that starts with "Needs the owner" in your summary and run flocc done.'
)
_DIALOGS = frozenset({"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"})
_EDITS = frozenset({"Edit", "Write", "NotebookEdit"})
_LINEAR = re.compile(r"^mcp__.*linear", re.IGNORECASE)
# Command separators, subshells and backticks: each piece is judged as its own command.
_SEGMENTS = re.compile(r"&&|\|\||[;|&\n()`]|\$\(")
_ASSIGN = re.compile(r"^[A-Za-z_]\w*=")
_GIT_VALUE_OPTS = frozenset({"-C", "-c", "--git-dir", "--work-tree", "--namespace"})
_GH_READS = frozenset({"list", "view", "status", "diff", "checks"})
_GH_FIELDS = ("-f", "-F", "--field", "--raw-field", "--input")


def _words(segment: str) -> list[str]:
    try:
        words = shlex.split(segment)
    except ValueError:
        words = segment.split()
    while words and _ASSIGN.match(words[0]):
        words.pop(0)
    return words


def _git_subcommand(args: list[str]) -> str | None:
    it = iter(args)
    for arg in it:
        if arg in _GIT_VALUE_OPTS:
            next(it, None)
        elif not arg.startswith("-"):
            return arg
    return None


def _gh_api_writes(args: list[str]) -> bool:
    method = None
    fields = False
    for i, arg in enumerate(args):
        if arg in ("-X", "--method") and i + 1 < len(args):
            method = args[i + 1]
        elif arg.startswith("--method="):
            method = arg.split("=", 1)[1]
        elif arg.startswith("-X") and len(arg) > 2:
            method = arg[2:]
        elif arg in _GH_FIELDS or arg.startswith(("--field=", "--raw-field=", "--input=")):
            fields = True
        elif len(arg) > 2 and arg[:2] in ("-f", "-F"):
            fields = True
    # gh api sends POST when it has fields and no method.
    return (method is not None and method.upper() != "GET") or (fields and method is None)


def _gh_writes(args: list[str]) -> bool:
    if not args:
        return False
    group = args[0]
    if group == "api":
        return _gh_api_writes(args[1:])
    if group in ("search", "status", "browse", "help", "version"):
        return False
    if group == "auth":
        return args[1:2] != ["status"]
    verb = next((a for a in args[1:] if not a.startswith("-")), None)
    return verb not in _GH_READS


def _bash(command: str, phase: str) -> str | None:
    for segment in _SEGMENTS.split(command):
        words = _words(segment)
        if not words:
            continue
        program, args = os.path.basename(words[0]), words[1:]
        if program == "git":
            sub = _git_subcommand(args)
            if sub == "push":
                return "Never push: the service pushes your commits after flocc done."
            if sub == "commit" and phase == "review":
                return "The reviewer changes nothing: write your findings in review.md."
        elif program == "flocc" and args[:1] == ["commit"] and phase == "review":
            return "The reviewer changes nothing: write your findings in review.md."
        elif program == "gh" and _gh_writes(args):
            return (
                "gh may only read here: the service opens and comments on PRs with its own token."
            )
    return None


def _inside(path: str, state: str) -> bool:
    if not path or not state:
        return False
    # Lexical, so ../ cannot climb out; a link inside the state directory only fools the guard,
    # which is a convenience and not a boundary.
    full, root = os.path.normpath(os.path.abspath(path)), os.path.normpath(state)
    return full == root or full.startswith(root + os.sep)


def decide(payload: dict, phase: str, state: str) -> str | None:
    """The reason to deny this tool call, or None to let it run."""
    tool = payload.get("tool_name")
    tool_input = payload.get("tool_input") or {}
    if not isinstance(tool, str) or not isinstance(tool_input, dict):
        return None
    if tool in _DIALOGS:
        return f"{tool} is not available in this session. {KEEP_MOVING}"
    if _LINEAR.match(tool):
        return "Linear is the service's: put what the owner should read in your summary."
    if tool in _EDITS and phase == "review":
        path = tool_input.get("file_path") or tool_input.get("notebook_path") or ""
        if not _inside(str(path), state):
            return "The reviewer changes nothing in the repo; only review.md in your state directory."
    if tool == "Bash" and isinstance(tool_input.get("command"), str):
        return _bash(tool_input["command"], phase)
    return None
```

- [ ] **Step 4: Add `flocc guard` and retire the pre-commit refusal**

In `src/flocc/cli.py`, add after `cmd_tests_amended`:

```python
def cmd_guard(args) -> int:
    from flocc.guard import decide

    # Exit 0 always: a guard that fails must not stop the session, and it is not the boundary.
    try:
        reason = decide(
            _hook_input(strict=True),
            os.environ.get("FLOCC_PHASE", ""),
            os.environ.get("FLOCC_STATE", ""),
        )
    except Exception as exc:  # noqa: BLE001
        print(f"flocc guard could not judge the call: {exc}", file=sys.stderr)
        return 0
    if reason:
        decision = {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
        print(json.dumps({"hookSpecificOutput": decision}))
    return 0
```

Replace the body of `cmd_hook` with:

```python
def cmd_hook(args) -> int:
    if args.name == "commit-msg":
        return _commit_msg(args.message_file, not args.no_trailer)
    # Worktrees prepared by 0.12 still call pre-commit; it no longer checks anything.
    return 0
```

Add `"guard": cmd_guard,` to `COMMANDS` and, in `build_parser` after the `tests-amended` parser, `add("guard", "Hook (PreToolUse): deny dialogs, pushes and GitHub writes")`.

- [ ] **Step 5: Stop generating the pre-commit hook**

In `src/flocc/worktree.py`, change `_OWN_HOOKS = ("pre-commit", "commit-msg")` to `_OWN_HOOKS = ("commit-msg",)`, change the signature to `def install_hook(worktree: Path, hooks_dir: Path, exe: str, trailer: bool = True) -> None:`, and delete:

```python
    hook = hooks_dir / "pre-commit"
    hook.write_text(
        "#!/bin/sh\nset -e\n"
        f'FLOCC_CONFIG="{config_path}" "{exe}" hook pre-commit\n'
        f'if [ -x "{original}/pre-commit" ]; then exec "{original}/pre-commit" "$@"; fi\n'
    )
    hook.chmod(0o755)
```

The repo's own `pre-commit` now gets a stub like its other hooks. In `src/flocc/poller.py`, remove the `rt.config_path,` argument from the `install_hook(...)` call. In `src/flocc/sessions.py`, change the `_DENY` comment's first line to `# --no-verify skips the commit-msg trailer and the repo's own hooks. Flag order varies, so the`.

In `tests/test_worktree.py`, drop the `config_path` argument from every `install_hook` call; in tests that asserted a generated `pre-commit`, assert instead that a repo `pre-commit` hook gets a stub that execs it. In `tests/test_cli.py`, delete `test_commit_in_a_review_worktree_is_rejected` and `test_commit_hook_allows_any_file_outside_the_review`, and add:

```python
def test_pre_commit_from_an_old_worktree_is_a_no_op():
    assert main(["hook", "pre-commit"]) == 0
```

- [ ] **Step 6: Register the hook**

Replace the `"PreToolUse"` entry in `plugin/hooks/hooks.json` with:

```json
    "PreToolUse": [
      {
        "hooks": [ { "type": "command", "command": "[ -z \"$FLOCC_STATE\" ] || rm -f \"$FLOCC_STATE/idle\"" } ]
      },
      {
        "matcher": "^(AskUserQuestion|EnterPlanMode|ExitPlanMode|Bash|Edit|Write|NotebookEdit|mcp__.*)$",
        "hooks": [ { "type": "command", "command": "flocc guard" } ]
      }
    ],
```

The matcher keeps a Python start off every Read, Grep and Glob call. The guard entry is here only until Task 13 moves it into root-owned managed settings, where flocc-agent cannot turn it off.

- [ ] **Step 7: Run the tests**

Run: `uv run pytest -q tests/test_guard.py tests/test_plugin.py tests/test_cli.py tests/test_worktree.py tests/test_poller.py && uv run pyrefly check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add src/flocc/guard.py src/flocc/cli.py src/flocc/worktree.py src/flocc/poller.py src/flocc/sessions.py plugin/hooks/hooks.json tests
git commit -s -m "Add the PreToolUse guard; retire the pre-commit review refusal"
```

---

### Task 3: The Stop hook refuses once, then marks the session idle

`flocc stop` refuses the first stop of a turn when `done.json` is missing and nothing runs in the background; a second stop in the same continuation (`stop_hook_active`) goes through and writes `idle`. `flocc stop --failure` (StopFailure) writes `idle` for API errors other than usage limits. The old `flocc idle` command goes.

**Files:**
- Modify: `src/flocc/cli.py` (`cmd_stop`, delete `cmd_idle`, parser)
- Modify: `src/flocc/idle.py` (`note_background`)
- Modify: `plugin/hooks/hooks.json`
- Test: `tests/test_cli.py`, `tests/test_idle.py`, `tests/test_plugin.py::test_hooks`

**Interfaces:**
- Consumes: `idle.note_idle(state, payload, now)`, `idle.note_background(state, payload)`, `idle.has_background(state)` (plan 1).
- Produces: the marker `$FLOCC_STATE/idle` = `{"at": float, "message": str}` now means "the session stopped twice without `flocc done`" (or an API error ended its turn). `cli.STOP_REASON: str`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_cli.py`, delete `test_idle_records_the_last_message`, `test_idle_falls_back_to_the_transcript` and `test_idle_bookkeeping_failure_exits_1_never_2`, and add:

```python
def stop(monkeypatch, payload, *flags):
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    return main(["stop", *flags])


def test_stop_refuses_once_then_marks_the_session_idle(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert stop(monkeypatch, {"stop_hook_active": False, "last_assistant_message": "a?"}) == 0
    refusal = json.loads(capsys.readouterr().out)
    assert refusal["decision"] == "block" and "flocc done" in refusal["reason"]
    assert "Needs the owner" in refusal["reason"] and not (tmp_path / "idle").exists()
    assert stop(monkeypatch, {"stop_hook_active": True, "last_assistant_message": "x" * 700}) == 0
    assert capsys.readouterr().out == ""
    marker = json.loads((tmp_path / "idle").read_text())
    assert marker["message"] == "x" * 600 and marker["at"] > 0


def test_stop_falls_back_to_the_transcript_for_the_idle_message(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "impl")
    transcript = tmp_path / "t.jsonl"
    text = {"type": "text", "text": "please run it for me"}
    transcript.write_text(json.dumps({"type": "assistant", "message": {"content": [text]}}))
    assert stop(monkeypatch, {"stop_hook_active": True, "transcript_path": str(transcript)}) == 0
    assert json.loads((tmp_path / "idle").read_text())["message"] == "please run it for me"


def test_stop_lets_a_finished_run_stop(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    (tmp_path / "done.json").write_text('{"summary": "ok"}')
    assert stop(monkeypatch, {"stop_hook_active": False}) == 0
    assert capsys.readouterr().out == "" and not (tmp_path / "idle").exists()


def test_stop_lets_a_session_waiting_on_background_work_stop(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    payload = {"stop_hook_active": False, "background_tasks": [{"id": "1", "type": "shell"}]}
    assert stop(monkeypatch, payload) == 0
    assert capsys.readouterr().out == "" and not (tmp_path / "idle").exists()
    assert json.loads((tmp_path / "background").read_text()) == ["shell"]


def test_a_scheduled_wakeup_counts_as_background(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    payload = {"stop_hook_active": True, "background_tasks": [], "session_crons": [{"id": "c"}]}
    assert stop(monkeypatch, payload) == 0
    assert capsys.readouterr().out == "" and not (tmp_path / "idle").exists()


def test_stop_failure_marks_idle_except_for_a_usage_limit(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "impl")
    assert stop(monkeypatch, {"error": "rate_limit"}, "--failure") == 0
    assert not (tmp_path / "idle").exists()
    payload = {"error": "server_error", "last_assistant_message": "API Error: 500"}
    assert stop(monkeypatch, payload, "--failure") == 0
    assert json.loads((tmp_path / "idle").read_text())["message"] == "API Error: 500"


def test_stop_bookkeeping_failure_exits_1_never_2(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("flocc.idle.note_idle", boom)
    assert stop(monkeypatch, {"stop_hook_active": True}) == 1
    assert "disk gone" in capsys.readouterr().err
```

In `tests/test_plugin.py::test_hooks`, change the first assert to `assert set(hooks) == {"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop", "StopFailure"}` and add `assert [h["command"] for h in hooks["StopFailure"][0]["hooks"]] == ["flocc stop --failure"]`.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_cli.py -k stop tests/test_plugin.py::test_hooks`
Expected: FAIL (no refusal is printed; `--failure` is an unknown argument).

- [ ] **Step 3: Rewrite `cmd_stop`, delete `cmd_idle`**

In `src/flocc/cli.py`, delete `cmd_idle` and replace `cmd_stop` with:

```python
STOP_REASON = (
    "flocc done has not run, so this run is not finished. Finish the work and run flocc done,"
    ' or write what needs the owner under a line that starts with "Needs the owner" in your'
    " summary and run flocc done. Nobody answers questions in this session."
)


def cmd_stop(args) -> int:
    # Exit 2 on Stop would block the stop; bookkeeping failures exit 1 so they never do.
    try:
        state = _state_dir()
        payload = _hook_input()
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not read its input: {exc}", file=sys.stderr)
        return 1
    if args.failure:
        # autoContinueAtUsageLimit resumes the turn after the reset.
        if payload.get("error") == "rate_limit":
            return 0
        message = payload.get("last_assistant_message") or str(payload.get("error", ""))
        payload = {"last_assistant_message": message}
    else:
        failed = 0
        try:
            idle.note_background(state, payload)
        except Exception as exc:  # noqa: BLE001
            print(f"flocc stop could not record the background tasks: {exc}", file=sys.stderr)
            failed = 1
        # Background work or a scheduled wakeup brings the session back on its own.
        if (state / "done.json").exists() or idle.has_background(state):
            return failed
        if not payload.get("stop_hook_active"):
            print(json.dumps({"decision": "block", "reason": STOP_REASON}))
            return failed
    try:
        idle.note_idle(state, payload, time.time())
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the stop: {exc}", file=sys.stderr)
        return 1
    return 0
```

Delete `"idle": cmd_idle,` from `COMMANDS` and the `add("idle", ...)` parser line. Replace the `stop` parser line with:

```python
    stop = add("stop", "Hook (Stop, StopFailure): refuse one early stop, then record the idle stop")
    stop.add_argument("--failure", action="store_true", help="the turn ended on an API error")
```

- [ ] **Step 4: Count scheduled wakeups as background**

In `src/flocc/idle.py`, replace the body of `note_background` from `kinds = [` to the end of that statement with:

```python
    kinds = [
        str(t.get("type", "")) for t in payload.get("background_tasks") or [] if isinstance(t, dict)
    ]
    kinds += ["cron" for c in payload.get("session_crons") or [] if isinstance(c, dict)]
```

and change its docstring to `"""Records what will wake the session again; a stop with any of it is not idle."""`. In `tests/test_idle.py`, add:

```python
def test_a_scheduled_wakeup_is_recorded_as_background(tmp_path):
    note_background(tmp_path, {"background_tasks": [], "session_crons": [{"id": "c"}]})
    assert has_background(tmp_path)
```

(import `note_background`, `has_background` from `flocc.idle` if the file does not already).

- [ ] **Step 5: Register StopFailure**

In `plugin/hooks/hooks.json`, after the `"Stop"` entry, add:

```json
    "StopFailure": [
      { "hooks": [ { "type": "command", "command": "flocc stop --failure" } ] }
    ]
```

(add the comma after the `Stop` entry's closing `]`).

- [ ] **Step 6: Run the tests**

Run: `uv run pytest -q tests/test_cli.py tests/test_idle.py tests/test_plugin.py && uv run pyrefly check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add src/flocc/cli.py src/flocc/idle.py plugin/hooks/hooks.json tests
git commit -s -m "Refuse one early stop, then mark the session idle; mark API-error stops"
```

---

### Task 4: A smaller supervisor

The idle marker now ends the run blocked on the next tick. The idle and stall notices, `flocc press`, `flocc say`, `idle_escalate_s` and `stall_escalate_s` go: with bypass mode, the guard and the Stop hook, nothing is left for them to catch (see "Where this plan departs"). A crash still gets one restart, now with a note telling the new session where it stands, and every finished step leaves its summary in `last-summary.md` for the next one.

**Files:**
- Modify: `src/flocc/supervisor.py`
- Modify: `src/flocc/idle.py` (`clear_idle`)
- Modify: `src/flocc/config.py`, `deploy/config.example.toml`
- Modify: `src/flocc/context.py` (`_COMMANDS`), `src/flocc/cli.py` (`cmd_context`, press/say)
- Modify: `src/flocc/sessions.py` (`Sessions.press`), `tests/fakes.py`
- Modify: `installer/internal/cli/cli.go:48-50`
- Test: `tests/test_supervisor.py`, `tests/test_cli.py`, `tests/test_config.py`, `tests/test_context.py`

**Interfaces:**
- Consumes: Task 3's marker meaning.
- Produces: `supervisor.LAST_SUMMARY = "last-summary.md"`; `supervisor._timed_out(rt, claim, name) -> None`; `supervisor._RESTART: str`. `idle.clear_idle(state)` removes only `idle`. No `cli.cmd_press`, `cli.cmd_say`, `Sessions.press`, `FakeSessions.press`, `Config.idle_escalate_s`, `Config.stall_escalate_s`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_supervisor.py`, delete every test that exercises `_check_idle` or `_check_stall` (run `grep -n -E "def test_.*(idle|stall|pane|press)" tests/test_supervisor.py`; delete each listed test) and the `IDLE_S` constant, then add:

```python
def test_an_idle_marker_ends_the_run_blocked(rt):
    add(rt, phase="impl")
    rt.sessions.alive_names.add("NF-1")
    d = state_dir(rt.cfg, "NF-1")
    (d / "idle").write_text(json.dumps({"at": 50.0, "message": "Which option do you want?"}))
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and rt.sessions.killed == ["NF-1"]
    (notice,) = rt.notifier.sent
    assert notice.cause == "NF-1:idle" and "Which option do you want?" in notice.summary


def test_an_idle_reviewer_skips_the_review_without_blocking(rt):
    add(rt, phase="review")
    rt.sessions.alive_names.add("NF-1-review")
    (state_dir(rt.cfg, "NF-1") / "idle").write_text(json.dumps({"at": 50.0, "message": ""}))
    supervise(rt, current(rt))
    assert current(rt).state == "done"
    assert any("review skipped" in line for _, _, line in ledger.pending_digest(rt.con))


def test_a_crash_restarts_once_with_a_note_then_blocks(rt):
    add(rt, phase="impl")
    supervise(rt, current(rt))
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and current(rt).restarts == 1
    assert any("ended before flocc done" in a for a in argv)
    rt.sessions.alive_names.discard("NF-1")
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and rt.notifier.sent[-1].cause == "NF-1:crash"


def test_a_finished_step_leaves_its_summary_for_the_next(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    done(rt, "tests for A and B")
    supervise(rt, current(rt))
    assert (state_dir(rt.cfg, "NF-1") / "last-summary.md").read_text() == "tests for A and B"
```

In `tests/test_cli.py`, delete the `press` and `say` tests (`grep -n -E "def test_(press|say)" tests/test_cli.py`). In `tests/test_config.py`, add:

```python
def test_retired_notice_keys_are_ignored(tmp_path, capsys):
    path = tmp_path / "config.toml"
    path.write_text("idle_escalate_s = 60.0\nstall_escalate_s = 60.0\n" + EXAMPLE.read_text())
    load(path)
    err = capsys.readouterr().err
    assert "idle_escalate_s is no longer used" in err and "stall_escalate_s" in err
```

In `tests/test_context.py::test_context_cli_prints_context_and_handoff`, rename it `test_context_cli_prints_context_and_the_last_summary`, write `last-summary.md` instead of `handoff.md`, and assert the output holds both the context and `## The previous step's summary`.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_supervisor.py tests/test_config.py tests/test_context.py -k "idle or crash or summary or retired_notice"`
Expected: FAIL.

- [ ] **Step 3: Shrink the supervisor**

In `src/flocc/supervisor.py`:
- Delete `PANE`, `STALL_ESCALATED`, `STALL_LINES`, `STALL_TAIL`, `_check_idle`, `_check_stall`, and the `has_background` import. Add below `OWNER_ITEMS = "owner-items.md"`:

```python
LAST_SUMMARY = "last-summary.md"
_RESTART = (
    "Your previous session for this step ended before flocc done. The branch holds what it"
    " committed; check git log and git status, then carry on from there."
)
```

- In `launch`, change `for leftover in (*_MARKERS, "status.json", PANE, STALL_ESCALATED, BACKGROUND):` to `for leftover in (*_MARKERS, "status.json", BACKGROUND):`.
- Replace `_relaunch` with:

```python
def _relaunch(rt, claim: Claim) -> None:
    # The check output reaches a fix session only through its prompt; a restart needs it again.
    path = state_dir(rt.cfg, claim.issue_key) / FIX_CONTEXT
    extra = path.read_text() if claim.phase == "fix" and path.exists() else ""
    launch(rt, claim, claim.phase, extra=f"{_RESTART}\n\n{extra}".strip())
```

- Add above `supervise`:

```python
def _idle_message(d: Path) -> str | None:
    """The last words of a session that stopped twice without flocc done; None when it did not."""
    try:
        raw = json.loads((d / "idle").read_text())
    except FileNotFoundError:
        return None
    except (OSError, ValueError):
        return ""
    return str(raw.get("message", "")) if isinstance(raw, dict) else ""


def _stopped(rt, claim: Claim, name: str, message: str) -> None:
    if claim.phase == "review":
        _review_skipped(rt, claim, f"the reviewer stopped without flocc done ({name})")
        return
    _block(
        rt,
        claim,
        "idle",
        f"{name} stopped twice without running flocc done. Its last words:\n"
        f"{message or '(none)'}"[:3000],
        "Reply on this issue with what it needs, then move the issue back to Todo; the rerun"
        " continues on the kept branch",
    )


def _timed_out(rt, claim: Claim, name: str) -> None:
    key = claim.issue_key
    rt.sessions.kill(name)
    line = f"stopped at the wall-clock limit ({name}); rerun with flocc release {key}"
    ledger.add_digest(rt.con, key, line, rt.now())
    _record_usage(rt, key, claim.phase, "end")
    ledger.update_claim(rt.con, key, state="timed_out")
```

- Replace `supervise` with:

```python
def supervise(rt, claim: Claim) -> None:
    key = claim.issue_key
    d = state_dir(rt.cfg, key)
    name = session_name(key, claim.phase)
    done = d / "done.json"
    if done.exists():
        try:
            summary = json.loads(done.read_text()).get("summary", "")
        except ValueError:  # still being written; the next tick reads it whole
            return
        rt.sessions.kill(name)
        if claim.phase != "review":
            (d / LAST_SUMMARY).write_text(summary)
        _finish_phase(rt, claim, summary)
        return
    if (message := _idle_message(d)) is not None:
        _stopped(rt, claim, name, message)
        return
    if not rt.sessions.alive(name):
        if claim.restarts >= 1 and claim.phase == "review":
            _review_skipped(rt, claim, f"the session died twice ({name})")
            return
        if claim.restarts >= 1:
            _block(
                rt,
                claim,
                "crash",
                "The session died twice.",
                f"Check the worktree, then run flocc release --keep-worktree {key}",
            )
            return
        _relaunch(rt, claim)
        ledger.update_claim(rt.con, key, restarts=claim.restarts + 1)
        return
    limit = rt.cfg.review_wall_clock_s if claim.phase == "review" else claim.wall_clock_s
    if rt.now() - claim.started_at > limit:
        if claim.phase == "review":
            _record_usage(rt, key, claim.phase, "end")
            _review_skipped(rt, claim, f"stopped at the wall-clock limit ({name})")
            return
        _timed_out(rt, claim, name)
```

In `src/flocc/idle.py`, make `clear_idle` remove only `idle`:

```python
def clear_idle(state: Path) -> None:
    (state / "idle").unlink(missing_ok=True)
```

and in `tests/test_idle.py` rename `test_clear_idle_removes_the_marker_and_its_notice_mark` to `test_clear_idle_removes_the_marker` and drop its `idle-escalated` lines.

- [ ] **Step 4: Remove the notice keys and the dialog commands**

In `src/flocc/config.py`: delete the fields `idle_escalate_s` and `stall_escalate_s`; change `_RETIRED = ("ntfy_url", "context_restart_pct")` to `_RETIRED = ("ntfy_url", "context_restart_pct", "idle_escalate_s", "stall_escalate_s")`. In `deploy/config.example.toml`, delete the two comment pairs for `idle_escalate_s` and `stall_escalate_s`.

In `src/flocc/cli.py`, delete `PRESS_KEYS` with its comment, `cmd_press`, `cmd_say`, their `COMMANDS` entries and their parsers; change `cmd_attach`'s hint to `"read-only · Ctrl-b d detaches, the session keeps running"`. Replace `cmd_context` with:

```python
def cmd_context(args) -> int:
    state = _state_dir()
    context = state / "context.md"
    if context.exists():
        print(context.read_text())
    last = state / "last-summary.md"
    if last.exists():
        print(f"## The previous step's summary\n\n{last.read_text()}")
    return 0
```

In `src/flocc/sessions.py`, delete `Sessions.press`; in `tests/fakes.py`, delete `FakeSessions.press`. In `src/flocc/context.py`, in `write_context` replace `"Your state directory is {d}; write handoff.md there.\n\n"` with `"Your state directory is {d}.\n\n"`. In `src/flocc/poller.py::_start`, delete the two lines that move `handoff.md` to `handoff.prev.md` and their comment, and change `for stale in ("scratchpads", "idle", "idle-escalated"):` to `for stale in ("scratchpads", "idle", LAST_SUMMARY):` (import `LAST_SUMMARY` from `flocc.supervisor`); in `tests/test_poller.py::test_rerun_on_a_branch_with_the_tests_resumes_at_the_implementer`, delete the `handoff.md` and `handoff.prev.md` lines.

In `installer/internal/cli/cli.go`, delete the `flocc press` and `flocc say` help lines (lines 49–50).

- [ ] **Step 5: Run the tests**

Run: `uv run pytest -q tests/test_supervisor.py tests/test_cli.py tests/test_config.py tests/test_context.py tests/test_idle.py tests/test_poller.py && uv run pyrefly check && go test ./installer/internal/cli/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add -A src tests deploy/config.example.toml installer/internal/cli/cli.go
git commit -s -m "Smaller supervisor: idle ends the run blocked; drop press, say and the notices"
```

---

### Task 5: The `Run:` sequence and the optional tester guard

A `Run:` line in the description picks the archetypes; the default is `executor, reviewer`. The claim records the sequence; a run starts at the tester only when the sequence has one and the branch has no remembered tests, and the reviewer runs only when listed. With no tester the claim's `tester_sha` stays `None` and the publisher skips the tester-file guard.

**Files:**
- Create: `src/flocc/sequence.py`
- Modify: `src/flocc/ledger.py` (migration 4, `Claim.sequence`, `_JSON_FIELDS`)
- Modify: `src/flocc/poller.py` (`poll`, `_start`, `_bad_sequence`)
- Modify: `src/flocc/supervisor.py` (`_finish_phase`, `_end_done`)
- Modify: `src/flocc/publisher.py` (`publish`, `_tests_guard`)
- Test: `tests/test_sequence.py` (new), `tests/test_ledger.py`, `tests/test_poller.py`, `tests/test_supervisor.py`, `tests/test_publisher.py`

**Interfaces:**
- Consumes: `agentblock.has_acceptance_criteria` (unchanged).
- Produces: `sequence.parse_sequence(description: str | None) -> tuple[str, ...]`; `sequence.SequenceError(ValueError)`; `sequence.DEFAULT = ("executor", "reviewer")`; `sequence.PHASE: dict[str, str]` (`tester→test`, `executor→impl`, `fixer→fix`, `reviewer→review`); `sequence.ARCHETYPE: dict[str, str]` (the inverse). `Claim.sequence: list[str]` (default `["executor", "reviewer"]`). `poller._bad_sequence(rt, issue, exc) -> None`. `supervisor._end_done(rt, claim, **values) -> None`.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_sequence.py`:

```python
import pytest

from flocc.sequence import ARCHETYPE, DEFAULT, PHASE, SequenceError, parse_sequence

CRITERIA = "\n## Acceptance criteria\n\n- works\n"


def test_no_run_line_is_the_default():
    assert parse_sequence("Do it." + CRITERIA) == DEFAULT == ("executor", "reviewer")
    assert parse_sequence(None) == DEFAULT


@pytest.mark.parametrize(
    "line, want",
    [
        ("Run: tester, executor, reviewer", ("tester", "executor", "reviewer")),
        ("Run: executor", ("executor",)),
        ("Run: tester, executor", ("tester", "executor")),
    ],
)
def test_a_run_line_overrides_it(line, want):
    assert parse_sequence(f"Do it.\n\n{line}\n{CRITERIA}") == want


def test_names_are_trimmed_and_case_insensitive():
    assert parse_sequence("run: Tester , EXECUTOR\r\n") == ("tester", "executor")


@pytest.mark.parametrize(
    "line, words",
    [
        ("Run: executor, planner", "unknown archetype: planner"),
        ("Run: just check", "unknown archetype: just check"),
        ("Run: executor, fixer", "never lists fixer"),
        ("Run: executor, executor", "twice"),
        ("Run: reviewer, executor", "order"),
        ("Run: tester", "order"),
        ("Run: executor,", "empty"),
    ],
)
def test_a_bad_run_line_is_refused(line, words):
    with pytest.raises(SequenceError, match=words):
        parse_sequence(line)


def test_two_run_lines_are_refused():
    with pytest.raises(SequenceError, match="more than one"):
        parse_sequence("Run: executor\nRun: tester, executor\n")


def test_archetypes_map_onto_the_ledger_phases():
    assert PHASE == {"tester": "test", "executor": "impl", "fixer": "fix", "reviewer": "review"}
    assert ARCHETYPE["impl"] == "executor"
```

In `tests/test_ledger.py`, add:

```python
def test_a_claim_keeps_its_sequence(con):
    c = make()
    c.sequence = ["tester", "executor"]
    ledger.insert_claim(con, c)
    assert ledger.get_claim(con, "NF-1").sequence == ["tester", "executor"]


def test_an_old_claim_gets_the_default_sequence(tmp_path):
    path = tmp_path / "l.sqlite"
    raw = sqlite3.connect(path)
    raw.executescript(ledger._SCHEMA)
    raw.execute(
        "INSERT INTO claims (issue_key, issue_id, repo, worktree, branch, phase, state, paths,"
        " depends_on, settings_json, started_at, wall_clock_s) VALUES ('NF-1', 'i', 'backend',"
        " '/wt/NF-1', 'agent/nf-1', 'impl', 'done', '[]', '[]', '{}', 0, 3600)"
    )
    raw.commit()
    raw.close()
    assert ledger.get_claim(ledger.connect(path), "NF-1").sequence == ["executor", "reviewer"]
```

(add `import sqlite3` to `tests/test_ledger.py` if it is not there).

In `tests/test_poller.py`, change `GOOD` to `"Do it.\n\nRun: tester, executor, reviewer\n\n## Acceptance criteria\n\n- works\n"` (so the existing tests keep starting at the tester) and add:

```python
PLAIN = "Do it.\n\n## Acceptance criteria\n\n- works\n"


def test_the_default_sequence_starts_at_the_executor_with_no_tester(rt):
    rt.linear.ready = [issue(desc=PLAIN)]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.sequence == ["executor", "reviewer"]
    assert (c.phase, c.tester_sha) == ("impl", None) and rt.sessions.started[0][0] == "NF-9"


def test_a_run_line_is_recorded_on_the_claim(rt):
    rt.linear.ready = [issue(desc=PLAIN.replace("Do it.", "Do it.\n\nRun: tester, executor"))]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.sequence == ["tester", "executor"] and c.phase == "test"


def test_an_unknown_archetype_skips_the_issue_with_one_comment(rt):
    rt.linear.ready = [issue(desc=PLAIN + "\nRun: executor, planner\n")]
    poll(rt)
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None
    (comment,) = rt.linear.comments
    assert "unknown archetype: planner" in comment[1] and comment[1].startswith("Skipping:")
```

In `tests/test_supervisor.py`, add:

```python
def test_without_a_reviewer_the_run_ends_when_the_pr_opens(rt):
    add(rt, phase="impl", sequence=["executor"])
    rt.sessions.alive_names.add("NF-1")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    assert current(rt).state == "done" and current(rt).phase == "impl"
    assert "https://pr/1" in rt.linear.comments[0]
    assert not any(name == "NF-1-review" for name, _ in rt.sessions.started)
```

In `tests/test_publisher.py`, add:

```python
def test_without_a_tester_the_guard_is_skipped(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    claim.tester_sha = None
    (path / "test_x.py").write_text("assert 1\n")
    git("commit", "-qam", "rewrite the test file", cwd=path)
    assert publish(claim, repo, "t", "s", token="t", runner=Gh()).status == "opened"
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_sequence.py tests/test_ledger.py tests/test_poller.py tests/test_supervisor.py tests/test_publisher.py -k "sequence or run_line or archetype or reviewer_the_run or without_a_tester"`
Expected: FAIL (`No module named 'flocc.sequence'`).

- [ ] **Step 3: Write `src/flocc/sequence.py`**

```python
import re

DEFAULT = ("executor", "reviewer")
# The service inserts fixers itself; a Run: line never names one.
PHASE = {"tester": "test", "executor": "impl", "fixer": "fix", "reviewer": "review"}
ARCHETYPE = {phase: name for name, phase in PHASE.items()}
_LISTABLE = ("tester", "executor", "reviewer")
_ORDERS = frozenset(
    {
        ("executor",),
        ("executor", "reviewer"),
        ("tester", "executor"),
        ("tester", "executor", "reviewer"),
    }
)
_LINE = re.compile(r"^[ \t]*run[ \t]*:(.*)$", re.IGNORECASE | re.MULTILINE)


class SequenceError(ValueError):
    pass


def parse_sequence(description: str | None) -> tuple[str, ...]:
    lines = _LINE.findall((description or "").replace("\r\n", "\n"))
    if not lines:
        return DEFAULT
    if len(lines) > 1:
        raise SequenceError("the description has more than one Run: line")
    raw = lines[0].strip()
    names = tuple(n.strip().lower() for n in raw.split(","))
    if "" in names:
        raise SequenceError(f"Run: {raw} has an empty name")
    if "fixer" in names:
        raise SequenceError("Run: never lists fixer; the service adds fixers itself")
    unknown = [n for n in names if n not in _LISTABLE]
    if unknown:
        raise SequenceError(
            f"Run: names an unknown archetype: {', '.join(unknown)}"
            " (use tester, executor and reviewer)"
        )
    if len(set(names)) != len(names):
        raise SequenceError("Run: names an archetype twice")
    if names not in _ORDERS:
        raise SequenceError(
            "Run: needs executor, optionally after tester and before reviewer, in that order"
        )
    return names
```

- [ ] **Step 4: Add the claim column**

In `src/flocc/ledger.py`, append to `_MIGRATIONS`:

```python
    ("""ALTER TABLE claims ADD COLUMN sequence TEXT NOT NULL DEFAULT '["executor", "reviewer"]'""",),
```

change `_JSON_FIELDS = ("depends_on",)` to `_JSON_FIELDS = ("depends_on", "sequence")`, add `from dataclasses import field` to the dataclasses import, and add as the last `Claim` field:

```python
    sequence: list[str] = field(default_factory=lambda: ["executor", "reviewer"])
```

- [ ] **Step 5: Parse it in the poller**

In `src/flocc/poller.py`, import `from flocc.sequence import SequenceError, parse_sequence`, add after `_comment_once`:

```python
def _bad_sequence(rt, issue: Issue, exc: SequenceError) -> None:
    # The only place an unusable Run: line reaches the owner.
    _comment_once(rt, issue, f"{issue.key}:bad-sequence:{exc}", f"Skipping: {exc}.")
```

In `poll`, right after the `except BlockError` block, add:

```python
        try:
            sequence = parse_sequence(issue.description)
        except SequenceError as exc:
            _bad_sequence(rt, issue, exc)
            continue
```

and pass it on: `_start(rt, issue, repo, settings, sequence)`. Change `_start`'s signature to `def _start(rt, issue: Issue, repo: str, settings, sequence: tuple[str, ...]) -> None:`, change `phase, tester_sha = "test", None` to `phase, tester_sha = ("test" if "tester" in sequence else "impl"), None`, change `if phase == "test":` (the `tests-amended.json` unlink) to `if tester_sha is None:`, and add `sequence=list(sequence),` to the `Claim(...)` call.

- [ ] **Step 6: Follow the sequence in the supervisor**

In `src/flocc/supervisor.py`, add after `_block`:

```python
def _end_done(rt, claim: Claim, **values) -> None:
    ledger.update_claim(rt.con, claim.issue_key, state="done", **values)
```

Replace every `ledger.update_claim(rt.con, key, state="done")` in the file with `_end_done(rt, claim)`, and `ledger.update_claim(rt.con, key, state="done", phase="review")` with `_end_done(rt, claim, phase="review")`. In `_finish_phase`'s `elif result.status == "opened":` branch, replace the closing:

```python
        else:
            _start_review(rt, claim, issue.title, issue.description, result.url)
```

with:

```python
        elif "reviewer" in claim.sequence:
            _start_review(rt, claim, issue.title, issue.description, result.url)
        else:
            (state_dir(rt.cfg, key) / "pr-url").write_text(result.url)
            _end_done(rt, claim)
```

- [ ] **Step 7: Make the tester guard optional**

In `src/flocc/publisher.py`, replace the start of `publish` from `wt = Path(claim.worktree)` through the `return PublishResult("tests_touched", ", ".join(parts))` line with:

```python
    wt = Path(claim.worktree)
    if claim.base_sha is None:
        raise ValueError(f"{claim.issue_key} has no recorded base commit")
    # No tester ran: there are no tester files to guard.
    if claim.tester_sha is not None:
        if (touched := _tests_guard(claim, repo, wt, accepted_sha, amended)) is not None:
            return touched
```

and add above `publish`:

```python
def _tests_guard(
    claim: Claim,
    repo: RepoConfig,
    wt: Path,
    accepted_sha: str | None,
    amended: dict[str, dict] | None,
) -> PublishResult | None:
    assert claim.base_sha is not None and claim.tester_sha is not None
    tester_files = changed_files(wt, claim.base_sha, claim.tester_sha)
    since = _without_merged_base(wt, accepted_sha or claim.tester_sha, repo.base_branch)
    touched = changed_files(wt, since, "HEAD") & tester_files
    weakened = {}
    for path in touched & set(amended or {}):
        if why := weakens_test(wt, since, path):
            weakened[path] = why
    touched -= set(amended or {})
    if touched or weakened:
        parts = [*sorted(touched), *(f"{p} {why}" for p, why in sorted(weakened.items()))]
        return PublishResult("tests_touched", ", ".join(parts))
    return None
```

Run `grep -n "no recorded tester commit" tests/` and change any test expecting that error to expect the guard skipped instead.

- [ ] **Step 8: Run the tests**

Run: `uv run pytest -q tests/test_sequence.py tests/test_ledger.py tests/test_poller.py tests/test_supervisor.py tests/test_publisher.py tests/test_rebase.py && uv run pyrefly check`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add src/flocc/sequence.py src/flocc/ledger.py src/flocc/poller.py src/flocc/supervisor.py src/flocc/publisher.py tests
git commit -s -m "Run: sequence on the claim; reviewer only when listed; optional tester guard"
```

---

### Task 6: `flocc done` for every archetype; "Needs the owner" ends the run blocked

`flocc review-done` goes: the reviewer runs `flocc done` too, which checks `review.md` in the review phase. A tester, executor or check fixer whose summary has a "Needs the owner" heading ends the run blocked; the review fixer keeps d161's owner items. The phase prompts name the new skills.

**Files:**
- Modify: `src/flocc/cli.py` (`cmd_done`, delete `cmd_review_done`, parser)
- Modify: `src/flocc/supervisor.py` (`_finish_phase`)
- Modify: `src/flocc/context.py` (`_COMMANDS`, `phase_prompt`)
- Test: `tests/test_cli.py`, `tests/test_supervisor.py`, `tests/test_context.py`, `tests/test_plugin.py`

**Interfaces:**
- Consumes: `sequence.ARCHETYPE` (Task 5); `supervisor._block` (plan 1).
- Produces: `phase_prompt(phase, issue_key, extra="")` names the skill `ARCHETYPE[phase]`; `flocc done [--summary TEXT]`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_cli.py`, replace `test_review_done_rejects_a_missing_or_empty_review` with:

```python
def test_done_in_the_review_phase_needs_the_review(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "review")
    assert main(["done"]) == 1
    (tmp_path / "review.md").write_text(" \n")
    assert main(["done"]) == 1 and not (tmp_path / "done.json").exists()
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    assert main(["done"]) == 0
    assert json.loads((tmp_path / "done.json").read_text())["summary"] == "review"


def test_done_elsewhere_needs_a_summary(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert main(["done"]) == 2 and "--summary" in capsys.readouterr().err
    assert main(["done", "--summary", "did it"]) == 0
    assert main(["review-done"]) == 2
```

In `tests/test_supervisor.py`, add:

```python
@pytest.mark.parametrize("phase, alive", [("impl", "NF-1"), ("test", "NF-1-test")])
def test_needs_the_owner_ends_the_run_blocked(rt, phase, alive):
    add(rt, phase=phase)
    rt.sessions.alive_names.add(alive)
    done(rt, "Did half.\n\nNeeds the owner: which currency is the default?")
    published = []
    rt.publish_fn = lambda *a, **kw: published.append(a) or PublishResult("opened", url="u")
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and not published
    (notice,) = rt.notifier.sent
    assert notice.cause == "NF-1:needs-owner" and "which currency" in notice.summary


def test_a_review_fixer_s_owner_items_do_not_block(rt):
    add(rt, phase="fix")
    ledger.meta_set(rt.con, "review_fix:NF-1", "1")
    rt.sessions.alive_names.add("NF-1")
    rt.publish_fn = lambda *a, **kw: PublishResult("opened", url="https://pr/1", existing=True)
    done(rt, "Fixed two.\n\nNeeds the owner: run the migration by hand.")
    supervise(rt, current(rt))
    assert current(rt).state == "running" and current(rt).phase == "review"
```

In `tests/test_context.py`, replace `test_phase_prompts_name_the_skill` with:

```python
def test_phase_prompts_name_the_archetype_skill():
    for phase, skill in (("test", "tester"), ("impl", "executor"), ("fix", "fixer")):
        prompt = phase_prompt(phase, "NF-1")
        assert f"flocc {skill} skill" in prompt and "flocc done" in prompt
    review = phase_prompt("review", "NF-1")
    assert "flocc reviewer skill" in review and "review.md" in review
    assert phase_prompt("fix", "NF-1", extra="E1 boom").endswith("E1 boom")
```

In `tests/test_plugin.py`, change `test_fix_prompt_points_at_the_fix_section` to `assert "fixer skill" in phase_prompt("fix", "NF-1", "check output")`.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_cli.py tests/test_supervisor.py tests/test_context.py tests/test_plugin.py -k "done or needs_the_owner or owner_items or prompt"`
Expected: FAIL.

- [ ] **Step 3: One `flocc done`**

In `src/flocc/cli.py`, replace `cmd_done` and delete `cmd_review_done`:

```python
def cmd_done(args) -> int:
    state = _state_dir()
    if os.environ.get("FLOCC_PHASE") == "review":
        review = state / "review.md"
        if not review.exists() or not review.read_text().strip():
            print(f"done: write {review} first; the service posts it", file=sys.stderr)
            return 1
        summary = "review"
    elif not (args.summary or "").strip():
        print("done: pass --summary '<what you did, for the PR body>'", file=sys.stderr)
        return 2
    else:
        summary = args.summary
    (state / "done.json").write_text(json.dumps({"summary": summary, "at": time.time()}))
    print("marked done; the service takes over on its next run")
    return 0
```

Delete `"review-done": cmd_review_done,` and the `add("review-done", ...)` line; change the `done` parser to:

```python
    done = add("done", "Mark this step done (the reviewer's review.md, or --summary)")
    done.add_argument("--summary", default=None, help="what was done, for the PR body")
```

- [ ] **Step 4: Block on "Needs the owner"**

In `src/flocc/supervisor.py`, import `from flocc.sequence import ARCHETYPE`. In `_finish_phase`, move the two lines

```python
    review_fix = bool(ledger.meta_get(rt.con, f"review_fix:{key}"))
    rebasing = bool(ledger.meta_get(rt.con, f"{REBASE}:{key}"))
```

up to just after the `if claim.phase == "review": ... return` block, and add right after them:

```python
    found = _OWNER_HEADING.search(summary)
    # A review fixer's owner items ride to the final comment instead: the PR is open and a
    # second review follows.
    if found and not review_fix and not rebasing:
        _block(
            rt,
            claim,
            "needs-owner",
            f"The {ARCHETYPE[claim.phase]} needs you:\n{summary[found.start() :].strip()}"[:3000],
            "Reply on this issue, then move it back to Todo; the rerun continues on the kept"
            " branch",
        )
        return
```

and change the later owner-items condition from `and (found := _OWNER_HEADING.search(summary))` to `and found`.

- [ ] **Step 5: Prompts and commands**

In `src/flocc/context.py`, replace `_COMMANDS`'s "Commands" list with:

```python
## Commands

- `flocc context` to see this text again.
- `flocc done --summary '<one paragraph>'` when this step is finished; the reviewer runs
  `flocc done` after writing review.md.
- `flocc commit -m '<message>' -- <paths>` to commit only those paths.
"""
```

(keep the "Execution only" section above it), and replace `phase_prompt` with:

```python
def phase_prompt(phase: str, issue_key: str, extra: str = "") -> str:
    skill = ARCHETYPE[phase]
    body = (
        f"You are the {skill} for {issue_key}. Follow the flocc {skill} skill and end the way it"
        " says: delete your scratchpad and the temp files you made, write your summary, run"
        " flocc done."
    )
    if phase == "review":
        body += (
            " Change nothing in the repo; read review-input.md and write review.md in your state"
            " directory."
        )
    return f"{body}\n\n{extra}".strip()
```

with `from flocc.sequence import ARCHETYPE` at the top.

- [ ] **Step 6: Run the tests**

Run: `uv run pytest -q tests/test_cli.py tests/test_supervisor.py tests/test_context.py tests/test_plugin.py && uv run pyrefly check`
Expected: PASS except `tests/test_plugin.py` tests that read `plugin/skills/implementer/`, which Task 7 replaces; if any fails only for that reason, leave it for Task 7 and say so in the commit body.

- [ ] **Step 7: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add src/flocc/cli.py src/flocc/supervisor.py src/flocc/context.py tests
git commit -s -m "flocc done for every archetype; Needs the owner ends the run blocked"
```

---

### Task 7: The four archetype skills

`plugin/skills/{executor,fixer,tester,reviewer}/SKILL.md` replace `implementer`, `tester` and `reviewer`. Each ends with cleanup, summary and `flocc done`; none mentions `AskUserQuestion`; a need for the owner goes under "Needs the owner".

**Files:**
- Create: `plugin/skills/executor/SKILL.md`, `plugin/skills/fixer/SKILL.md`
- Modify (replace whole file): `plugin/skills/tester/SKILL.md`, `plugin/skills/reviewer/SKILL.md`
- Delete: `plugin/skills/implementer/`
- Modify: `plugin/.claude-plugin/plugin.json` (`description`)
- Test: `tests/test_plugin.py`

**Interfaces:**
- Consumes: `phase_prompt` names `flocc <archetype> skill` (Task 6); `flocc done`, `flocc commit`, `flocc tests-amended`; the catalogue agents in `plugin/agents/`.
- Produces: skill names `executor`, `fixer`, `tester`, `reviewer`.

- [ ] **Step 1: Write the failing test**

Add to `tests/test_plugin.py`:

```python
def test_the_four_archetype_skills_end_the_same_way():
    skills = {p.parent.name: p.read_text() for p in (PLUGIN / "skills").glob("*/SKILL.md")}
    assert set(skills) == {"executor", "fixer", "tester", "reviewer"}
    for name, text in skills.items():
        assert frontmatter(PLUGIN / "skills" / name / "SKILL.md")["name"] == name
        assert "AskUserQuestion" not in text and "auto mode" not in text
        assert "Needs the owner" in text and "flocc done" in text
        assert "scratchpad" in text
        assert "## Last steps" in text
```

- [ ] **Step 2: Run it to see it fail**

Run: `uv run pytest -q tests/test_plugin.py::test_the_four_archetype_skills_end_the_same_way`
Expected: FAIL (`implementer` is present, `executor` and `fixer` are not).

- [ ] **Step 3: Write the skills**

```bash
git rm -r plugin/skills/implementer
```

`plugin/skills/executor/SKILL.md`:

```markdown
---
name: executor
description: Use when you are the executor session for a flocc issue
---

Carry out the issue as written. The issue, and the specs and plans it names, are the approved
design: do not redesign, re-plan or ask for sign-off.

- Read the issue's acceptance criteria and every spec or plan path it names before changing code.
  When it names a plan, its tasks are your tasks, in its order.
- Where the spec leaves a choice open, take its default or the smallest choice that meets the
  criteria. Nobody answers questions in this session. If you cannot go on without the owner, stop
  changing code and write the need under a line that starts with `Needs the owner` in your
  summary; the run then ends blocked with your work kept on the branch.
- If a tester ran, the committed tests are the contract. Change a tester's file only when one of
  its tests contradicts the acceptance criteria or the real API; then fix it, run
  `flocc tests-amended <path>... --reason "<why it was wrong>"` and carry on. Never delete or skip
  a test: the service rejects the run if you do.
- Commit as you go with `flocc commit -m '<message>' -- <paths>`; the repo's own hooks still run.
- For a task bigger than a few files you may start one `flocc:task-implementer` per task with a
  brief (requirements, owned paths, the focused test command) in `briefs/` in your state
  directory, then have one `flocc:wave-reviewer` read the combined diff once and fix what it
  finds. Start only `flocc:` agents; never general-purpose agents, workflows, teammates or
  `claude` from the shell.
- To look up how a library or API behaves, use `flocc:source-finder` then `flocc:source-reader`,
  with today's date in the task.
- Run the repo's check (usually `just check`) before finishing.
- Never push, open a PR or touch Linear; the service does that after `flocc done`.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean: commit what belongs to the change, delete what does not.
3. Run `flocc done --summary '<what changed and why, for the PR body>'`.
```

`plugin/skills/fixer/SKILL.md`:

```markdown
---
name: fixer
description: Use when you are the fixer session for a flocc issue (a failed check, a review's findings, or a PR that no longer merges)
---

Fix what the text you were started with reports, on the existing branch, and nothing else.

- The failure is in your prompt and in `check-failure.md` in your state directory; the full check
  output, when there is one, is in `check-output.log` there. A review's findings are in your
  prompt. Read `last-summary.md` for what the previous step did.
- Fix inline. For a fix bigger than a few files you may start one `flocc:task-implementer` with a
  brief in `briefs/fix.md` in your state directory and one `flocc:fix-rereviewer` on its diff.
- Change a tester's file only when its test is wrong, and record it with
  `flocc tests-amended <path>... --reason "<why>"`. Never delete or skip a test.
- When the fix answers a review, fix what is agent work. For a finding that needs the owner (a
  manual run, a product decision), change nothing and list it under a line that starts with
  `Needs the owner` in your summary.
- When you were started on a failed check and cannot make it pass, write what you could not fix
  under `Needs the owner`; the run then ends blocked.
- Commit with `flocc commit -m '<message>' -- <paths>`. Run the repo's check before finishing.
- Never push, open a PR or touch Linear. Nobody answers questions in this session.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean.
3. Run `flocc done --summary '<what you fixed>'`.
```

`plugin/skills/tester/SKILL.md`:

```markdown
---
name: tester
description: Use when you are the tester session for a flocc issue
---

Write tests for the issue's acceptance criteria and nothing else.

- Each test asserts observable behavior a correct implementation must have. A test that passes
  against a stub, or only checks that a function exists, does not count.
- Follow the repo's existing test layout and helpers. Run the new tests; they should fail now.
- Do not write implementation code.
- Commit them with `flocc commit -m '<message naming the criteria>' -- <paths>`.
- Where the spec leaves a choice open, take its default. Nobody answers questions in this
  session; if a criterion cannot be tested without the owner, write that under a line that
  starts with `Needs the owner` in your summary, and the run ends blocked.
- To look up how a library or API behaves, use `flocc:source-finder` then `flocc:source-reader`,
  with today's date in the task. Start no other agents.
- Never push or touch Linear.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean.
3. Run `flocc done --summary '<which test covers which criterion, how to run them, and what you
   learned about the code that the executor would otherwise rediscover>'`. The executor starts
   from this summary.
```

`plugin/skills/reviewer/SKILL.md`: keep the current file's body from "Review the branch" through "Close the file with exactly one line, ..." unchanged, with these changes, then end with the "Last steps" section below:
- Delete the bullet that starts `Run the repo's check only if you doubt it passes` and replace it with `- Run the repo's check only if you doubt it passes; the service ran it before the push.`
- Add as the last bullet: ``- Nobody answers questions in this session. A finding only the owner can settle goes under `## Important` with a line that starts with `Needs the owner`.``
- Replace the final paragraph (`Then run \`flocc review-done\`. ...`) with:

```markdown
## Last steps

1. Delete your scratchpad and any temp files you made.
2. Run `flocc done`. The service posts `review.md` as a PR comment; the owner decides whether to
   merge.
```

In `plugin/.claude-plugin/plugin.json`, set `"description": "Executor, fixer, tester and reviewer skills for flocc sessions"`.

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q tests/test_plugin.py tests/test_context.py`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A plugin tests/test_plugin.py
git commit -s -m "Executor, fixer, tester and reviewer skills replace the implementer"
```

---

## Part B: two users

From Task 8 on, the service reads every file a session writes from `cfg.agent_state_root/<KEY>` (flocc-agent's) and keeps its own files in `cfg.state_root/<KEY>`. In tests, `tmp_cfg` points both at the same directory so the existing supervisor and poller tests keep working; the tests that pin the split use separate directories. A real host does not work again until Task 15 lands; nothing ships before then.

### Task 8: The fixed layout and link-safe reads of agent files

`worktree_root` and `plugin_dir` become fixed two-user paths, not config keys; new fields name flocc-agent's home, state root, `flocc` binary and the wrapper argv. `flocc.agentfs` reads what flocc-agent wrote without following a link it planted, and every service read of a session's file goes through it.

**Files:**
- Create: `src/flocc/agentfs.py`
- Modify: `src/flocc/config.py`, `deploy/config.example.toml`, `tests/conftest.py`
- Modify: `src/flocc/context.py` (`agent_dir`)
- Modify: `src/flocc/supervisor.py` (reads of `done.json`, `idle`, `review.md`; the posted review's service copy)
- Modify: `src/flocc/publisher.py` (`read_amended`), `src/flocc/usage.py` (`read_status`, `latest_usage`), `src/flocc/status.py` (`_claim_row`, `collect`), `src/flocc/tick.py` (`read_amended` call)
- Modify: every `latest_usage(cfg.state_root, ...)` / `latest_usage(rt.cfg.state_root, ...)` call (`supervisor.py`, `poller.py`, `rebase.py`, `status.py`)
- Modify: `installer/internal/steps/testdata/config.golden.toml`, `config.skip.golden.toml` (regenerated)
- Test: `tests/test_agentfs.py` (new), `tests/test_config.py`, `tests/test_supervisor.py`, `tests/test_usage.py`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Config.agent_user = "flocc-agent"`, `Config.agent_home = Path("/home/flocc-agent")`, `Config.worktree_root = Path("/home/flocc-agent/worktrees")`, `Config.agent_state_root = Path("/home/flocc-agent/state")`, `Config.plugin_dir = Path("/opt/flocc/plugin")`, `Config.agent_flocc = Path("/opt/flocc/bin/flocc")`, `Config.agent_run: tuple[str, ...] = AGENT_RUN`; properties `Config.agent_token_file`, `Config.agent_claude`. `config.AGENT_RUN = ("sudo", "-n", "-H", "-u", "flocc-agent", "/usr/local/libexec/flocc-agent-run")`. `context.agent_dir(cfg, key) -> Path`. `agentfs.read_text(directory: Path, name: str, limit: int = LIMIT) -> str | None`, `agentfs.exists(directory, name) -> bool`, `agentfs.mtime(directory, name) -> float | None`, `agentfs.entries(directory) -> list[str]`, `agentfs.AgentFileError(OSError)`. `supervisor.POSTED_REVIEW = "review-posted.md"` (service state). `usage.latest_usage(agent_state_root, now)`.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_agentfs.py`:

```python
import os

import pytest

from flocc.agentfs import AgentFileError, entries, exists, mtime, read_text

as_root = pytest.mark.skipif(os.geteuid() == 0, reason="root may follow root-owned links")


def test_reads_a_file_and_misses_quietly(tmp_path):
    (tmp_path / "NF-1").mkdir()
    (tmp_path / "NF-1" / "done.json").write_text('{"summary": "ok"}')
    assert read_text(tmp_path / "NF-1", "done.json") == '{"summary": "ok"}'
    assert read_text(tmp_path / "NF-1", "idle") is None
    assert read_text(tmp_path / "NF-2", "done.json") is None
    assert exists(tmp_path / "NF-1", "done.json") and not exists(tmp_path / "NF-1", "idle")
    assert mtime(tmp_path / "NF-1", "done.json") is not None


@as_root
def test_a_link_planted_anywhere_on_the_path_is_refused(tmp_path):
    secret = tmp_path / "linear.key"
    secret.write_text("lin_api_secret")
    state = tmp_path / "agent" / "state"
    (state / "NF-1").mkdir(parents=True)
    (state / "NF-1" / "review.md").symlink_to(secret)
    with pytest.raises(AgentFileError):
        read_text(state / "NF-1", "review.md")
    (tmp_path / "done.json").write_text("{}")
    (state / "NF-2").symlink_to(tmp_path)
    with pytest.raises(AgentFileError):
        read_text(state / "NF-2", "done.json")
    with pytest.raises(AgentFileError):
        exists(state / "NF-2", "done.json")


def test_a_fifo_or_an_oversized_file_is_refused(tmp_path):
    os.mkfifo(tmp_path / "idle")
    with pytest.raises(AgentFileError):
        read_text(tmp_path, "idle")
    (tmp_path / "big").write_text("12345")
    with pytest.raises(AgentFileError):
        read_text(tmp_path, "big", limit=4)


@as_root
def test_entries_lists_real_directories_only(tmp_path):
    (tmp_path / "NF-1").mkdir()
    (tmp_path / "NF-2").symlink_to(tmp_path / "NF-1")
    (tmp_path / "file").write_text("x")
    assert entries(tmp_path) == ["NF-1"]
    assert entries(tmp_path / "missing") == []
```

In `tests/test_config.py`, add (`from pathlib import Path` if missing):

```python
def test_layout_keys_in_an_old_config_are_ignored_with_a_warning(tmp_path, capsys):
    path = tmp_path / "config.toml"
    old = 'worktree_root = "~/worktrees"\nplugin_dir = "~/flocc/plugin"\n'
    path.write_text(old + EXAMPLE.read_text())
    cfg = load(path)
    assert cfg.worktree_root == Path("/home/flocc-agent/worktrees")
    assert cfg.plugin_dir == Path("/opt/flocc/plugin")
    assert cfg.agent_token_file == Path("/home/flocc-agent/.config/flocc/gh-agent.token")
    err = capsys.readouterr().err
    assert "worktree_root is fixed by the two-user layout" in err and "plugin_dir" in err
```

In `tests/test_supervisor.py`, add (`from flocc.agentfs import AgentFileError`):

```python
def test_review_md_that_is_a_link_is_never_posted(rt, tmp_path):
    add(rt, phase="review")
    rt.sessions.alive_names.add("NF-1-review")
    secret = tmp_path / "linear.key"
    secret.write_text("lin_api_secret")
    (state_dir(rt.cfg, "NF-1") / "review.md").symlink_to(secret)
    done(rt, "review")
    with pytest.raises(AgentFileError):
        supervise(rt, current(rt))
    assert rt.pr_comments == []


def test_session_files_are_read_from_the_agent_state_root(rt, tmp_path):
    rt.cfg = replace(rt.cfg, agent_state_root=tmp_path / "agent-state")
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    (tmp_path / "agent-state" / "NF-1").mkdir(parents=True)
    (tmp_path / "agent-state" / "NF-1" / "done.json").write_text('{"summary": "tests"}')
    supervise(rt, current(rt))
    assert current(rt).phase == "impl"
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_agentfs.py tests/test_config.py tests/test_supervisor.py -k "agentfs or link or layout or agent_state_root or reads_a_file or fifo or entries"`
Expected: FAIL (`No module named 'flocc.agentfs'`).

- [ ] **Step 3: Write `src/flocc/agentfs.py`**

```python
import os
import stat
from pathlib import Path

LIMIT = 8 * 1024 * 1024
_DIR = os.O_RDONLY | os.O_DIRECTORY


class AgentFileError(OSError):
    pass


def _open_dir(path: Path) -> int:
    """A descriptor for path, reached without following any link flocc-agent could have made."""
    fd = os.open("/", _DIR)
    try:
        for part in Path(os.path.abspath(path)).parts[1:]:
            st = os.stat(part, dir_fd=fd, follow_symlinks=False)
            flags = _DIR | os.O_NOFOLLOW
            if stat.S_ISLNK(st.st_mode):
                # Only root's links are followed (a distro's /home -> var/home); flocc-agent owns
                # everything under its home and could point a link at flocc's secrets.
                if st.st_uid != 0:
                    raise AgentFileError(f"{path}: {part} is a link")
                flags = _DIR
            nfd = os.open(part, flags, dir_fd=fd)
            os.close(fd)
            fd = nfd
    except BaseException:
        os.close(fd)
        raise
    return fd


def _stat(directory: Path, name: str) -> os.stat_result | None:
    try:
        dfd = _open_dir(directory)
    except FileNotFoundError:
        return None
    try:
        return os.stat(name, dir_fd=dfd, follow_symlinks=False)
    except FileNotFoundError:
        return None
    finally:
        os.close(dfd)


def read_text(directory: Path, name: str, limit: int = LIMIT) -> str | None:
    """directory/name as text; None when either is missing. Raises AgentFileError for a link on
    the way, a file that is not regular, or one over limit bytes."""
    try:
        dfd = _open_dir(directory)
    except FileNotFoundError:
        return None
    try:
        # O_NONBLOCK: opening a FIFO flocc-agent left there must not hang the tick.
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=dfd)
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise AgentFileError(f"{directory / name}: {exc.strerror}") from exc
    finally:
        os.close(dfd)
    with os.fdopen(fd, "rb") as f:
        if not stat.S_ISREG(os.fstat(f.fileno()).st_mode):
            raise AgentFileError(f"{directory / name} is not a regular file")
        data = f.read(limit + 1)
    if len(data) > limit:
        raise AgentFileError(f"{directory / name} is over {limit} bytes")
    return data.decode(errors="replace")


def exists(directory: Path, name: str) -> bool:
    return _stat(directory, name) is not None


def mtime(directory: Path, name: str) -> float | None:
    st = _stat(directory, name)
    return st.st_mtime if st is not None and stat.S_ISREG(st.st_mode) else None


def entries(directory: Path) -> list[str]:
    """The real subdirectories of directory, by name."""
    try:
        dfd = _open_dir(directory)
    except FileNotFoundError:
        return []
    try:
        names = []
        for name in os.listdir(dfd):
            try:
                st = os.stat(name, dir_fd=dfd, follow_symlinks=False)
            except FileNotFoundError:
                continue
            if stat.S_ISDIR(st.st_mode):
                names.append(name)
        return sorted(names)
    finally:
        os.close(dfd)
```

- [ ] **Step 4: Fix the layout in config**

In `src/flocc/config.py`:
- Add after `_RETIRED`:

```python
# The two-user layout (d176): paths flocc setup creates, the same on every host.
_LAYOUT = ("worktree_root", "plugin_dir", "agent_home", "agent_state_root", "agent_flocc", "agent_run")
AGENT_RUN = ("sudo", "-n", "-H", "-u", "flocc-agent", "/usr/local/libexec/flocc-agent-run")
```

- Remove `"worktree_root"` and `"plugin_dir"` from `_PATH_KEYS`.
- In `Config`, delete the required fields `worktree_root: Path` and `plugin_dir: Path`, and add after `steer_authors`:

```python
    agent_user: str = "flocc-agent"
    agent_home: Path = Path("/home/flocc-agent")
    worktree_root: Path = Path("/home/flocc-agent/worktrees")
    agent_state_root: Path = Path("/home/flocc-agent/state")
    plugin_dir: Path = Path("/opt/flocc/plugin")
    agent_flocc: Path = Path("/opt/flocc/bin/flocc")
    agent_run: tuple[str, ...] = AGENT_RUN
```

- Add properties after `effort_for`:

```python
    @property
    def agent_token_file(self) -> Path:
        """flocc-agent's copy of the read-only token, written by flocc-agent-run."""
        return self.agent_home / ".config" / "flocc" / "gh-agent.token"

    @property
    def agent_claude(self) -> Path:
        return self.agent_home / ".local" / "bin" / "claude"
```

- In `load`, after the `_RETIRED` loop, add:

```python
    for key in _LAYOUT:
        if raw.pop(key, None) is not None:
            print(f"{path}: {key} is fixed by the two-user layout; delete it", file=sys.stderr)
```

In `deploy/config.example.toml`, delete the `worktree_root = ...` and `plugin_dir = ...` lines. Regenerate the installer's goldens: `go test ./installer/internal/steps -run Config -update`, then `git diff installer/internal/steps/testdata` must show only those two lines gone.

In `tests/conftest.py::tmp_cfg`, add to the `replace(...)` call `agent_home=tmp_path,` and `agent_state_root=tmp_path / "state",` (the same directory as `state_root`, so tests that write a session's files into `state_dir` keep working). Run `grep -rn "worktree_root\|plugin_dir" tests/*.py` and fix any test that read them from `EXAMPLE` to expect the fixed paths.

- [ ] **Step 5: Read session files through agentfs**

In `src/flocc/context.py`, add:

```python
def agent_dir(cfg: Config, key: str) -> Path:
    """Where the session for key keeps its files: flocc-agent's, read through flocc.agentfs."""
    return cfg.agent_state_root / key
```

In `src/flocc/supervisor.py` (import `from flocc import agentfs, canary, ledger` and `agent_dir` from `flocc.context`; add `POSTED_REVIEW = "review-posted.md"` beside `OWNER_ITEMS`):
- In `supervise`, replace

```python
    done = d / "done.json"
    if done.exists():
        try:
            summary = json.loads(done.read_text()).get("summary", "")
        except ValueError:  # still being written; the next tick reads it whole
            return
```

with

```python
    agent = agent_dir(rt.cfg, key)
    raw_done = agentfs.read_text(agent, "done.json")
    if raw_done is not None:
        try:
            summary = str(json.loads(raw_done).get("summary", ""))
        except (ValueError, AttributeError):  # still being written; the next tick reads it whole
            return
```

  and change `if (message := _idle_message(d)) is not None:` to `if (message := _idle_message(agent)) is not None:`.
- Replace `_idle_message` with:

```python
def _idle_message(agent: Path) -> str | None:
    """The last words of a session that stopped twice without flocc done; None when it did not."""
    raw = agentfs.read_text(agent, "idle")
    if raw is None:
        return None
    try:
        data = json.loads(raw)
    except ValueError:
        return ""
    return str(data.get("message", "")) if isinstance(data, dict) else ""
```

- In `_finish_review`, replace `review = state_dir(rt.cfg, key) / "review.md"` and `text = review.read_text() if review.exists() else ""` with:

```python
    text = agentfs.read_text(agent_dir(rt.cfg, key), "review.md") or ""
    if text.strip():
        (state_dir(rt.cfg, key) / POSTED_REVIEW).write_text(text)
```

- In `_start_review`, change the two `(d / "review.md")` reads of the earlier review to `(d / POSTED_REVIEW)` and delete `(d / "review.md").unlink(missing_ok=True)` (the agent's `review.md` is cleared at the next launch). Run `grep -n '"review.md"' tests/test_supervisor.py`; where a test seeds the earlier review for a second round, seed `review-posted.md` instead.
- Change `latest_usage(rt.cfg.state_root, rt.now())` to `latest_usage(rt.cfg.agent_state_root, rt.now())` (both places).

In `src/flocc/publisher.py`, replace `read_amended`:

```python
def read_amended(state: Path) -> dict[str, dict]:
    try:
        data = json.loads(agentfs.read_text(state, AMENDED) or "{}")
    except ValueError:
        return {}
    return data if isinstance(data, dict) else {}
```

(import `from flocc import agentfs`). In `src/flocc/tick.py`, change `amended=read_amended(state_dir(cfg, claim.issue_key))` to `amended=read_amended(agent_dir(cfg, claim.issue_key))` (import `agent_dir`).

In `src/flocc/usage.py` (import `from flocc import agentfs`), replace `read_status` and `latest_usage`:

```python
def read_status(state_dir: Path) -> dict | None:
    try:
        data = json.loads(agentfs.read_text(state_dir, "status.json") or "null")
    except (OSError, ValueError):
        return None
    return data if isinstance(data, dict) else None


def latest_usage(state_root: Path, now: float) -> Usage | None:
    best: tuple[float, dict] | None = None
    for key in agentfs.entries(state_root):
        # Each launch deletes the previous session's status file, so it can vanish mid-scan.
        try:
            mtime = agentfs.mtime(state_root / key, "status.json")
            data = read_status(state_root / key)
        except OSError:
            continue
        if mtime is None or data is None:
            continue
        if "rate_limits" in data and (best is None or mtime > best[0]):
            best = (mtime, data)
    if best is None:
        return None
    five, week = limit_pcts(best[1], now)
    # Not 0%: admission would start sessions on a reading it cannot interpret.
    if five is None or week is None:
        return None
    return Usage(five_hour_pct=five, weekly_pct=week, observed_at=best[0])
```

`write_status` stays (the session's `flocc statusline` writes its own directory).

In `src/flocc/status.py`, in `_claim_row` replace `state_dir = cfg.state_root / claim.issue_key`, `status = usage.read_status(state_dir)` and `updated = _mtime(state_dir / "status.json")` with:

```python
    agent = cfg.agent_state_root / claim.issue_key
    status = usage.read_status(agent)
    try:
        updated = agentfs.mtime(agent, "status.json")
    except OSError:
        updated = None
```

and in `collect` change `usage.latest_usage(cfg.state_root, now)` to `usage.latest_usage(cfg.agent_state_root, now)` (import `agentfs`). Change the remaining `latest_usage(cfg.state_root, ...)` / `latest_usage(rt.cfg.state_root, ...)` calls in `poller.py` and `rebase.py` to `agent_state_root`.

- [ ] **Step 6: Run the tests**

Run: `uv run pytest -q && uv run pyrefly check && go test ./installer/internal/steps/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add src tests deploy/config.example.toml installer/internal/steps/testdata
git commit -s -m "Fix the two-user layout; read session files without following links"
```

---

### Task 9: `flocc-agent-run`, `flocc-agent` and sessions started through sudo

The wrapper script, its sudoers rule, the agent-side module and the service-side client. Sessions now start, stop and get typed into through the wrapper; the files a session needs travel in the `start` request; the pane's environment is flocc-agent's; the agent's token is written by the wrapper. `flocc tests-amended` reads `tester.json` instead of the ledger, which flocc-agent cannot open.

**Files:**
- Create: `deploy/flocc-agent-run`, `deploy/flocc-agent.sudoers`
- Create: `src/flocc/agentrun.py`, `src/flocc/agent.py`
- Modify: `pyproject.toml` (`[project.scripts]`)
- Modify: `src/flocc/sessions.py` (`agent_env` replaces `clean_env`; `claude_argv` drops `disabled_plugins`, uses `cfg.agent_token_file`)
- Modify: `src/flocc/supervisor.py` (`launch`, `_allowed_plugins` replaces `_disabled_plugins`)
- Modify: `src/flocc/poller.py` (`_start`'s `tests-amended.json` reset)
- Modify: `src/flocc/runtime.py` (`agent` field), `src/flocc/tick.py` (`build_runtime`)
- Modify: `src/flocc/config.py` (drop `claude_json`, `installed_plugins_file`)
- Modify: `src/flocc/cli.py` (`cmd_tests_amended`, delete `_ledger`)
- Modify: `tests/fakes.py`, `tests/conftest.py`
- Test: `tests/test_agentrun.py` (new), `tests/test_sessions.py`, `tests/test_supervisor.py`, `tests/test_cli.py`

**Interfaces:**
- Consumes: `agent_dir`, `Config.agent_*` (Task 8); `LAST_SUMMARY` (Task 4).
- Produces:
  - `agentrun.SUBCOMMANDS: tuple[str, ...]` (`alive`, `attach`, `capture`, `kill`, `send`, `start` here; later tasks add theirs), `agentrun.main(argv) -> int`, `agentrun.apply_handoff(state: Path, files: dict[str, str], clear) -> dict[str, bytes]`, `agentrun.restore(state, saved)`, `agentrun.with_disabled_plugins(argv, allowed) -> list[str]`, `agentrun.write_token(token)`, `agentrun.BadRequest`, `agentrun.HANDLERS: dict[str, Callable[[dict], dict]]` (JSON-request subcommands).
  - `agent.AgentError(subprocess.SubprocessError)`; `agent.Agent(cfg, runner=subprocess.run)` with `run(sub, *args, payload=None, timeout=60) -> CompletedProcess`, `call(sub, *args, payload=None, timeout=60) -> dict`, `token() -> str`, `sessions() -> AgentSessions`; `agent.AgentSessions` with `start(name, cwd, argv, *, state, files=None, clear=(), trust=(), plugins=None)`, `alive(name) -> bool`, `kill(name)`, `send(name, text)`, `capture(name, lines) -> str`.
  - `sessions.agent_env(cfg, extra) -> dict[str, str]`.
  - `supervisor.TESTER = "tester.json"` (`{"base_sha": ..., "tester_sha": ...}`), `supervisor.launch(rt, claim, phase, extra="", reset=())`.
  - `Runtime.agent: Any = None`.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_agentrun.py`:

```python
import json
import re
import subprocess
import sys
from pathlib import Path

import pytest

from flocc import agentrun
from flocc.agent import Agent, AgentError
from flocc.agentrun import apply_handoff, restore, with_disabled_plugins

ROOT = Path(__file__).parent.parent
WRAPPER = ROOT / "deploy" / "flocc-agent-run"


def test_the_wrapper_and_the_module_know_the_same_subcommands():
    listed = re.search(r"^  ([a-z|-]+)\) ;;$", WRAPPER.read_text(), re.MULTILINE)
    assert listed and sorted(listed.group(1).split("|")) == sorted(agentrun.SUBCOMMANDS)


def test_the_wrapper_refuses_an_unknown_subcommand():
    for argv in (["rm", "-rf", "/"], []):
        out = subprocess.run(["sh", str(WRAPPER), *argv], capture_output=True, text=True)
        assert out.returncode == 2 and "unknown subcommand" in out.stderr


def test_the_sudoers_rule_allows_only_the_wrapper():
    rules = [
        line
        for line in (ROOT / "deploy" / "flocc-agent.sudoers").read_text().splitlines()
        if line.strip() and not line.startswith("#")
    ]
    assert rules == ["flocc ALL=(flocc-agent) NOPASSWD: /usr/local/libexec/flocc-agent-run"]


def test_the_module_refuses_unknown_subcommands_and_bad_names(capsys):
    assert agentrun.main(["shell"]) == 2
    assert agentrun.main(["kill", "NF-1; rm -rf ~"]) == 2
    assert "bad request" in capsys.readouterr().err


def test_handoff_clears_markers_writes_files_and_can_put_markers_back(tmp_path):
    (tmp_path / "done.json").write_text('{"summary": "old"}')
    saved = apply_handoff(tmp_path, {"context.md": "CTX"}, ["done.json", "idle"])
    assert not (tmp_path / "done.json").exists() and (tmp_path / "context.md").read_text() == "CTX"
    restore(tmp_path, saved)
    assert (tmp_path / "done.json").read_text() == '{"summary": "old"}'
    with pytest.raises(agentrun.BadRequest):
        apply_handoff(tmp_path, {"../x": "y"}, [])


def test_unlisted_plugins_are_disabled_in_the_settings(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    plugins = tmp_path / ".claude" / "plugins"
    plugins.mkdir(parents=True)
    (plugins / "installed_plugins.json").write_text(json.dumps({"plugins": {"a@m": [], "b@m": []}}))
    argv = ["claude", "--settings", json.dumps({"x": 1}), "--name", "NF-1"]
    out = with_disabled_plugins(argv, ["a@m"])
    assert json.loads(out[2]) == {"x": 1, "enabledPlugins": {"b@m": False}}
    assert with_disabled_plugins(argv, None) == argv


def test_start_through_the_module_writes_the_token_and_the_files(tmp_cfg, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.agentrun.Sessions.start", lambda self, name, cwd, argv: None)
    monkeypatch.setenv("HOME", str(tmp_path))
    state = tmp_path / "state" / "NF-1"
    request = {
        "name": "NF-1",
        "cwd": str(tmp_path / "wt" / "NF-1"),
        "argv": ["true"],
        "state": str(state),
        "files": {"context.md": "CTX"},
        "clear": ["done.json"],
        "trust": [],
        "plugins": None,
        "token": "ghp_x\n",
    }
    monkeypatch.setattr("sys.stdin", __import__("io").StringIO(json.dumps(request)))
    assert agentrun.main(["start"]) == 0
    assert (state / "context.md").read_text() == "CTX"
    token = tmp_path / ".config" / "flocc" / "gh-agent.token"
    assert token.read_text() == "ghp_x\n" and token.stat().st_mode & 0o777 == 0o600
    trusted = json.loads((tmp_path / ".claude.json").read_text())["projects"]
    assert trusted[str(tmp_path / "wt" / "NF-1")]["hasTrustDialogAccepted"] is True


def test_start_outside_the_agent_home_is_refused(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("HOME", str(tmp_path / "agent"))
    request = {"name": "NF-1", "cwd": "/etc", "argv": ["true"], "state": "/etc/x", "token": ""}
    monkeypatch.setattr("sys.stdin", __import__("io").StringIO(json.dumps(request)))
    assert agentrun.main(["start"]) == 2 and "outside" in capsys.readouterr().err


def test_the_client_runs_the_module_and_maps_failures(tmp_cfg):
    agent = Agent(tmp_cfg)
    assert agent.sessions().alive("NF-999") is False
    with pytest.raises(AgentError):
        agent.call("send", payload={"name": "bad name", "text": "x"})


def test_the_client_sets_the_agent_home(tmp_cfg):
    seen = {}

    def runner(argv, **kw):
        seen.update(kw)
        return subprocess.CompletedProcess(argv, 0, "{}", "")

    Agent(tmp_cfg, runner=runner).call("send", payload={"name": "NF-1", "text": "hi"})
    assert seen["env"]["HOME"] == str(tmp_cfg.agent_home)
    assert json.loads(seen["input"]) == {"name": "NF-1", "text": "hi"}
```

In `tests/conftest.py::tmp_cfg`, add `agent_run=(sys.executable, "-m", "flocc.agentrun"),` to the `replace(...)` call (`import sys`), and remove `claude_json=...` and `installed_plugins_file=...` from it.

In `tests/test_sessions.py`: replace `test_clean_env_keeps_only_the_allowlist` with

```python
def test_the_pane_gets_flocc_agent_s_environment_and_nothing_else(monkeypatch):
    monkeypatch.setenv("SUDO_UID", "1000")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk")
    env = agent_env(CFG, {"FLOCC_PHASE": "impl"})
    assert env["HOME"] == "/home/flocc-agent" and env["USER"] == "flocc-agent"
    assert env["PATH"].startswith("/opt/flocc/bin:/home/flocc-agent/.local/bin:")
    assert env["FLOCC_PHASE"] == "impl"
    assert not {"SUDO_UID", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} & set(env)
```

(import `agent_env` instead of `clean_env`); in `test_argv_wraps_claude_in_a_clean_env` change `CFG.agent_gh_token_file` to `CFG.agent_token_file`; delete `test_unlisted_plugins_are_disabled` (now in `test_agentrun.py`); drop `disabled_plugins=` from any remaining `claude_argv` call.

In `tests/test_supervisor.py`, add:

```python
def test_launch_hands_the_session_its_files_and_clears_the_old_markers(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    d = state_dir(rt.cfg, "NF-1")
    (d / "context.md").write_text("CTX")
    (d / "last-summary.md").write_text("tests for A")
    supervise(rt, current(rt))
    (start,) = rt.sessions.starts
    assert start["state"] == agent_dir(rt.cfg, "NF-1")
    assert start["files"]["context.md"] == "CTX"
    assert start["files"]["last-summary.md"] == "tests for A"
    assert json.loads(start["files"]["tester.json"]) == {
        "base_sha": "sha-base",
        "tester_sha": "sha-tests",
    }
    assert {"done.json", "idle", "status.json", "review.md"} <= set(start["clear"])
    env = start["argv"]
    assert "FLOCC_STATE=" + str(agent_dir(rt.cfg, "NF-1")) in env
    assert not any(a.startswith("FLOCC_CONFIG=") for a in env)
    assert "--remote-control" in env
```

(import `agent_dir` from `flocc.context`), and in `test_failed_start_puts_the_markers_back` change `def down(name, cwd, argv):` to `def down(name, cwd, argv, **kw):`.

In `tests/test_cli.py`, replace the `tester_claim` helper's last three lines (`claim = make(...)` through `ledger.insert_claim(...)`) with:

```python
    state = Path(os.environ["FLOCC_STATE"])
    (state / "tester.json").write_text(json.dumps({"base_sha": base, "tester_sha": tester}))
    os.chdir(wt)
```

and add:

```python
def test_tests_amended_without_a_tester_is_refused(claims_env, tmp_path, capsys):
    assert main(["tests-amended", "a.py", "--reason", "why"]) == 1
    assert "no tester ran" in capsys.readouterr().err
```

(`claims_env` stays; its `FLOCC_LEDGER` is now unused and harmless).

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_agentrun.py tests/test_sessions.py tests/test_supervisor.py tests/test_cli.py -k "agentrun or wrapper or sudoers or handoff or plugins or client or pane or launch_hands or tests_amended"`
Expected: FAIL (`No module named 'flocc.agentrun'`).

- [ ] **Step 3: The wrapper and its sudoers rule**

`deploy/flocc-agent-run`:

```sh
#!/bin/sh
# Root-owned at /usr/local/libexec/flocc-agent-run. flocc's one sudoers rule names only this file,
# run as flocc-agent; it passes a fixed set of subcommands on to flocc-agent.
set -eu
umask 027
case "${1:-}" in
  alive|attach|capture|kill|send|start) ;;
  *)
    echo "flocc-agent-run: unknown subcommand: ${1:-none}" >&2
    exit 2
    ;;
esac
PATH="/opt/flocc/bin:$HOME/.local/bin:$HOME/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin"
export PATH
# The service's working directory is usually one flocc-agent cannot enter.
[ -x . ] || cd "$HOME"
exec /opt/flocc/bin/flocc-agent "$@"
```

`chmod 755 deploy/flocc-agent-run` (git keeps the mode).

`deploy/flocc-agent.sudoers`:

```
# Written by flocc setup. flocc reaches flocc-agent only through this wrapper (d176).
flocc ALL=(flocc-agent) NOPASSWD: /usr/local/libexec/flocc-agent-run
```

- [ ] **Step 4: Write `src/flocc/agentrun.py`**

```python
"""flocc-agent: what flocc-agent-run runs as flocc-agent. A subcommand that takes a request reads
it as one JSON object on stdin and prints its answer as one JSON object on stdout."""

import json
import os
import re
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

from flocc.sessions import SOCKET, Sessions, installed_plugins, trust_folders

SUBCOMMANDS = ("alive", "attach", "capture", "kill", "send", "start")
_NAME = re.compile(r"^[A-Z][A-Z0-9]*-[0-9]+(?:-test|-review)?$")
_FILE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
ATTACH_HINT = "read-only · Ctrl-b d detaches, the session keeps running"


class BadRequest(ValueError):
    pass


def _home() -> Path:
    return Path.home()


def under_home(raw) -> Path:
    if not isinstance(raw, str) or not raw:
        raise BadRequest(f"not a path: {raw!r}")
    path = Path(os.path.abspath(raw))
    if not path.is_relative_to(_home()):
        raise BadRequest(f"{raw} is outside {_home()}")
    return path


def session_name(raw) -> str:
    if not isinstance(raw, str) or not _NAME.match(raw):
        raise BadRequest(f"not a session name: {raw!r}")
    return raw


def _file(raw) -> str:
    if not isinstance(raw, str) or not _FILE.match(raw):
        raise BadRequest(f"not a state file name: {raw!r}")
    return raw


def write_token(token) -> None:
    if not isinstance(token, str):
        raise BadRequest("token is not a string")
    path = _home() / ".config" / "flocc" / "gh-agent.token"
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(token.strip() + "\n")
    os.replace(tmp, path)


def apply_handoff(state: Path, files: dict[str, str], clear) -> dict[str, bytes]:
    """Removes clear's files from state, then writes files there; returns what it removed."""
    names = [_file(n) for n in clear]
    for name in files:
        _file(name)
    state.mkdir(parents=True, exist_ok=True)
    saved = {}
    for name in names:
        path = state / name
        if path.is_file() and not path.is_symlink():
            saved[name] = path.read_bytes()
        path.unlink(missing_ok=True)
    for name, text in files.items():
        tmp = state / f".{name}.tmp"
        tmp.write_text(text)
        os.replace(tmp, state / name)
    return saved


def restore(state: Path, saved: dict[str, bytes]) -> None:
    for name, content in saved.items():
        (state / name).write_bytes(content)


def with_disabled_plugins(argv: list[str], allowed) -> list[str]:
    """argv with every installed plugin outside allowed turned off in its --settings JSON."""
    if allowed is None or "--settings" not in argv:
        return argv
    listed = _home() / ".claude" / "plugins" / "installed_plugins.json"
    disabled = [p for p in installed_plugins(listed) if p not in allowed]
    if not disabled:
        return argv
    i = argv.index("--settings") + 1
    settings = json.loads(argv[i])
    settings["enabledPlugins"] = {p: False for p in disabled}
    return [*argv[:i], json.dumps(settings), *argv[i + 1 :]]


def start(req: dict) -> dict:
    name = session_name(req["name"])
    cwd, state = under_home(req["cwd"]), under_home(req["state"])
    write_token(req["token"])
    saved = apply_handoff(state, dict(req.get("files") or {}), req.get("clear") or [])
    argv = with_disabled_plugins([str(a) for a in req["argv"]], req.get("plugins"))
    trust = [cwd, *(under_home(p) for p in req.get("trust") or [])]
    trust_folders(_home() / ".claude.json", trust)
    try:
        Sessions().start(name, cwd, argv)
    except BaseException:
        # Without its marker the next tick would take the step for a crash instead of repeating
        # it: a tester rerun on its own commits, or a fix started without the check output.
        restore(state, saved)
        raise
    return {}


def send(req: dict) -> dict:
    Sessions().send(session_name(req["name"]), str(req["text"]))
    return {}


def capture(req: dict) -> dict:
    return {"text": Sessions().capture(session_name(req["name"]), int(req["lines"]))}


def attach(name: str) -> int:
    print(f"Attaching {name} {ATTACH_HINT}", file=sys.stderr)
    argv = ["tmux", "-L", SOCKET, "attach", "-r", "-t", f"={name}", ";"]
    os.execvp("tmux", [*argv, "display-message", "-d", "15000", ATTACH_HINT])


HANDLERS: dict[str, Callable[[dict], dict]] = {"start": start, "send": send, "capture": capture}


def _request() -> dict:
    data = json.loads(sys.stdin.read() or "{}")
    if not isinstance(data, dict):
        raise BadRequest("the request is not a JSON object")
    return data


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    os.umask(0o027)
    if not argv or argv[0] not in SUBCOMMANDS:
        print(f"flocc-agent: unknown subcommand: {argv[0] if argv else 'none'}", file=sys.stderr)
        return 2
    sub, args = argv[0], argv[1:]
    try:
        if sub == "alive":
            return 0 if Sessions().alive(session_name(args[0])) else 1
        if sub == "kill":
            Sessions().kill(session_name(args[0]))
            return 0
        if sub == "attach":
            return attach(session_name(args[0]))
        out = HANDLERS[sub](_request())
    except (BadRequest, KeyError, IndexError, TypeError, ValueError) as exc:
        print(f"flocc-agent {sub}: bad request: {exc}", file=sys.stderr)
        return 2
    except subprocess.CalledProcessError as exc:
        print(f"flocc-agent {sub}: {exc} {exc.stderr or ''}".strip(), file=sys.stderr)
        return 1
    print(json.dumps(out))
    return 1 if "error" in out else 0


if __name__ == "__main__":
    raise SystemExit(main())
```

In `pyproject.toml`, under `[project.scripts]`, add `flocc-agent = "flocc.agentrun:main"`.

- [ ] **Step 5: Write `src/flocc/agent.py`**

```python
import json
import os
import subprocess
from pathlib import Path

from flocc.config import Config


class AgentError(subprocess.SubprocessError):
    pass


class Agent:
    """The service's side of flocc-agent-run: every call is one sudo to flocc-agent."""

    def __init__(self, cfg: Config, runner=subprocess.run):
        self.cfg = cfg
        self._run = runner

    def run(
        self, sub: str, *args: str, payload: dict | None = None, timeout: float = 60
    ) -> subprocess.CompletedProcess:
        # sudo -H replaces HOME with flocc-agent's. Without sudo (tests, a one-user dev host) this
        # keeps flocc-agent's files under agent_home instead of the caller's home.
        env = {**os.environ, "HOME": str(self.cfg.agent_home)}
        return self._run(
            [*self.cfg.agent_run, sub, *args],
            input="" if payload is None else json.dumps(payload),
            capture_output=True,
            text=True,
            env=env,
            timeout=timeout,
            check=False,
        )

    def call(self, sub: str, *args: str, payload: dict | None = None, timeout: float = 60) -> dict:
        """The subcommand's JSON answer; an answer with "error" is returned, anything else raises."""
        out = self.run(sub, *args, payload=payload, timeout=timeout)
        why = (out.stderr or "").strip()[-1500:]
        if out.returncode not in (0, 1):
            raise AgentError(f"flocc-agent-run {sub} exited {out.returncode}: {why}")
        try:
            data = json.loads(out.stdout or "null")
        except ValueError as exc:
            raise AgentError(f"flocc-agent-run {sub} answered no JSON: {why}") from exc
        if not isinstance(data, dict) or (out.returncode == 1 and "error" not in data):
            raise AgentError(f"flocc-agent-run {sub} failed: {why}")
        return data

    def token(self) -> str:
        path = self.cfg.agent_gh_token_file
        return path.read_text().strip() if path.exists() else ""

    def sessions(self) -> "AgentSessions":
        return AgentSessions(self)


class AgentSessions:
    """flocc-agent's tmux sessions, with the surface supervisor and poller use."""

    def __init__(self, agent: Agent):
        self.agent = agent

    def start(self, name, cwd: Path, argv, *, state, files=None, clear=(), trust=(), plugins=None):
        self.agent.call(
            "start",
            payload={
                "name": name,
                "cwd": str(cwd),
                "argv": list(argv),
                "state": str(state),
                "files": dict(files or {}),
                "clear": list(clear),
                "trust": [str(t) for t in trust],
                "plugins": None if plugins is None else list(plugins),
                "token": self.agent.token(),
            },
        )

    def alive(self, name: str) -> bool:
        out = self.agent.run("alive", name)
        if out.returncode not in (0, 1):
            raise AgentError(f"flocc-agent-run alive exited {out.returncode}: {out.stderr.strip()}")
        return out.returncode == 0

    def kill(self, name: str) -> None:
        self.agent.run("kill", name)

    def send(self, name: str, text: str) -> None:
        self.agent.call("send", payload={"name": name, "text": text})

    def capture(self, name: str, lines: int) -> str:
        return str(self.agent.call("capture", payload={"name": name, "lines": lines}).get("text", ""))
```

- [ ] **Step 6: Build sessions for flocc-agent**

In `src/flocc/sessions.py`, replace `_KEEP` and `clean_env` with:

```python
def agent_env(cfg: Config, extra: dict[str, str]) -> dict[str, str]:
    # The pane gets only these. Left out on purpose: sudo's SUDO_* (Claude Code refuses
    # bypassPermissions under sudo), ANTHROPIC_API_KEY (it would bill the API), CLAUDE_CONFIG_DIR
    # (it would move the login away), and whatever the session that started the tick had.
    home = str(cfg.agent_home)
    path = f"{cfg.agent_flocc.parent}:{home}/.local/bin:{home}/.local/share/mise/shims"
    return {
        "HOME": home,
        "USER": cfg.agent_user,
        "PATH": f"{path}:/usr/local/bin:/usr/bin:/bin",
        "LANG": "C.UTF-8",
        "TERM": "xterm-256color",
        **extra,
    }
```

In `claude_argv`, delete the `disabled_plugins` parameter and the `if disabled_plugins:` block, and change `str(cfg.agent_gh_token_file),` to `str(cfg.agent_token_file),`.

In `src/flocc/config.py`, delete the fields `claude_json` and `installed_plugins_file` with their comments, remove them from `_PATH_KEYS`, and add them to `_LAYOUT`.

- [ ] **Step 7: Launch through the start request**

In `src/flocc/supervisor.py`: import `agent_env` (not `clean_env`) and `allowed_plugins` from `flocc.sessions`, drop `installed_plugins` and `trust_folders`; add beside the other names:

```python
TESTER = "tester.json"
# Service files a session reads, copied into its state directory at every start.
_HANDOFF = ("context.md", LAST_SUMMARY, TESTER, "review-input.md", "pr.diff", FIX_CONTEXT, CHECK_OUTPUT)
# A previous session's files that the new one would be taken to have written.
_CLEAR = ("done.json", "status.json", "idle", BACKGROUND, "review.md")
```

Replace `_disabled_plugins` with:

```python
def _allowed_plugins(rt) -> tuple[str, ...] | None:
    cfg = rt.cfg
    return allowed_plugins(cfg.agent_plugins_file or cfg.plugin_dir.parent / "deploy" / "agent-plugins.txt")
```

Replace `launch` with:

```python
def launch(rt, claim: Claim, phase: str, extra: str = "", reset: tuple[str, ...] = ()) -> None:
    token = rt.cfg.agent_gh_token_file
    if not token.exists() or not token.read_text().strip():
        raise LaunchError(f"{token} is missing or empty; agents get their GitHub token from it")
    key = claim.issue_key
    d = state_dir(rt.cfg, key)
    # Before the start below: the outgoing session's status file is the freshest usage reading.
    _record_usage(rt, key, phase, "start")
    name = session_name(key, phase)
    agent = agent_dir(rt.cfg, key)
    env = agent_env(
        rt.cfg,
        {
            "FLOCC_STATE": str(agent),
            "FLOCC_PHASE": phase,
            "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH": "1",
            "CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS": "1",
        },
    )
    settings = settings_from_json(claim.settings_json)
    if phase == "review":
        settings = replace(settings, model=rt.cfg.review_model)
    if settings.effort is None:
        settings = replace(settings, effort=rt.cfg.effort_for(phase))
    prompt = phase_prompt(phase, key, extra)
    argv = claude_argv(rt.cfg, name, settings, prompt, env, rt.claude_exe, rt.exe)
    if claim.tester_sha is not None:
        (d / TESTER).write_text(
            json.dumps({"base_sha": claim.base_sha, "tester_sha": claim.tester_sha})
        )
    else:
        (d / TESTER).unlink(missing_ok=True)
    files = {n: (d / n).read_text() for n in _HANDOFF if (d / n).exists()}
    rt.sessions.start(
        name,
        Path(claim.worktree),
        argv,
        state=agent,
        files=files,
        clear=[*_CLEAR, *_HANDOFF, *reset],
        # The worktree is trusted as the session's cwd; trust may also be keyed on its clone.
        trust=[rt.cfg.agent_home / "src" / claim.repo],
        plugins=_allowed_plugins(rt),
    )
```

Two texts the session reads name paths, which must be the paths it can open: in `_start_review`, change `f"PR: {url}\n\nThe diff is in {d / 'pr.diff'}.\n\n"` to `f"PR: {url}\n\nThe diff is in {agent_dir(rt.cfg, key) / 'pr.diff'}.\n\n"`; in `_finish_phase`'s check-fix branch, change `extra += f"\n\nThis is an excerpt; the full output is in {log}."` to `extra += f"\n\nThis is an excerpt; the full output is in {agent_dir(rt.cfg, key) / CHECK_OUTPUT}."`. Both files reach that directory through `_HANDOFF`.

In `src/flocc/poller.py::_start`, delete the `if tester_sha is None: (d / "tests-amended.json").unlink(...)` lines and change the `launch(rt, claim, phase)` call to:

```python
        # A run with no tester commit has no amendments; one resuming at the executor keeps them.
        launch(rt, claim, phase, reset=("tests-amended.json",) if tester_sha is None else ())
```

- [ ] **Step 8: The runtime and the fakes**

In `src/flocc/runtime.py`, add `agent: Any = None` as the last field. In `src/flocc/tick.py::build_runtime`: replace `exe, claude_exe = _which("flocc"), _which("claude")` with `exe, claude_exe = str(cfg.agent_flocc), str(cfg.agent_claude)`, add `agent = Agent(cfg)` (import from `flocc.agent`), pass `sessions=agent.sessions(),` and `agent=agent,` to `Runtime(...)`, and delete `_which` if nothing else uses it (`grep -rn _which src tests`).

In `tests/fakes.py`, replace `FakeSessions.start` with:

```python
    starts: list = field(default_factory=list)

    def start(self, name, cwd, argv, *, state=None, files=None, clear=(), trust=(), plugins=None):
        # The real file handling, so tests see what flocc-agent would find in its directory.
        if state is not None:
            apply_handoff(Path(state), dict(files or {}), clear)
        self.started.append((name, argv))
        self.starts.append(
            {
                "name": name,
                "cwd": cwd,
                "argv": argv,
                "state": state,
                "files": dict(files or {}),
                "clear": list(clear),
                "trust": list(trust),
                "plugins": plugins,
            }
        )
        self.alive_names.add(name)
```

(declare `starts` with the other fields at the top of the dataclass; import `from pathlib import Path` and `from flocc.agentrun import apply_handoff`).

- [ ] **Step 9: `tests-amended` from `tester.json`**

In `src/flocc/cli.py`, delete `_ledger` and replace the start of `cmd_tests_amended` through the `tester_files = ...` line with:

```python
def cmd_tests_amended(args) -> int:
    from flocc.publisher import AMENDED
    from flocc.worktree import changed_files, git

    state = _state_dir()
    try:
        tester = json.loads((state / "tester.json").read_text())
        base_sha, tester_sha = str(tester["base_sha"]), str(tester["tester_sha"])
    except (OSError, ValueError, KeyError, TypeError):
        print(
            f"tests-amended: no tester ran for {state.name}, so it has no tester files",
            file=sys.stderr,
        )
        return 1
    top = Path(git("rev-parse", "--show-toplevel", cwd=Path.cwd()))
    tester_files = changed_files(top, base_sha, tester_sha)
```

Remove `ledger` from the `from flocc import ...` line if nothing else in `cli.py` uses it (`cmd_release` and `cmd_canary` do; keep it then).

- [ ] **Step 10: Run the tests**

Run: `uv run pytest -q && uv run pyrefly check && uvx --from shellcheck-py shellcheck deploy/flocc-agent-run`
Expected: PASS; shellcheck prints nothing.

- [ ] **Step 11: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add deploy/flocc-agent-run deploy/flocc-agent.sudoers pyproject.toml src tests
git commit -s -m "Start sessions as flocc-agent through flocc-agent-run"
```

---

### Task 10: Worktrees, the check and the push through flocc-agent

`prepare` and `check` join the wrapper. flocc-agent owns its clone and worktrees; the repo's `./setup` and check, on a branch or on the base, run as flocc-agent. The service fetches the branch from the worktree into `refs/flocc/<KEY>` of its own clone, runs the tester guard and the trailer check there, and pushes from there with the publish token. flocc never runs another git command inside a flocc-agent repo.

**Files:**
- Modify: `src/flocc/agentrun.py` (`prepare`, `check`, `_clone`, `CREDENTIAL_HELPER`), `deploy/flocc-agent-run` (case list)
- Modify: `src/flocc/agent.py` (`prepare`, `check_worktree`, `check_base`, `setup_base`)
- Modify: `src/flocc/worktree.py` (`agent_ref`, `fetch_agent_branch`, `recover_tests(head=)`)
- Modify: `src/flocc/publisher.py` (`publish`, `check_worktree`, guard helpers take `head`, `pr_view`, `comment_on_pr`)
- Modify: `src/flocc/poller.py` (`_start`, `restart_timed_out`)
- Modify: `src/flocc/supervisor.py`, `src/flocc/rebase.py`, `src/flocc/cli.py` (`rt.git_head(claim)`; `cmd_canary`)
- Modify: `src/flocc/tick.py` (`build_runtime`), `src/flocc/runtime.py` (`git_head` comment)
- Modify: `tests/fakes.py` (`FakeAgent`)
- Test: `tests/test_agentrun.py`, `tests/test_publisher.py`, `tests/test_poller.py`, `tests/test_worktree.py`, `tests/test_cli.py`

**Interfaces:**
- Consumes: Task 9's `Agent.call`, `agentrun.HANDLERS`, `write_token`, `under_home`.
- Produces:
  - wrapper subcommands `prepare` and `check`; `agentrun.CREDENTIAL_HELPER` (the same string as `credentialHelper` in `installer/internal/steps/config.go`).
  - `Agent.prepare(repo_name: str, key: str) -> tuple[Path, str, str]` raising `subprocess.CalledProcessError(cmd=["./setup"])`, `subprocess.TimeoutExpired`, `worktree.DirtyWorktree`, `worktree.BranchDiverged` or `CalledProcessError(cmd=["git"])`, each with attribute `at_base: str | None`; `Agent.check_worktree(claim, repo, timeout_s) -> dict`; `Agent.check_base(repo_name) -> tuple[str, PublishResult | None]`; `Agent.setup_base(repo_name) -> tuple[int | None, str, str]`.
  - `worktree.agent_ref(key) -> str` (`refs/flocc/<KEY>`), `worktree.fetch_agent_branch(repo_path, worktree, branch, key) -> str`, `worktree.recover_tests(repo_path, base_branch, key, head="HEAD")`.
  - `publisher.check_worktree(command, wt, timeout_s) -> dict` (`{"status": "ok" | "dirty" | "failed" | "timeout", "output": str, "head": str}`); `publish(..., check=None)` where `check(claim, repo, timeout_s) -> dict`.
  - `Runtime.git_head(claim) -> str`: the branch's head as fetched into the service clone.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_agentrun.py`:

```python
from flocc.publisher import check_worktree
from flocc.worktree import BranchDiverged, DirtyWorktree, agent_ref, fetch_agent_branch, git


def agent_cfg(tmp_cfg, origin, tmp_path):
    # The origin fixture's clone is tmp_path/src, so flocc-agent's home goes beside it.
    home = tmp_path / "agent"
    return replace(
        tmp_cfg,
        agent_home=home,
        worktree_root=home / "worktrees",
        agent_state_root=home / "state",
        repos={"backend": replace(tmp_cfg.repos["backend"], path=origin)},
    )


def test_prepare_clones_for_flocc_agent_and_adds_the_worktree(tmp_cfg, origin, tmp_path):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    path, branch, base = Agent(cfg).prepare("backend", "NF-7")
    home = cfg.agent_home
    assert path == (home / "worktrees").resolve() / "NF-7" and branch == "agent/nf-7"
    assert (home / "src" / "backend" / ".git").is_dir()
    assert (path / ".setup-ran").exists()
    assert (home / "state" / "NF-7" / "hooks" / "commit-msg").exists()
    assert (home / ".config" / "flocc" / "gh-agent.token").read_text() == "t\n"


def test_a_failing_setup_comes_back_as_the_setup_error(tmp_cfg, origin, tmp_path):
    (origin / "setup").write_text("#!/bin/sh\necho broken >&2\nexit 3\n")
    git("commit", "-qam", "break setup", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    with pytest.raises(subprocess.CalledProcessError) as exc:
        Agent(cfg).prepare("backend", "NF-7")
    assert exc.value.cmd == ["./setup"] and "broken" in exc.value.stderr
    assert exc.value.at_base == git("rev-parse", "origin/main", cwd=origin)


def test_check_reports_dirty_failed_and_ok(origin):
    assert check_worktree("true", origin, 30)["status"] == "ok"
    assert check_worktree("false", origin, 30)["status"] == "failed"
    (origin / "stray.txt").write_text("x")
    dirty = check_worktree("true", origin, 30)
    assert dirty["status"] == "dirty" and "stray.txt" in dirty["output"]


def test_the_branch_is_fetched_into_the_service_clone(origin, tmp_path):
    service = tmp_path / "service"
    subprocess.run(["git", "clone", "-q", str(tmp_path / "origin.git"), str(service)], check=True)
    git("checkout", "-qb", "agent/nf-1", cwd=origin)
    (origin / "b.txt").write_text("b\n")
    git("add", ".", cwd=origin)
    git("commit", "-qm", "work", cwd=origin)
    head = fetch_agent_branch(service, origin, "agent/nf-1", "NF-1")
    assert head == git("rev-parse", "HEAD", cwd=origin)
    assert git("rev-parse", agent_ref("NF-1"), cwd=service) == head
```

(add `from dataclasses import replace` to the imports of `tests/test_agentrun.py`).

Add to `tests/test_publisher.py`:

```python
def test_the_push_goes_from_the_service_clone_with_the_check_run_elsewhere(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    seen = []

    def remote_check(c, r, timeout_s):
        seen.append((c.issue_key, r.check))
        return check_worktree(r.check, Path(c.worktree), timeout_s)

    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), check=remote_check)
    assert result.status == "opened" and seen == [("NF-5", "true")]
    assert git("rev-parse", "refs/flocc/NF-5", cwd=origin) == git("rev-parse", "HEAD", cwd=path)


def test_a_branch_that_moves_during_the_check_is_not_pushed(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)

    def moving_check(c, r, timeout_s):
        (path / "late.txt").write_text("x")
        git("add", ".", cwd=path)
        git("commit", "-qm", "late", cwd=path)
        return check_worktree("true", path, timeout_s)

    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), check=moving_check)
    assert result.status == "check_failed" and "moved while the check ran" in result.detail
```

(import `check_worktree` from `flocc.publisher` and `Path` from `pathlib`).

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_agentrun.py tests/test_publisher.py -k "prepare or setup_error or check_reports or fetched or service_clone or moves_during"`
Expected: FAIL (`Agent` has no `prepare`; `check_worktree` does not exist).

- [ ] **Step 3: Fetch into the service clone**

In `src/flocc/worktree.py`, add after `git`:

```python
def agent_ref(key: str) -> str:
    return f"refs/flocc/{key}"


def fetch_agent_branch(repo_path: Path, worktree: Path, branch: str, key: str) -> str:
    """Copies branch from flocc-agent's worktree into repo_path's agent_ref(key); its head."""
    ref = agent_ref(key)
    # upload-pack is built to serve an untrusted repository (git-upload-pack(1), SECURITY), and
    # without safe.directory git refuses flocc-agent's repo as dubious ownership. flocc runs no
    # other git command inside a repo flocc-agent owns.
    git(
        "-c",
        "safe.directory=*",
        "fetch",
        "-q",
        "--no-tags",
        str(worktree),
        f"+refs/heads/{branch}:{ref}",
        cwd=repo_path,
    )
    return git("rev-parse", ref, cwd=repo_path)
```

Change `recover_tests` to take `head: str = "HEAD"` as its last parameter and use it in place of the two `"HEAD"` literals (`merge-base ... HEAD` and `f"{fork}..HEAD"`).

- [ ] **Step 4: Publish from the service clone**

In `src/flocc/publisher.py`:
- Import `agent_ref`, `fetch_agent_branch` from `flocc.worktree` and `from collections.abc import Callable`.
- Give `_without_merged_base` a `head: str = "HEAD"` parameter used in its `merge-base` call, and `weakens_test` a `head: str = "HEAD"` parameter used as `f"{since}..{head}"`.
- Change `_tests_guard` (Task 5) to take `repo_path: Path, ref: str` instead of `wt: Path`, running `changed_files(repo_path, claim.base_sha, claim.tester_sha)`, `_without_merged_base(repo_path, ..., repo.base_branch, ref)`, `changed_files(repo_path, since, ref)` and `weakens_test(repo_path, since, path, ref)`.
- Add above `publish`:

```python
def check_worktree(command: str, wt: Path, timeout_s: float) -> dict:
    """The repo's check in wt: status ok, dirty, failed or timeout, its output, and the head."""
    head = git("rev-parse", "HEAD", cwd=wt)
    # Only commits are pushed and guarded; a check on the working tree would pass on files the PR
    # leaves out.
    dirty = git("status", "--porcelain", cwd=wt)
    if dirty:
        return {"status": "dirty", "output": dirty, "head": head}
    code, out = run_bounded(shlex.split(command), wt, timeout_s)
    status = "timeout" if code is None else "ok" if code == 0 else "failed"
    return {"status": status, "output": out, "head": git("rev-parse", "HEAD", cwd=wt)}
```

- In `publish`, add the parameter `check: Callable[[Claim, RepoConfig, float], dict] | None = None` and replace everything from `wt = Path(claim.worktree)` down to (not including) the `# Commits made with --no-verify` comment with:

```python
    wt = Path(claim.worktree)
    if claim.base_sha is None:
        raise ValueError(f"{claim.issue_key} has no recorded base commit")
    head = fetch_agent_branch(repo.path, wt, claim.branch, claim.issue_key)
    ref = agent_ref(claim.issue_key)
    # No tester ran: there are no tester files to guard.
    if claim.tester_sha is not None:
        touched = _tests_guard(claim, repo, repo.path, ref, accepted_sha, amended)
        if touched is not None:
            return touched
    ran = check(claim, repo, check_timeout_s) if check else check_worktree(repo.check, wt, check_timeout_s)
    out = str(ran.get("output", ""))
    if ran["status"] == "dirty":
        detail = f"Uncommitted changes; commit them or remove them, then finish again:\n{out}"
        return PublishResult("check_failed", detail[:_TAIL])
    if ran["status"] == "timeout":
        detail = f"`{repo.check}` timed out after {check_timeout_s}s\n\n{out[-_TAIL:]}"
        return PublishResult("check_failed", detail.strip(), log=out)
    if ran["status"] != "ok":
        return PublishResult("check_failed", failure_excerpt(out), log=out)
    if ran["head"] != head:
        return PublishResult(
            "check_failed",
            "The branch moved while the check ran: commit nothing during the check, then finish"
            " again.",
        )
```

- In the untrailed `git(...)` call, change `f"{claim.base_sha}..{claim.branch}"` to `f"{claim.base_sha}..{ref}"` and `cwd=wt` to `cwd=repo.path`.
- In the push argv, replace `"-u",\n                "origin",\n                claim.branch,` with `"origin",\n                f"{ref}:refs/heads/{claim.branch}",`.
- Change `cwd=wt` to `cwd=repo.path` in the push, `gh pr list`, `gh pr edit` and `gh pr create` calls.
- In `pr_view`, replace the `cwd=...` line and its comment with `cwd=None,` (a full PR URL needs no checkout); in `comment_on_pr`, delete `cwd=claim.worktree,`.
- `run_check` stays; `check_at_base` uses it.

- [ ] **Step 5: `prepare` and `check` in the module and the wrapper**

In `deploy/flocc-agent-run`, change the case line to `  alive|attach|capture|check|kill|prepare|send|start) ;;`.

In `src/flocc/agentrun.py`, change `SUBCOMMANDS` to `("alive", "attach", "capture", "check", "kill", "prepare", "send", "start")`, add the imports

```python
import shutil

from flocc.config import RepoConfig
from flocc.publisher import check_at_base, check_worktree
from flocc.worktree import (
    BranchDiverged,
    DirtyWorktree,
    head_at_base,
    install_hook,
    prepare_worktree,
    setup_at_base,
)
```

and add before `HANDLERS`:

```python
_KEY = re.compile(r"^[A-Z][A-Z0-9]*-[0-9]+$")
_REPO = re.compile(r"^[A-Za-z0-9._-]+$")
# Read on every fetch, so a replaced token takes effect without touching the clone. The same
# string as credentialHelper in installer/internal/steps/config.go.
CREDENTIAL_HELPER = (
    '!f() { test "$1" = get && printf \'username=x-access-token\\npassword=%s\\n\''
    ' "$(cat "$HOME/.config/flocc/gh-agent.token")"; }; f'
)


def _key(raw) -> str:
    if not isinstance(raw, str) or not _KEY.match(raw):
        raise BadRequest(f"not an issue key: {raw!r}")
    return raw


def _clone(name, url) -> Path:
    if not isinstance(name, str) or not _REPO.match(name) or name in (".", ".."):
        raise BadRequest(f"not a repo name: {name!r}")
    clone = _home() / "src" / name
    if not (clone / ".git").exists():
        clone.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "-c", "credential.helper=", "-c", f"credential.helper={CREDENTIAL_HELPER}"]
            + ["clone", "-q", str(url), str(clone)],
            check=True,
            capture_output=True,
            text=True,
        )
        subprocess.run(
            ["git", "config", "--replace-all", "credential.helper", ""], cwd=clone, check=True
        )
        subprocess.run(
            ["git", "config", "--add", "credential.helper", CREDENTIAL_HELPER],
            cwd=clone,
            check=True,
        )
    return clone


def _clean_stale(root: Path) -> None:
    # Left by a killed tick; setup runs under the tick lock, so none is in use now.
    for pattern in (".uv-cache-*", ".canary-*"):
        for stale in root.glob(pattern):
            shutil.rmtree(stale, ignore_errors=True)


def _text(raw) -> str:
    if isinstance(raw, bytes):
        return raw.decode(errors="replace")
    return raw or ""


def _failed(kind: str, detail: str, at_base: str | None = None, **extra) -> dict:
    return {"error": kind, "detail": detail[-4000:], "at_base": at_base, **extra}


def prepare(req: dict) -> dict:
    key = _key(req["key"])
    write_token(req["token"])
    repo = RepoConfig(path=_clone(req["repo"], req["url"]), base_branch=str(req["base_branch"]))
    root = under_home(req["worktree_root"])
    _clean_stale(root)
    try:
        path, branch, base = prepare_worktree(
            repo, root, key, float(req["setup_timeout_s"]), bool(req["shared_uv_cache"])
        )
    except DirtyWorktree as exc:
        return _failed("dirty", str(exc), head_at_base(repo, root.resolve() / key))
    except BranchDiverged as exc:
        return _failed("diverged", str(exc))
    except subprocess.TimeoutExpired as exc:
        return _failed("timeout", _text(exc.stderr), timeout=exc.timeout)
    except subprocess.CalledProcessError as exc:
        if exc.cmd == ["./setup"]:
            at_base = head_at_base(repo, root.resolve() / key)
            return _failed("setup", _text(exc.stderr), at_base, code=exc.returncode)
        return _failed("git", _text(exc.stderr) or str(exc), code=exc.returncode)
    state = under_home(req["state_root"]) / key
    install_hook(path, state / "hooks", shutil.which("flocc") or "flocc", bool(req["trailer"]))
    return {"path": str(path), "branch": branch, "base": base}


def check(req: dict) -> dict:
    mode = req["mode"]
    if mode == "worktree":
        return check_worktree(str(req["command"]), under_home(req["worktree"]), float(req["timeout_s"]))
    write_token(req["token"])
    clone = _clone(req["repo"], req["url"])
    repo = RepoConfig(path=clone, base_branch=str(req["base_branch"]), check=str(req["command"]))
    root = under_home(req["worktree_root"])
    _clean_stale(root)
    setup_s, shared = float(req["setup_timeout_s"]), bool(req["shared_uv_cache"])
    if mode == "base-check":
        sha, failure = check_at_base(repo, root, setup_s, float(req["check_timeout_s"]), shared)
        found = None if failure is None else {"detail": failure.detail, "log": failure.log}
        return {"sha": sha, "failure": found}
    if mode == "base-setup":
        code, out, sha = setup_at_base(repo, root, setup_s, shared)
        return {"sha": sha, "code": code, "output": out}
    raise BadRequest(f"unknown check mode {mode!r}")
```

and extend `HANDLERS` with `"prepare": prepare, "check": check`.

- [ ] **Step 6: The client's side**

In `src/flocc/agent.py`, add the imports `from flocc.config import trailer_enabled`, `from flocc.ledger import Claim`, `from flocc.publisher import PublishResult`, `from flocc.worktree import BranchDiverged, DirtyWorktree, git`, and these methods on `Agent`:

```python
    def _repo(self, name: str) -> tuple[dict, float]:
        repo = self.cfg.repos[name]
        url = git("remote", "get-url", "origin", cwd=repo.path)
        common = {
            "repo": name,
            "url": url,
            "base_branch": repo.base_branch,
            "worktree_root": str(self.cfg.worktree_root),
            "setup_timeout_s": self.cfg.setup_timeout_s,
            "shared_uv_cache": self.cfg.shared_uv_cache,
            "token": self.token(),
        }
        # A first clone of a big repo, then ./setup.
        return common, self.cfg.setup_timeout_s + 900

    def prepare(self, repo_name: str, key: str) -> tuple[Path, str, str]:
        common, timeout = self._repo(repo_name)
        payload = {
            **common,
            "key": key,
            "state_root": str(self.cfg.agent_state_root),
            "trailer": trailer_enabled(self.cfg, self.cfg.repos[repo_name]),
        }
        data = self.call("prepare", payload=payload, timeout=timeout)
        if "error" in data:
            raise _prepare_error(data)
        return Path(data["path"]), str(data["branch"]), str(data["base"])

    def check_worktree(self, claim: Claim, repo, timeout_s: float) -> dict:
        payload = {"mode": "worktree", "worktree": claim.worktree, "command": repo.check, "timeout_s": timeout_s}
        return self.call("check", payload=payload, timeout=timeout_s + 120)

    def check_base(self, repo_name: str) -> tuple[str, PublishResult | None]:
        common, timeout = self._repo(repo_name)
        payload = {
            **common,
            "mode": "base-check",
            "command": self.cfg.repos[repo_name].check,
            "check_timeout_s": self.cfg.check_timeout_s,
        }
        data = self.call("check", payload=payload, timeout=timeout + self.cfg.check_timeout_s)
        failure = data.get("failure")
        if failure is None:
            return str(data["sha"]), None
        return str(data["sha"]), PublishResult(
            "check_failed", str(failure["detail"]), log=str(failure["log"])
        )

    def setup_base(self, repo_name: str) -> tuple[int | None, str, str]:
        common, timeout = self._repo(repo_name)
        payload = {**common, "mode": "base-setup", "command": self.cfg.repos[repo_name].check}
        data = self.call("check", payload=payload, timeout=timeout)
        return data["code"], str(data["output"]), str(data["sha"])
```

and at module level:

```python
def _prepare_error(data: dict) -> subprocess.SubprocessError:
    kind, detail = data["error"], str(data.get("detail", ""))
    exc: subprocess.SubprocessError
    if kind == "setup":
        exc = subprocess.CalledProcessError(int(data.get("code") or 1), ["./setup"], stderr=detail)
    elif kind == "timeout":
        exc = subprocess.TimeoutExpired(["./setup"], float(data.get("timeout") or 0), stderr=detail)
    elif kind == "dirty":
        exc = DirtyWorktree(detail)
    elif kind == "diverged":
        exc = BranchDiverged(detail)
    else:
        exc = subprocess.CalledProcessError(int(data.get("code") or 1), ["git"], stderr=detail)
    # Set only when ./setup failed on a worktree still exactly at the base: main's failure.
    setattr(exc, "at_base", data.get("at_base"))
    return exc
```

- [ ] **Step 7: The service uses them**

In `src/flocc/tick.py::build_runtime` (import `agent_ref`, `fetch_agent_branch` from `flocc.worktree`):

```python
    def git_head(claim):
        repo = cfg.repos[claim.repo]
        return fetch_agent_branch(repo.path, Path(claim.worktree), claim.branch, claim.issue_key)

    def review_diff(claim):
        repo = cfg.repos[claim.repo]
        git_head(claim)
        return git("diff", f"origin/{repo.base_branch}...{agent_ref(claim.issue_key)}", cwd=repo.path)
```

pass `check=agent.check_worktree,` inside `publish_fn`'s `publish(...)` call, set `base_check=agent.check_base` (delete the local `base_check` function) and `git_head=git_head` in `Runtime(...)`. In `src/flocc/runtime.py`, change `git_head: Callable[[str], str]` to `git_head: Callable[[Claim], str]` with the comment `# The claim's branch head, fetched from flocc-agent's worktree into the service clone.`

Change `rt.git_head(claim.worktree)` to `rt.git_head(claim)` in `src/flocc/supervisor.py` (the tester step), `src/flocc/rebase.py` and `src/flocc/cli.py::cmd_release`.

In `src/flocc/poller.py::_start` (import `from flocc.agent import AgentError` is not needed: `AgentError` is a `SubprocessError`; import `agent_ref`, `fetch_agent_branch` from `flocc.worktree`; drop `head_at_base`, `install_hook`, `prepare_worktree` from that import):
- Replace the `prepare_worktree(...)` call inside the `try:` with:

```python
        path, branch, base = rt.agent.prepare(repo, issue.key)
        repo_path = cfg.repos[repo].path
        fetch_agent_branch(repo_path, path, branch, issue.key)
```

- Replace `base = head_at_base(cfg.repos[repo], cfg.worktree_root.resolve() / issue.key)` with `base = getattr(exc, "at_base", None)`.
- Delete the `install_hook(...)` call (flocc-agent's `prepare` installs the hooks).
- Change `contains(path, remembered[1], f"origin/{base_branch}")` to `contains(repo_path, remembered[1], f"origin/{base_branch}")`, `contains(path, remembered[1])` (both) to `contains(repo_path, remembered[1], agent_ref(issue.key))`, and `recover_tests(path, base_branch, issue.key)` to `recover_tests(repo_path, base_branch, issue.key, agent_ref(issue.key))`.

In `restart_timed_out`, replace `stamps = git("log", "--format=%ct", claim.branch, "--not", claim.base_sha, cwd=wt)` with:

```python
            repo_path = rt.cfg.repos[claim.repo].path
            fetch_agent_branch(repo_path, wt, claim.branch, key)
            ref = agent_ref(key)
            stamps = git("log", "--format=%ct", ref, "--not", claim.base_sha, cwd=repo_path)
```

In `src/flocc/cli.py::cmd_canary`, replace the `publisher.check_at_base(...)` call with `Agent(cfg).check_base(args.repo)` and the `worktree.setup_at_base(...)` call with `Agent(cfg).setup_base(args.repo)` (import `from flocc.agent import Agent`; drop the now unused `publisher`, `worktree` imports there). The `except (subprocess.SubprocessError, OSError)` around them already catches `AgentError`.

- [ ] **Step 8: The fakes and the tests that used the old calls**

In `tests/fakes.py`, add:

```python
@dataclass
class FakeAgent:
    cfg: object
    prepare_fn: object = None
    removed: list = field(default_factory=list)
    probe_result: dict = field(
        default_factory=lambda: {"read": {}, "write": {}, "tmux": True, "groups": []}
    )

    def prepare(self, name, key):
        repo = self.cfg.repos[name]
        return self.prepare_fn(
            repo,
            self.cfg.worktree_root,
            key,
            setup_timeout_s=self.cfg.setup_timeout_s,
            shared_uv_cache=self.cfg.shared_uv_cache,
        )

    def remove(self, key, keep_dirty=False):
        self.removed.append((key, keep_dirty))
        return {"removed": True, "dirty": False}

    def probe(self, read, write):
        return self.probe_result

    def claude_argv(self):
        return ["/opt/claude"]
```

In `tests/test_poller.py`:
- In the `rt` fixture, delete the three `monkeypatch.setattr` lines for `prepare_worktree`, `install_hook` and `recover_tests`, add `monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: None)` and `monkeypatch.setattr("flocc.poller.fetch_agent_branch", lambda *a: "sha0")` back, and pass `agent=FakeAgent(tmp_cfg, fake_prepare),` to `Runtime(...)` (import `FakeAgent`).
- Replace every `monkeypatch.setattr("flocc.poller.prepare_worktree", X)` with `rt.agent.prepare_fn = X`.
- Where a test monkeypatches `flocc.poller.head_at_base` to return `at_base`, delete that line and set `exc.at_base = at_base` on the exception its failing prepare raises.
- In the `contains` lambdas, change `ref="HEAD"` defaults to `ref=""` and `ref == "HEAD"` to `ref == "refs/flocc/NF-9"`.
- The two tests that run the real `prepare_worktree` and `recover_tests` also need `monkeypatch.setattr("flocc.poller.fetch_agent_branch", fetch_agent_branch)` (import it from `flocc.worktree`); their repo is the clone the worktree was added to, so the real fetch works.
- Tests of `restart_timed_out` that built a branch in a real worktree keep working when the repo in `rt.cfg.repos` is the clone that worktree belongs to; where a test's worktree is a bare directory, monkeypatch `flocc.poller.fetch_agent_branch` to `lambda *a: "sha"` and `flocc.poller.git` to return the stamps it wants.

In `tests/test_cli.py`, the canary tests that monkeypatched `flocc.publisher.check_at_base` or `flocc.worktree.setup_at_base` now monkeypatch `flocc.agent.Agent.check_base` (`lambda self, repo: (sha, failure)`) and `flocc.agent.Agent.setup_base` (`lambda self, repo: (code, out, sha)`). In `tests/test_worktree.py`, any `recover_tests` call keeps its three positional arguments.

- [ ] **Step 9: Point checks and panes at flocc-agent's Podman socket**

Testcontainers in a check, and any container a session starts, reach rootless Podman through `DOCKER_HOST`. `sudo` and the pane's `env -i` both drop `XDG_RUNTIME_DIR`, so `flocc-agent-run` sets all three variables itself, once per call for the checks it runs and in the pane's `env -i` list. A host without the socket gets none of them, so a check there fails on the missing Docker socket exactly as before.

Add to `tests/test_agentrun.py`:

```python
def test_container_env_points_at_the_podman_socket(tmp_path, monkeypatch):
    sock = tmp_path / "podman" / "podman.sock"
    sock.parent.mkdir()
    sock.touch()
    monkeypatch.setattr("flocc.agentrun._runtime_dir", lambda: tmp_path)
    assert agentrun.container_env() == {
        "XDG_RUNTIME_DIR": str(tmp_path),
        "DOCKER_HOST": f"unix://{sock}",
        "TESTCONTAINERS_RYUK_DISABLED": "true",
    }


def test_no_socket_means_no_container_env(tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.agentrun._runtime_dir", lambda: tmp_path)
    assert agentrun.container_env() == {}


def test_the_pane_gets_the_container_env_after_env_i(monkeypatch):
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    argv = ["/usr/bin/env", "-i", "HOME=/h", "claude", "--name", "NF-1"]
    assert agentrun.with_container_env(argv) == [
        "/usr/bin/env", "-i", "DOCKER_HOST=unix:///s", "HOME=/h", "claude", "--name", "NF-1",
    ]
    assert agentrun.with_container_env(["claude"]) == ["claude"]
```

In `src/flocc/agentrun.py`, add after `under_home`:

```python
def _runtime_dir() -> Path:
    return Path(f"/run/user/{os.getuid()}")


def container_env() -> dict[str, str]:
    sock = _runtime_dir() / "podman" / "podman.sock"
    if not sock.exists():
        return {}
    return {
        "XDG_RUNTIME_DIR": str(_runtime_dir()),
        "DOCKER_HOST": f"unix://{sock}",
        # Ryuk needs a privileged container, which rootless Podman does not give it.
        "TESTCONTAINERS_RYUK_DISABLED": "true",
    }


def with_container_env(argv: list[str]) -> list[str]:
    if argv[:2] != ["/usr/bin/env", "-i"]:
        return argv
    return [*argv[:2], *(f"{k}={v}" for k, v in container_env().items()), *argv[2:]]
```

In `start`, change `argv = with_disabled_plugins(...)` to `argv = with_container_env(with_disabled_plugins([str(a) for a in req["argv"]], req.get("plugins")))`. In `main`, after `os.umask(0o027)`, add `os.environ.update(container_env())`, so `prepare`'s `./setup` and `check`'s commands inherit it through `run_bounded`.

- [ ] **Step 10: Run the tests**

Run: `uv run pytest -q && uv run pyrefly check && uvx --from shellcheck-py shellcheck deploy/flocc-agent-run`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add deploy/flocc-agent-run src tests
git commit -s -m "Prepare, check and fetch through flocc-agent; push from the service clone"
```

---

### Task 11: Cleanup when the issue is done, canceled or released

Today `reap_worktrees` removes only the worktree of a finished claim whose issue is Done or Canceled, and keeps the claim and the state; `release` removes the worktree unless `--keep-worktree`; scratchpads are removed by the tick once a claim stops running. Now one `remove` subcommand removes the worktree, flocc-agent's state for the issue and the scratchpads it listed, the service removes its own state, and Done or Canceled also deletes the claim. A timed-out run's dirty worktree is still kept (d59).

**Files:**
- Modify: `src/flocc/agentrun.py` (`remove`, `scratch_path`), `deploy/flocc-agent-run`
- Modify: `src/flocc/agent.py` (`remove`)
- Modify: `src/flocc/poller.py` (`remove_issue`, `release_claim`, `reap_worktrees`; delete `scratch_root`, `_scratch_path`, `clean_scratchpads`)
- Modify: `src/flocc/tick.py` (drop `clean_scratchpads`)
- Test: `tests/test_agentrun.py`, `tests/test_poller.py`, `tests/test_cli.py`

**Interfaces:**
- Consumes: Task 10's `FakeAgent.remove`, `Agent.call`.
- Produces: wrapper subcommand `remove`; `Agent.remove(key, keep_dirty=False) -> dict` (`{"removed": bool, "dirty": bool}`); `agentrun.scratch_path(line) -> Path | None`; `poller.remove_issue(rt, claim, keep_dirty=False) -> bool`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_agentrun.py`:

```python
def test_remove_takes_the_worktree_its_state_and_its_scratchpads(tmp_cfg, origin, tmp_path):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    agent = Agent(cfg)
    path, _, _ = agent.prepare("backend", "NF-7")
    pad = Path(f"/tmp/claude-{os.getuid()}/flocc-test-{os.getpid()}/session/scratchpad")
    pad.mkdir(parents=True, exist_ok=True)
    state = cfg.agent_state_root / "NF-7"
    (state / "scratchpads").write_text(f"{pad}\n/etc\n")
    assert agent.remove("NF-7") == {"removed": True, "dirty": False}
    assert not path.exists() and not state.exists() and not pad.exists()
    pad.parent.parent.rmdir()


def test_remove_keeps_a_dirty_worktree_when_asked(tmp_cfg, origin, tmp_path):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    agent = Agent(cfg)
    path, _, _ = agent.prepare("backend", "NF-7")
    (path / "work.py").write_text("x\n")
    assert agent.remove("NF-7", keep_dirty=True) == {"removed": False, "dirty": True}
    assert path.exists()


def test_scratch_paths_outside_the_user_s_pads_are_refused():
    root = f"/tmp/claude-{os.getuid()}"
    assert agentrun.scratch_path(f"{root}/p/s/scratchpad") == Path(f"{root}/p/s/scratchpad")
    for line in ("/etc", f"{root}/p", f"{root}/p/s/../../../etc", ""):
        assert agentrun.scratch_path(line) is None
```

(`import os` in that file). Move the `_scratch_path` tests from `tests/test_poller.py` to `tests/test_agentrun.py` as `agentrun.scratch_path` tests, and delete the `clean_scratchpads` tests from `tests/test_poller.py`.

Add to `tests/test_poller.py`:

```python
def test_done_or_canceled_removes_everything_but_the_branch(rt):
    claim = make("NF-9", state="done")
    ledger.insert_claim(rt.con, claim)
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir(parents=True)
    (d / "pr-url").write_text("https://pr/9")
    reap_worktrees(rt)
    assert rt.agent.removed == [("NF-9", False)]
    assert ledger.get_claim(rt.con, "NF-9") is None and not d.exists()


def test_a_timed_out_run_keeps_its_dirty_worktree(rt):
    ledger.insert_claim(rt.con, make("NF-9", state="timed_out"))
    rt.agent.remove = lambda key, keep_dirty=False: {"removed": False, "dirty": True}
    reap_worktrees(rt)
    assert ledger.get_claim(rt.con, "NF-9") is not None


def test_release_removes_the_worktree_and_both_state_directories(rt):
    claim = make("NF-9")
    ledger.insert_claim(rt.con, claim)
    state_dir(rt.cfg, "NF-9").mkdir(parents=True)
    release_claim(rt, claim)
    assert rt.agent.removed == [("NF-9", False)] and not state_dir(rt.cfg, "NF-9").exists()
    ledger.insert_claim(rt.con, claim)
    release_claim(rt, claim, keep_worktree=True)
    assert rt.agent.removed == [("NF-9", False)]
```

(`PollLinear.state_types` already answers `completed`; import `release_claim` from `flocc.poller`). Delete the existing `reap_worktrees` tests that asserted `git worktree remove` calls or `.uv-cache-*`/`.canary-*` cleanup by the service (`grep -n "def test_.*reap\|uv-cache\|canary-" tests/test_poller.py`); flocc-agent's `prepare` and `check` now clear those.

In `tests/test_cli.py::test_release_clears_a_claim_whose_issue_and_worktree_are_gone`, pass `agent=FakeAgent(cfg)` to `Runtime(...)` and change the asserted output `"worktree" in captured.err` to check only `"Entity not found" in captured.err`.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_agentrun.py tests/test_poller.py -k "remove or scratch or done_or_canceled or timed_out_run or release_removes"`
Expected: FAIL.

- [ ] **Step 3: `remove` in the module and the wrapper**

In `deploy/flocc-agent-run`, change the case line to `  alive|attach|capture|check|kill|prepare|remove|send|start) ;;`; in `src/flocc/agentrun.py`, add `"remove"` to `SUBCOMMANDS` (sorted), import `has_uncommitted_changes` from `flocc.worktree`, and add:

```python
def scratch_path(line: str) -> Path | None:
    """A Claude Code scratchpad of this user's from a session's listing, or None."""
    if not line.strip():
        return None
    root = Path(f"/tmp/claude-{os.getuid()}")
    path = Path(line.strip())
    try:
        rel = path.resolve().relative_to(root.resolve())
    except ValueError:
        return None
    # A link or ".." in the listed path would make the delete land on another session's pad.
    if path != root / rel:
        return None
    # <project>/<session>/<scratchpad>: anything shorter is shared by other sessions.
    return path if len(rel.parts) >= 3 else None


def remove(req: dict) -> dict:
    key = _key(req["key"])
    wt = under_home(req["worktree_root"]) / key
    state = under_home(req["state_root"]) / key
    if wt.exists():
        try:
            if req.get("keep_dirty") and has_uncommitted_changes(wt):
                return {"removed": False, "dirty": True}
            common = subprocess.run(
                ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
                cwd=wt,
                capture_output=True,
                text=True,
                check=True,
            ).stdout.strip()
        except subprocess.CalledProcessError:
            common = ""
        if common:
            clone = Path(common).parent
            subprocess.run(
                ["git", "worktree", "remove", "--force", str(wt)],
                cwd=clone,
                capture_output=True,
                check=False,
            )
        shutil.rmtree(wt, ignore_errors=True)
        if common:
            subprocess.run(["git", "worktree", "prune"], cwd=clone, capture_output=True, check=False)
    listing = state / "scratchpads"
    if listing.is_file() and not listing.is_symlink():
        for line in listing.read_text().splitlines():
            if (pad := scratch_path(line)) is not None:
                shutil.rmtree(pad, ignore_errors=True)
    shutil.rmtree(state, ignore_errors=True)
    _prune_containers()
    return {"removed": True, "dirty": False}


def _prune_containers() -> None:
    # Only stopped ones: a running container may belong to another issue's check. Ryuk is off,
    # so a check killed mid-run leaves its containers behind until this runs.
    if container_env() and shutil.which("podman"):
        subprocess.run(["podman", "container", "prune", "-f"], capture_output=True, check=False, timeout=120)
```

and add `"remove": remove` to `HANDLERS`. In `tests/test_agentrun.py`, add a test that `remove` runs `podman container prune -f` when `container_env` is non-empty and `shutil.which("podman")` answers (monkeypatch both and `subprocess.run`), and runs nothing podman-related when `container_env` returns `{}`.

In `src/flocc/agent.py`, add:

```python
    def remove(self, key: str, keep_dirty: bool = False) -> dict:
        payload = {
            "key": key,
            "worktree_root": str(self.cfg.worktree_root),
            "state_root": str(self.cfg.agent_state_root),
            "keep_dirty": keep_dirty,
        }
        return self.call("remove", payload=payload, timeout=300)
```

- [ ] **Step 4: The service side**

In `src/flocc/poller.py`, delete `scratch_root`, `_scratch_path` and `clean_scratchpads`, add after `_comment_once`:

```python
def remove_issue(rt, claim: Claim, *, keep_dirty: bool = False) -> bool:
    """Removes the worktree and both state directories; the branch stays on GitHub. False when
    nothing was removed: a dirty worktree kept by keep_dirty, or flocc-agent did not answer."""
    try:
        out = rt.agent.remove(claim.issue_key, keep_dirty=keep_dirty)
    except (subprocess.SubprocessError, OSError) as exc:
        print(f"{claim.issue_key}: worktree and state not removed: {exc}", file=sys.stderr)
        return False
    if not out.get("removed"):
        return False
    shutil.rmtree(state_dir(rt.cfg, claim.issue_key), ignore_errors=True)
    return True
```

In `release_claim`, replace the `if not keep_worktree:` block with:

```python
    if not keep_worktree:
        remove_issue(rt, claim)
```

Replace `reap_worktrees` with:

```python
def reap_worktrees(rt) -> None:
    """Removes the worktree, the state and the claim of finished runs whose issue is closed."""
    for claim in ledger.finished_claims(rt.con):
        try:
            state = rt.linear.state_types([claim.issue_key])[claim.issue_key]
        except LinearError as exc:
            # A deleted issue can never be rerun, and asking again every tick would not change that.
            # Only Linear's entity error: a wrong-workspace key fails every issue with other text.
            if "entity not found" not in str(exc).lower():
                print(f"reap: {claim.issue_key} state not read: {exc}", file=sys.stderr)
                continue
            state = "canceled"
        # Anything else stays: the owner may move the issue to Todo to rerun, which reuses it.
        if state not in ("completed", "canceled"):
            continue
        # A run stopped by the wall clock may hold work nobody committed, new files included;
        # a finished one published, so what is left is disposable.
        if not remove_issue(rt, claim, keep_dirty=claim.state == "timed_out"):
            cause = f"{claim.issue_key}:worktree-kept"
            if not ledger.alert_seen(rt.con, cause, COMMENT_ONCE_S, rt.now()):
                print(f"reap: {claim.worktree} kept: uncommitted changes", file=sys.stderr)
                ledger.record_alert(rt.con, cause, 3, claim.issue_key, rt.now())
            continue
        ledger.delete_claim(rt.con, claim.issue_key)
        ledger.clear_issue_alerts(rt.con, claim.issue_key)
```

In `src/flocc/tick.py`, delete the `clean_scratchpads` import and its call with its comment; change the reap comment to `# Before poll, so the space it frees counts toward this tick's admission.` (unchanged text, kept).

- [ ] **Step 5: Run the tests**

Run: `uv run pytest -q && uv run pyrefly check && uvx --from shellcheck-py shellcheck deploy/flocc-agent-run`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add deploy/flocc-agent-run src tests
git commit -s -m "Remove the worktree and state when the issue closes or is released"
```

---

### Task 12: Status, peek, attach and doctor through the wrapper

The Claude login, the session list and the tmux server are flocc-agent's now. `claude` (a fixed list of `claude` invocations) and `probe` (what flocc-agent can reach) join the wrapper. Doctor gains the boundary check the spec asks for.

**Files:**
- Modify: `src/flocc/agentrun.py` (`claude`, `probe`), `deploy/flocc-agent-run`
- Modify: `src/flocc/agent.py` (`claude_argv`, `probe`)
- Modify: `src/flocc/tokens.py` (`claude_login`, `check_claude_login`)
- Modify: `src/flocc/status.py` (`collect`), `src/flocc/doctor.py`, `src/flocc/cli.py` (`cmd_status`, `_live_session`, `cmd_attach`, `cmd_doctor`)
- Create: `deploy/managed-settings.json`
- Test: `tests/test_agentrun.py`, `tests/test_tokens.py`, `tests/test_status.py`, `tests/test_doctor.py`, `tests/test_cli.py`

**Interfaces:**
- Consumes: Task 9's `Agent`, `AgentSessions`, `agentrun.session_name`.
- Produces: wrapper subcommands `claude` and `probe`; `agentrun.CLAUDE_ALLOWED`; `Agent.claude_argv() -> list[str]` (`[*agent_run, "claude"]`); `Agent.probe(read: list[str], write: list[str]) -> dict` (`{"read": {path: bool}, "write": {path: bool}, "tmux": bool, "groups": [str], "containers": bool}`); `doctor.MANAGED_SETTINGS = Path("/etc/claude-code/managed-settings.json")`, `doctor.managed_check(path=MANAGED_SETTINGS) -> Result`; `tokens.claude_login(claude: list[str], run=subprocess.run, timeout=30)`; `status.collect(cfg, sessions, ..., claude: list[str] | None = None)`; `doctor.agent_checks(cfg, config_path, agent) -> list[Result]`; `doctor.claude_checks(claude: list[str] | None, run=subprocess.run)`; `doctor.run_doctor(cfg, config_path)`, `doctor.collect_results(cfg, config_path, agent=None)`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_agentrun.py`:

```python
@pytest.mark.parametrize(
    "args",
    [["auth", "logout"], ["-p", "hi"], ["auth", "login", "--console"], ["plugin", "install", "x"]],
)
def test_claude_runs_only_the_listed_invocations(args, capsys):
    assert agentrun.main(["claude", *args]) == 2
    assert "not allowed" in capsys.readouterr().err


def test_probe_reports_what_flocc_agent_can_reach(tmp_path, monkeypatch):
    readable = tmp_path / "r"
    readable.write_text("x")
    request = {"read": [str(readable), str(tmp_path / "missing")], "write": [str(tmp_path)]}
    monkeypatch.setattr("sys.stdin", __import__("io").StringIO(json.dumps(request)))
    monkeypatch.setattr("flocc.agentrun._tmux_up", lambda: False)
    out = agentrun.probe(json.loads(json.dumps(request)))
    assert out["read"] == {str(readable): True, str(tmp_path / "missing"): False}
    assert out["write"] == {str(tmp_path): True} and out["tmux"] is False
    assert isinstance(out["groups"], list)
```

In `tests/test_tokens.py`, change every `claude_login("<exe>", ...)` call to pass a list, e.g. `claude_login(["/opt/claude"], run)`, and assert the runner got `["/opt/claude", "auth", "status"]`.

In `tests/test_doctor.py`, add (import `agent_checks`, `Result` from `flocc.doctor`, `FakeAgent` from `tests.fakes`):

```python
def test_the_boundary_check_fails_when_flocc_agent_can_read_a_secret(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)
    key = str(tmp_cfg.linear_key_file)
    agent.probe_result = {"read": {key: True}, "write": {}, "tmux": True, "groups": []}
    results = agent_checks(tmp_cfg, tmp_path / "c.toml", agent)
    (boundary,) = [r for r in results if r.name == "agent boundary"]
    assert boundary.status == "fail" and key in boundary.detail


def test_the_docker_group_fails_the_boundary(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)
    agent.probe_result = {"read": {"/x": False}, "write": {"/opt/flocc/plugin": False},
                          "tmux": False, "groups": ["flocc-agent", "docker"], "containers": False}
    by_name = {r.name: r for r in agent_checks(tmp_cfg, tmp_path / "c.toml", agent)}
    assert by_name["agent boundary"].status == "ok"
    assert by_name["agent docker"].status == "fail" and "root-equivalent" in by_name["agent docker"].detail
    assert by_name["containers"].status == "ok" and "no Podman socket" in by_name["containers"].detail
    assert by_name["tmux"].status == "fail"


def test_a_clean_agent_passes_every_line(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)
    agent.probe_result = {"read": {}, "write": {}, "tmux": True, "groups": ["flocc-agent"], "containers": True}
    by_name = {r.name: r for r in agent_checks(tmp_cfg, tmp_path / "c.toml", agent)}
    assert by_name["agent docker"].status == "ok"
    assert "Podman socket" in by_name["containers"].detail


def test_managed_settings_must_carry_the_guard_and_the_dialog_denies(tmp_path):
    path = tmp_path / "managed-settings.json"
    assert managed_check(path).status == "fail"
    path.write_text("{}")
    assert managed_check(path).status == "fail"
    shipped = Path(__file__).parent.parent / "deploy" / "managed-settings.json"
    path.write_text(shipped.read_text())
    assert managed_check(path).status == "ok"


def test_an_unanswered_wrapper_fails_the_agent_check(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)

    def silent(read, write):
        raise subprocess.SubprocessError("sudo: a password is required")

    agent.probe = silent
    (result,) = agent_checks(tmp_cfg, tmp_path / "c.toml", agent)
    assert result.status == "fail" and "sudoers" in result.detail
```

Update `tests/test_doctor.py` tests of `claude_checks` to pass a list (`["/opt/claude"]`) and of `tmux_check` to be deleted (the tmux line now comes from `agent_checks`). In `tests/test_status.py`, pass `claude=["/opt/claude"]` where a test passed a path string. In `tests/test_cli.py`, the `peek` and `attach` tests: monkeypatch `flocc.agent.AgentSessions.alive` and `flocc.agent.AgentSessions.capture` instead of `flocc.sessions.Sessions`, and in the attach test assert the exec'd argv ends with `["attach", "NF-12"]` after the config's `agent_run`.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_agentrun.py tests/test_doctor.py tests/test_tokens.py -k "claude or probe or boundary or wrapper or login"`
Expected: FAIL.

- [ ] **Step 3: `claude` and `probe` in the module and the wrapper**

In `deploy/flocc-agent-run`, change the case line to `  alive|attach|capture|check|claude|kill|prepare|probe|remove|send|start) ;;`. In `src/flocc/agentrun.py`, add `"claude"` and `"probe"` to `SUBCOMMANDS` (sorted), `import grp` and `import tempfile`, and add:

```python
# The only claude invocations the service needs from flocc-agent's install.
CLAUDE_ALLOWED = (("--version",), ("auth", "status"), ("auth", "login"), ("remote-control",))
_EMAIL = re.compile(r"^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$")


def claude(args: list[str]) -> int:
    allowed = next((p for p in CLAUDE_ALLOWED if tuple(args[: len(p)]) == p), None)
    rest = args[len(allowed) :] if allowed else args
    if allowed == ("auth", "login"):
        ok = rest in ([], ["--claudeai"]) or (
            len(rest) == 3 and rest[0] == "--claudeai" and rest[1] == "--email" and _EMAIL.match(rest[2])
        )
    else:
        ok = allowed is not None and not rest
    if not ok:
        raise BadRequest(f"claude {' '.join(args)} is not allowed")
    if allowed == ("remote-control",):
        # The consent runs in flocc-agent's worktrees; an untrusted folder would sit at the dialog.
        trust_folders(_home() / ".claude.json", [Path.cwd()])
    exe = _home() / ".local" / "bin" / "claude"
    os.execv(exe, [str(exe), *args])


def _tmux_up() -> bool:
    try:
        probe = ["tmux", "-N", "-L", SOCKET, "list-sessions"]
        return subprocess.run(probe, capture_output=True, check=False).returncode == 0
    except OSError:
        return False


def probe(req: dict) -> dict:
    def readable(path: str) -> bool:
        try:
            with open(path, "rb") as f:
                f.read(1)
            return True
        except OSError:
            return False

    def writable(path: str) -> bool:
        try:
            with tempfile.NamedTemporaryFile(dir=path):
                return True
        except OSError:
            return False

    def group(gid: int) -> str:
        try:
            return grp.getgrgid(gid).gr_name
        except KeyError:
            return str(gid)

    return {
        "read": {str(p): readable(str(p)) for p in req.get("read") or []},
        "write": {str(p): writable(str(p)) for p in req.get("write") or []},
        "tmux": _tmux_up(),
        "groups": sorted({group(g) for g in os.getgroups()}),
        "containers": bool(container_env()),
    }
```

Add `"probe": probe` to `HANDLERS`, and in `main`, before `out = HANDLERS[sub](_request())`, add:

```python
        if sub == "claude":
            return claude(args)
```

In `src/flocc/agent.py`, add:

```python
    def claude_argv(self) -> list[str]:
        return [*self.cfg.agent_run, "claude"]

    def probe(self, read: list[str], write: list[str]) -> dict:
        return self.call("probe", payload={"read": read, "write": write})
```

- [ ] **Step 4: Callers**

In `src/flocc/tokens.py`, change `claude_login(exe: str, run=subprocess.run, timeout: float = 30)` to `claude_login(claude: list[str], run=subprocess.run, timeout: float = 30)` running `[*claude, "auth", "status"]`, and in `check_claude_login` call `claude_login(rt.agent.claude_argv(), run)`.

In `src/flocc/status.py`, change `collect`'s `claude: str | None = None` to `claude: list[str] | None = None` (the call `claude_login(claude, run, _LOGIN_TIMEOUT)` is unchanged).

In `src/flocc/cli.py`:
- `cmd_status`: replace `report = collect(cfg, Sessions(), claude=shutil.which("claude"))` with

```python
    agent = Agent(cfg)
    report = collect(cfg, agent.sessions(), claude=agent.claude_argv())
```

  (import `from flocc.agent import Agent` inside the function, drop the `Sessions` import there and `shutil` at the top if unused).
- `_live_session`: replace `sessions = Sessions()` with `sessions = Agent(cfg).sessions()` and drop the `Sessions` import there.
- `cmd_attach`: replace its body with

```python
def cmd_attach(args) -> int:
    from flocc.agentrun import ATTACH_HINT

    cfg, _ = _config(args)
    print(f"Attaching {args.session} {ATTACH_HINT}", file=sys.stderr)
    argv = [*cfg.agent_run, "attach", args.session]
    os.execvp(argv[0], argv)
```

- `cmd_doctor`: pass the path on: `return run_doctor(cfg, path)` and `return doctor_json(cfg, path)` (already passes it).

In `src/flocc/doctor.py`:
- `claude_checks(claude: list[str] | None, run=subprocess.run)`: `if claude is None: return [Result("fail", "claude", "flocc-agent-run is not set up")]`; run `[*claude, "--version"]`; pass `claude` to `claude_login`; change the login fail remedy to `LOGIN_REMEDY` (unchanged); delete the `api key` check (the pane's environment is built from nothing, so the service's own `ANTHROPIC_API_KEY` never reaches a session).
- Delete `tmux_check` and drop `"tmux"` from `path_checks`' tool list.
- Add:

```python
def agent_checks(cfg: Config, config_path: Path, agent) -> list[Result]:
    secrets = [
        cfg.linear_key_file,
        cfg.publish_gh_token_file,
        cfg.agent_gh_token_file,
        cfg.ledger_path,
        config_path,
    ]
    try:
        seen = agent.probe([str(p) for p in secrets], [str(cfg.plugin_dir)])
    except (subprocess.SubprocessError, OSError) as exc:
        detail = f"flocc-agent-run did not answer ({exc}); flocc setup installs it and its sudoers rule"
        return [Result("fail", "agent", detail)]
    results = [Result("ok", "agent", "flocc-agent-run answers through sudo")]
    reached = [p for p, ok in {**seen["read"], **seen["write"]}.items() if ok]
    if reached:
        detail = f"flocc-agent can reach {', '.join(reached)}; keep /home/flocc mode 700 and {cfg.plugin_dir} flocc's"
        results.append(Result("fail", "agent boundary", detail))
    else:
        detail = "flocc-agent cannot read the secrets or the ledger, nor write the plugin directory"
        results.append(Result("ok", "agent boundary", detail))
    if "docker" in seen.get("groups", []):
        detail = (
            "flocc-agent is in the docker group, which is root-equivalent; checks use its Podman"
            " socket instead: gpasswd -d flocc-agent docker"
        )
        results.append(Result("fail", "agent docker", detail))
    else:
        results.append(Result("ok", "agent docker", "flocc-agent is not in the docker group"))
    if seen.get("containers"):
        results.append(Result("ok", "containers", "checks reach flocc-agent's Podman socket"))
    else:
        detail = "no Podman socket for flocc-agent; checks that start containers fail (docker = true in the host file sets it up)"
        results.append(Result("ok", "containers", detail))
    if seen.get("tmux"):
        results.append(Result("ok", "tmux", "flocc-agent's session server is up"))
    else:
        detail = "flocc-agent has no session server; flocc ship -H <this host> starts it"
        results.append(Result("fail", "tmux", detail))
    return results


MANAGED_SETTINGS = Path("/etc/claude-code/managed-settings.json")
_DIALOG_TOOLS = {"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"}


def managed_check(path: Path = MANAGED_SETTINGS) -> Result:
    remedy = "flocc ship -H <this host> writes it in the root phase"
    try:
        data = json.loads(path.read_text())
        deny = set(data["permissions"]["deny"])
        commands = [h["command"] for e in data["hooks"]["PreToolUse"] for h in e["hooks"]]
    except (OSError, ValueError, KeyError, TypeError) as exc:
        return Result("fail", "managed settings", f"{path} unreadable or incomplete ({exc}); {remedy}")
    if not _DIALOG_TOOLS <= deny or not any(c.endswith("flocc guard") for c in commands):
        return Result("fail", "managed settings", f"{path} lacks the guard or the dialog denies; {remedy}")
    return Result("ok", "managed settings", "the guard and the dialog denies are root's")
```

Call it from `collect_results`: `results.append(managed_check())` after `agent_checks`. Import `managed_check` in `tests/test_doctor.py`. Tests that call `collect_results` monkeypatch `flocc.doctor.managed_check` to `lambda: Result("ok", "managed settings", "")`; patching `MANAGED_SETTINGS` would not reach the default argument, which is bound at definition.

Create `deploy/managed-settings.json`, which Task 13 installs as root:

```json
{
  "permissions": {
    "deny": ["AskUserQuestion", "EnterPlanMode", "ExitPlanMode"]
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^(AskUserQuestion|EnterPlanMode|ExitPlanMode|Bash|Edit|Write|NotebookEdit|mcp__.*)$",
        "hooks": [ { "type": "command", "command": "/opt/flocc/bin/flocc guard" } ]
      }
    ]
  }
}
```

The dialog tools stay in the guard's matcher although the deny rules already block them: the guard's deny carries `KEEP_MOVING` to the model, the rule's does not. The path is absolute because the hook runs for any user who starts `claude` on the host, whatever its `PATH`.

- Change `collect_results(cfg)` to `collect_results(cfg: Config, config_path: Path, agent=None)`: `agent = agent or Agent(cfg)`; replace `results += claude_checks(shutil.which("claude"))` and `results.append(tmux_check())` with `results += claude_checks(agent.claude_argv())` and `results += agent_checks(cfg, config_path, agent)`. `run_doctor(cfg, config_path)` and `doctor_json(cfg, config_path)` pass `config_path` on. Update `tests/test_doctor.py` call sites of `collect_results` and `run_doctor` to pass a path and a `FakeAgent`.

- [ ] **Step 5: Run the tests**

Run: `uv run pytest -q && uv run pyrefly check && uvx --from shellcheck-py shellcheck deploy/flocc-agent-run`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add deploy/flocc-agent-run deploy/managed-settings.json src tests
git commit -s -m "Status, peek, attach and doctor through flocc-agent-run; check the boundary"
```

---

## Part C: the installer

The three host phases after this part: `root` (as root) → `agent` (as `flocc-agent`) → `service` (as `flocc`); `login` stays `flocc`'s. Constants live in `installer/internal/steps/user.go` and match the Global Constraints' layout.

### Task 13: Root phase: flocc-agent, the wrapper, sudoers, managed settings, Podman and the login move

**Files:**
- Modify: `deploy/embed.go`, `deploy/embed_test.go`, `deploy/flocc-tmux.service`, `deploy/flocc.service`
- Modify: `installer/internal/steps/user.go` (constants)
- Create: `installer/internal/steps/agent_user.go`, `installer/internal/steps/agent_user_test.go`
- Delete: `installer/internal/steps/docker.go`, `installer/internal/steps/docker_test.go`
- Create: `installer/internal/steps/containers.go`, `installer/internal/steps/containers_test.go`
- Modify: `installer/internal/steps/list.go` (`RootSteps`), `installer/internal/answers/answers.go` (`Docker` comment)
- Modify: `plugin/hooks/hooks.json` (the guard moves out), `tests/test_plugin.py::test_hooks`

**Interfaces:**
- Consumes: `deploy/flocc-agent-run`, `deploy/flocc-agent.sudoers` (Task 9); `deploy/managed-settings.json` (Task 12).
- Produces: `steps.AgentUser = "flocc-agent"`, `steps.WorkGroup = "flocc-work"`, `steps.AgentHome = "/home/flocc-agent"`, `steps.AgentRun = "/usr/local/libexec/flocc-agent-run"`, `steps.OptDir = "/opt/flocc"`, `steps.ManagedSettingsFile = "/etc/claude-code/managed-settings.json"`; steps `AgentAccount{}`, `AgentAccess{}`, `ClaudeHome{}`, `Containers{}` (replaces `HostDocker{}`); `deploy.Units = []string{"flocc.service", "flocc.timer"}` (the service's), `deploy.AgentUnits = []string{"flocc-tmux.service"}`; `deploy.Files` also embeds `flocc-agent-run`, `flocc-agent.sudoers` and `managed-settings.json`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/agent_user_test.go`:

```go
package steps

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestAgentAccountCreatesTheUserGroupAndDirectories(t *testing.T) {
	f := host.NewFake()
	f.OnSeq("id -u flocc-agent", host.Result{Code: 1}, host.Result{Stdout: "1001\n"})
	f.On("id -nG flocc", 0, "flocc\n")
	f.On("id -nG flocc-agent", 0, "flocc-agent\n")
	f.On("id -u flocc", 0, "1000\n")
	f.OnName("groupadd", 0, "").OnName("useradd", 0, "").OnName("usermod", 0, "")
	f.OnName("chgrp", 0, "").OnName("chmod", 0, "").OnName("install", 0, "").OnName("chown", 0, "")
	f.OnName("loginctl", 0, "").OnName("systemctl", 0, "")
	e := testEnv(t, f)
	if err := (AgentAccount{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"groupadd -f flocc-work",
		"useradd --create-home --shell /bin/bash flocc-agent",
		"usermod -aG flocc-work flocc",
		"chmod 710 /home/flocc-agent",
		"install -d -o flocc-agent -g flocc-work -m 2750 /home/flocc-agent/worktrees",
		"chmod 2750 /home/flocc-agent/state",
		"loginctl enable-linger flocc-agent",
		"systemctl try-restart user@1000.service",
	} {
		if !f.Ran(want) {
			t.Errorf("did not run %q; ran %v", want, f.Calls)
		}
	}
}

func TestAgentAccountRefusesAnAgentInAPrivilegedGroup(t *testing.T) {
	f := host.NewFake()
	f.On("id -u flocc-agent", 0, "1001\n").On("id -nG flocc", 0, "flocc flocc-work\n")
	f.On("id -nG flocc-agent", 0, "flocc-agent wheel\n")
	done, detail, _ := (AgentAccount{}).Check(context.Background(), testEnv(t, f))
	if done || detail != "flocc-agent is in group wheel" {
		t.Fatalf("%v %q", done, detail)
	}
}

func TestAgentAccessInstallsTheWrapperAndAValidatedRule(t *testing.T) {
	f := host.NewFake().OnName("visudo", 0, "").OnName("install", 0, "")
	e := testEnv(t, f)
	if err := (AgentAccess{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	want, _ := deploy.Files.ReadFile("flocc-agent-run")
	got, _ := os.ReadFile(e.Path(AgentRun))
	if string(got) != string(want) {
		t.Fatal("wrapper not written")
	}
	if st, _ := os.Stat(e.Path(sudoersFile)); st == nil || st.Mode().Perm() != 0o440 {
		t.Fatalf("sudoers file: %v", st)
	}
	if !f.Ran("visudo -cf " + sudoersFile + ".new") {
		t.Fatalf("rule not validated: %v", f.Calls)
	}
	want, _ = deploy.Files.ReadFile("managed-settings.json")
	got, _ = os.ReadFile(e.Path(ManagedSettingsFile))
	if string(got) != string(want) {
		t.Fatal("managed settings not written")
	}
	if st, _ := os.Stat(e.Path(ManagedSettingsFile)); st == nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("managed settings: %v", st)
	}
}

func TestAgentAccountRefusesTheDockerGroup(t *testing.T) {
	f := host.NewFake()
	f.On("id -u flocc-agent", 0, "1001\n").On("id -nG flocc", 0, "flocc flocc-work\n")
	f.On("id -nG flocc-agent", 0, "flocc-agent docker\n")
	done, detail, _ := (AgentAccount{}).Check(context.Background(), testEnv(t, f))
	if done || detail != "flocc-agent is in group docker" {
		t.Fatalf("%v %q", done, detail)
	}
}

func TestAgentAccessLeavesNoRuleWhenVisudoRejectsIt(t *testing.T) {
	f := host.NewFake().OnName("visudo", 1, "")
	e := testEnv(t, f)
	if err := (AgentAccess{}).Apply(context.Background(), e); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(e.Path(sudoersFile)); err == nil {
		t.Fatal("rejected rule installed")
	}
	if _, err := os.Stat(e.Path(sudoersFile + ".new")); err == nil {
		t.Fatal("rejected rule left behind")
	}
}

func TestClaudeHomeMovesTheLoginToFloccAgent(t *testing.T) {
	f := host.NewFake().OnName("mv", 0, "").OnName("chown", 0, "")
	e := testEnv(t, f)
	os.MkdirAll(e.Path("/home/flocc/.claude"), 0o700)
	os.WriteFile(e.Path("/home/flocc/.claude.json"), []byte("{}"), 0o600)
	os.MkdirAll(e.Path(AgentHome), 0o710)
	if done, _, _ := (ClaudeHome{}).Check(context.Background(), e); done {
		t.Fatal("done before the move")
	}
	if err := (ClaudeHome{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mv -- /home/flocc/.claude /home/flocc-agent/.claude",
		"chown -R -h flocc-agent:flocc-agent /home/flocc-agent/.claude",
		"mv -- /home/flocc/.claude.json /home/flocc-agent/.claude.json",
	} {
		if !f.Ran(want) {
			t.Errorf("did not run %q; ran %v", want, f.Calls)
		}
	}
}

func TestClaudeHomeRefusesALink(t *testing.T) {
	e := testEnv(t, host.NewFake())
	os.MkdirAll(e.Path("/home/flocc"), 0o700)
	os.Symlink("/etc", e.Path("/home/flocc/.claude"))
	if err := (ClaudeHome{}).Apply(context.Background(), e); err == nil {
		t.Fatal("followed a link")
	}
}

func TestTheUnitsAreSplitBetweenTheUsers(t *testing.T) {
	if !slices.Equal(deploy.AgentUnits, []string{"flocc-tmux.service"}) || slices.Contains(deploy.Units, "flocc-tmux.service") {
		t.Fatal(deploy.Units, deploy.AgentUnits)
	}
	b, _ := deploy.Files.ReadFile("flocc-tmux.service")
	if !strings.Contains(string(b), "\nUMask=0027\n") {
		t.Fatal("tmux unit lacks UMask=0027")
	}
}
```

`git rm installer/internal/steps/docker.go installer/internal/steps/docker_test.go`, then create `installer/internal/steps/containers_test.go`:

```go
package steps

import (
	"context"
	"os"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func containersEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Distro.Manager = host.Apt
	e.Answers.Docker = true
	return e
}

func podmanHost(serviceGroups string) *host.Fake {
	return host.NewFake().
		On("sh -c command -v podman", 0, "/usr/bin/podman").
		On("id -nG flocc", 0, serviceGroups)
}

func TestContainersAreDoneWhenNotAskedFor(t *testing.T) {
	if ok, detail, _ := (Containers{}).Check(context.Background(), testEnv(t, host.NewFake())); !ok || detail != "none asked for" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersSkipHostsWithoutApt(t *testing.T) {
	e := containersEnv(t, host.NewFake())
	e.Distro.Manager = host.Pacman
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); !ok || detail != "apt hosts only" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersAreNotDoneWithoutPodman(t *testing.T) {
	if ok, detail, _ := (Containers{}).Check(context.Background(), containersEnv(t, host.NewFake())); ok || detail != "podman is not installed" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersNeedSubordinateIDsForTheAgent(t *testing.T) {
	e := containersEnv(t, podmanHost("flocc flocc-work"))
	os.MkdirAll(e.Path("/etc"), 0o755)
	os.WriteFile(e.Path("/etc/subuid"), []byte("flocc:100000:65536\n"), 0o644)
	os.WriteFile(e.Path("/etc/subgid"), []byte("flocc:100000:65536\n"), 0o644)
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); ok || detail != "flocc-agent has no /etc/subuid range" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersAreNotDoneWhileTheServiceIsInTheDockerGroup(t *testing.T) {
	e := containersEnv(t, podmanHost("flocc flocc-work docker"))
	writeSubIDs(t, e)
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); ok || detail != "flocc is still in the docker group" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersAreDone(t *testing.T) {
	e := containersEnv(t, podmanHost("flocc flocc-work"))
	writeSubIDs(t, e)
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); !ok || detail != "rootless Podman for flocc-agent" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersApplyInstallsPodmanAddsIDsAndLeavesDockerdAlone(t *testing.T) {
	f := host.NewFake().OnName("apt-get", 0, "").OnName("usermod", 0, "").OnName("gpasswd", 0, "").
		On("id -nG flocc", 0, "flocc docker")
	e := containersEnv(t, f)
	os.MkdirAll(e.Path("/etc"), 0o755)
	os.WriteFile(e.Path("/etc/subuid"), []byte("flocc:100000:65536\n"), 0o644)
	os.WriteFile(e.Path("/etc/subgid"), []byte("flocc:100000:65536\n"), 0o644)
	if err := (Containers{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"apt-get install -y podman uidmap slirp4netns",
		"usermod --add-subuids 165536-231071 --add-subgids 165536-231071 flocc-agent",
		"gpasswd -d flocc docker",
	} {
		if !f.Ran(want) {
			t.Errorf("did not run %q; ran %v", want, f.Calls)
		}
	}
	dockerUntouched(t, f.Calls)
}

func writeSubIDs(t *testing.T, e *Env) {
	t.Helper()
	os.MkdirAll(e.Path("/etc"), 0o755)
	for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
		os.WriteFile(e.Path(f), []byte("flocc-agent:165536:65536\n"), 0o644)
	}
}
```

`dockerUntouched` is the package's alias for `dockerguard.Untouched` (`create_docker_test.go`).

In `tests/test_plugin.py::test_hooks`, replace the `(clearing, guarding) = hooks["PreToolUse"]` assertions from Task 2 with:

```python
    (clearing,) = hooks["PreToolUse"]
    assert "matcher" not in clearing and [h["command"] for h in clearing["hooks"]] == [clear]


def test_the_guard_is_in_managed_settings():
    managed = json.loads((ROOT / "deploy" / "managed-settings.json").read_text())
    (guarding,) = managed["hooks"]["PreToolUse"]
    assert [h["command"] for h in guarding["hooks"]] == ["/opt/flocc/bin/flocc guard"]
    for tool in ("AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "Bash", "Edit", "Write"):
        assert re.search(guarding["matcher"], tool)
    assert re.search(guarding["matcher"], "mcp__linear__save_issue")
    assert not re.search(guarding["matcher"], "Read")
    assert {"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"} <= set(managed["permissions"]["deny"])
    assert "allowManagedHooksOnly" not in managed
```

(`ROOT` is the repo root; use the name `tests/test_plugin.py` already has for it, or `Path(__file__).parent.parent`.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/steps/ -run 'AgentAccount|AgentAccess|ClaudeHome|Units|Containers' ./deploy/... && uv run pytest -q tests/test_plugin.py`
Expected: FAIL (undefined `AgentAccount`, `deploy.AgentUnits`).

- [ ] **Step 3: Deploy files**

`deploy/embed.go`:

```go
// Package deploy embeds the unit files, the agent wrapper, its sudoers rule, Claude Code's
// managed settings and the config template, so the installer ships the copies in this commit
// and no others.
package deploy

import "embed"

//go:embed flocc.service flocc.timer flocc-tmux.service config.example.toml flocc-agent-run flocc-agent.sudoers managed-settings.json
var Files embed.FS

// Units are the service user's; AgentUnits are flocc-agent's.
var (
	Units      = []string{"flocc.service", "flocc.timer"}
	AgentUnits = []string{"flocc-tmux.service"}
)
```

In `deploy/embed_test.go`, change the loop to `for _, name := range append(append(append([]string{}, Units...), AgentUnits...), "config.example.toml", "flocc-agent-run", "flocc-agent.sudoers", "managed-settings.json") {`.

`deploy/flocc-tmux.service`:

```ini
[Unit]
Description=tmux server for flocc sessions

[Service]
# -D keeps tmux in the foreground for systemd and turns exit-empty off, so the server outlives
# its sessions. Ticks connect through flocc-agent-run with -N and never start a server of their own.
ExecStart=/usr/bin/tmux -L flocc -D
ExecStop=/usr/bin/tmux -L flocc kill-server
# Sessions inherit it: flocc reads their worktrees and files through the flocc-work group, and
# nobody else may.
UMask=0027

[Install]
WantedBy=default.target
```

In `deploy/flocc.service`, change `After=network-online.target flocc-tmux.service` to `After=network-online.target`, `Wants=network-online.target flocc-tmux.service` to `Wants=network-online.target`, and the last comment pair to:

```ini
# The tick takes its own lock. Sessions live in flocc-agent's flocc-tmux.service, outside this
# unit's cgroup, so they survive the end of each tick.
```

- [ ] **Step 4: Constants and the steps**

In `installer/internal/steps/user.go`, add below `const ServiceUser = "flocc"`:

```go
// The two-user layout (d176); src/flocc/config.py's fixed paths match these.
const (
	AgentUser = "flocc-agent"
	WorkGroup = "flocc-work"
	AgentHome = "/home/" + AgentUser
	AgentRun  = "/usr/local/libexec/flocc-agent-run"
	OptDir    = "/opt/flocc"
	// Claude Code reads it for every user and lets no user setting override it.
	ManagedSettingsFile = "/etc/claude-code/managed-settings.json"
)
```

`installer/internal/steps/agent_user.go`:

```go
package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/deploy"
)

const sudoersFile = "/etc/sudoers.d/flocc-agent"

// agentDirs are what flocc reads through flocc-work: the worktrees, the clones they share objects
// with, and each issue's session files.
var agentDirs = []string{"worktrees", "src", "state"}

func inGroup(ctx context.Context, e *Env, user, group string) bool {
	return slices.Contains(strings.Fields(e.Output(ctx, "id", "-nG", user)), group)
}

// AgentAccount is flocc-agent: it runs the sessions and the repos' code, and can read nothing of
// flocc's. flocc reads flocc-agent's worktrees and state through the group flocc-work. Any group
// in PrivilegedGroups, docker included, would give the agent root and with it flocc's secrets.
type AgentAccount struct{}

func (AgentAccount) Name() string { return "Agent user" }

func (AgentAccount) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.Ok(ctx, "id", "-u", AgentUser) {
		return false, "no " + AgentUser + " user", nil
	}
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", AgentUser)) {
		if slices.Contains(PrivilegedGroups, g) || g == ServiceUser || g == WorkGroup {
			return false, AgentUser + " is in group " + g, nil
		}
	}
	if !inGroup(ctx, e, ServiceUser, WorkGroup) {
		return false, ServiceUser + " is not in " + WorkGroup, nil
	}
	st, err := os.Stat(e.Path(AgentHome))
	if err != nil || st.Mode().Perm() != 0o710 {
		return false, AgentHome + " is not mode 710", nil
	}
	for _, d := range agentDirs {
		st, err := os.Stat(e.Path(filepath.Join(AgentHome, d)))
		if err != nil || st.Mode().Perm() != 0o750 || st.Mode()&fs.ModeSetgid == 0 {
			return false, AgentHome + "/" + d + " is not mode 2750", nil
		}
	}
	if e.Mode == Systemd && e.Output(ctx, "loginctl", "show-user", AgentUser, "-p", "Linger", "--value") != "yes" {
		return false, "lingering off for " + AgentUser, nil
	}
	return true, AgentUser + ", home 710, " + ServiceUser + " reads through " + WorkGroup, nil
}

func (AgentAccount) Apply(ctx context.Context, e *Env) error {
	if _, err := e.Must(ctx, "groupadd", "-f", WorkGroup); err != nil {
		return err
	}
	if !e.Ok(ctx, "id", "-u", AgentUser) {
		if _, err := e.Must(ctx, "useradd", "--create-home", "--shell", "/bin/bash", AgentUser); err != nil {
			return err
		}
	}
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", AgentUser)) {
		if slices.Contains(PrivilegedGroups, g) || g == ServiceUser || g == WorkGroup {
			return fmt.Errorf("%s is in group %s, which reaches past its sessions; remove it with gpasswd -d %s %s and run again", AgentUser, g, AgentUser, g)
		}
	}
	added := !inGroup(ctx, e, ServiceUser, WorkGroup)
	cmds := [][]string{
		{"usermod", "-aG", WorkGroup, ServiceUser},
		{"chgrp", WorkGroup, AgentHome},
		{"chmod", "710", AgentHome},
	}
	for _, d := range agentDirs {
		dir := filepath.Join(AgentHome, d)
		// install -d leaves an existing directory's owner and mode as they were.
		cmds = append(cmds,
			[]string{"install", "-d", "-o", AgentUser, "-g", WorkGroup, "-m", "2750", dir},
			[]string{"chown", AgentUser + ":" + WorkGroup, dir},
			[]string{"chmod", "2750", dir})
	}
	if e.Mode == Systemd {
		cmds = append(cmds, []string{"loginctl", "enable-linger", AgentUser})
	}
	for _, c := range cmds {
		if _, err := e.Must(ctx, c[0], c[1:]...); err != nil {
			return err
		}
	}
	if added && e.Mode == Systemd {
		// A running user manager keeps the groups it started with; until it restarts, the ticks
		// it starts cannot read the worktrees.
		uid := e.Output(ctx, "id", "-u", ServiceUser)
		if _, err := e.Must(ctx, "systemctl", "try-restart", "user@"+uid+".service"); err != nil {
			return err
		}
	}
	return nil
}

// AgentAccess installs the wrapper, the one sudoers rule that reaches it, Claude Code's managed
// settings (the guard hook and the dialog denies, which flocc-agent cannot turn off), and
// /opt/flocc, where the service publishes the plugin and the flocc build flocc-agent runs.
type AgentAccess struct{}

func (AgentAccess) Name() string { return "Agent access" }

var accessFiles = []struct {
	path, src string
	mode      os.FileMode
}{
	{AgentRun, "flocc-agent-run", 0o755},
	{sudoersFile, "flocc-agent.sudoers", 0o440},
	{ManagedSettingsFile, "managed-settings.json", 0o644},
}

func (AgentAccess) Check(ctx context.Context, e *Env) (bool, string, error) {
	for _, f := range accessFiles {
		want, _ := deploy.Files.ReadFile(f.src)
		got, err := os.ReadFile(e.Path(f.path))
		if err != nil || !bytes.Equal(got, want) {
			return false, f.path + " not installed or out of date", nil
		}
		if st, err := os.Stat(e.Path(f.path)); err != nil || st.Mode().Perm() != f.mode {
			return false, fmt.Sprintf("%s is not mode %o", f.path, f.mode), nil
		}
	}
	if st, err := os.Stat(e.Path(OptDir)); err != nil || !st.IsDir() {
		return false, "no " + OptDir, nil
	}
	return true, "wrapper, sudoers rule, " + OptDir, nil
}

func (AgentAccess) Apply(ctx context.Context, e *Env) error {
	run, _ := deploy.Files.ReadFile("flocc-agent-run")
	if err := os.MkdirAll(e.Path(filepath.Dir(AgentRun)), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(e.Path(AgentRun), run, 0o755); err != nil {
		return err
	}
	rule, _ := deploy.Files.ReadFile("flocc-agent.sudoers")
	if err := os.MkdirAll(e.Path(filepath.Dir(sudoersFile)), 0o750); err != nil {
		return err
	}
	// sudo skips files in sudoers.d whose names hold a dot, so the candidate is inert until the
	// rename. A rule sudo cannot parse breaks every sudo on the machine, the owner's included.
	tmp := sudoersFile + ".new"
	if err := os.WriteFile(e.Path(tmp), rule, 0o440); err != nil {
		return err
	}
	if _, err := e.Must(ctx, "visudo", "-cf", tmp); err != nil {
		return errors.Join(err, os.Remove(e.Path(tmp)))
	}
	if err := os.Rename(e.Path(tmp), e.Path(sudoersFile)); err != nil {
		return err
	}
	managed, _ := deploy.Files.ReadFile("managed-settings.json")
	if err := os.MkdirAll(e.Path(filepath.Dir(ManagedSettingsFile)), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(e.Path(ManagedSettingsFile), managed, 0o644); err != nil {
		return err
	}
	_, err := e.Must(ctx, "install", "-d", "-o", ServiceUser, "-g", ServiceUser, "-m", "755", OptDir)
	return err
}

// ClaudeHome moves a one-user host's Claude login and Remote Control consent to flocc-agent,
// whose sessions use them now.
type ClaudeHome struct{}

func (ClaudeHome) Name() string { return "Claude login owner" }

var claudeHomeFiles = []string{".claude", ".claude.json"}

func (ClaudeHome) Check(ctx context.Context, e *Env) (bool, string, error) {
	for _, f := range claudeHomeFiles {
		if _, err := os.Lstat(e.Path(filepath.Join("/home", ServiceUser, f))); err == nil {
			return false, "/home/" + ServiceUser + "/" + f + " is still " + ServiceUser + "'s", nil
		}
	}
	return true, "the login is " + AgentUser + "'s", nil
}

func (ClaudeHome) Apply(ctx context.Context, e *Env) error {
	for _, f := range claudeHomeFiles {
		from, to := filepath.Join("/home", ServiceUser, f), filepath.Join(AgentHome, f)
		st, err := os.Lstat(e.Path(from))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		// Root acts inside the service home here; a link could point anywhere.
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; remove it by hand and run again", from)
		}
		if _, err := os.Lstat(e.Path(to)); err == nil {
			// flocc-agent already has its own; the service's copy is a stale login.
			if _, err := e.Must(ctx, "rm", "-rf", "--", from); err != nil {
				return err
			}
			continue
		}
		for _, c := range [][]string{{"mv", "--", from, to}, {"chown", "-R", "-h", AgentUser + ":" + AgentUser, to}} {
			if _, err := e.Must(ctx, c[0], c[1:]...); err != nil {
				return err
			}
		}
	}
	return nil
}
```

`writeAtomic` already exists in the package (`claude.go` uses it); if it does not set the mode on an existing file, add `os.Chmod(e.Path(AgentRun), 0o755)` after the call.

`installer/internal/steps/containers.go`:

```go
package steps

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// subIDCount is the range useradd gives a new user on Debian and Ubuntu.
const subIDCount = 65536

// Containers gives flocc-agent rootless Podman for checks that start containers (testcontainers);
// the agent phase starts its podman.socket and flocc-agent-run points DOCKER_HOST at it. It is
// not Docker, the create-host step that opens the firewall for Docker on the VM's bridge.
//
// The docker group is root-equivalent, so neither user may be in it. A host that had the Docker
// step keeps its docker.io package and daemon: nothing here stops or restarts dockerd, and with
// nobody in the group it serves no one.
type Containers struct{}

func (Containers) Name() string { return "Containers" }

func (Containers) Check(ctx context.Context, e *Env) (bool, string, error) {
	switch {
	case !e.Answers.Docker:
		return true, "none asked for", nil
	case e.Distro.Manager != host.Apt:
		return true, "apt hosts only", nil
	case !e.Ok(ctx, "sh", "-c", "command -v podman"):
		return false, "podman is not installed", nil
	}
	for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
		if !hasSubIDs(e.Path(f), AgentUser) {
			return false, AgentUser + " has no " + f + " range", nil
		}
	}
	if inGroup(ctx, e, ServiceUser, "docker") {
		return false, ServiceUser + " is still in the docker group", nil
	}
	return true, "rootless Podman for " + AgentUser, nil
}

func (Containers) Apply(ctx context.Context, e *Env) error {
	install := host.Cmd{Name: "apt-get", Args: []string{"install", "-y", "podman", "uidmap", "slirp4netns"}, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
	if err := e.SpinCmd(ctx, install); err != nil {
		return err
	}
	if !hasSubIDs(e.Path("/etc/subuid"), AgentUser) || !hasSubIDs(e.Path("/etc/subgid"), AgentUser) {
		// One range past every range already given out, in both files, so the two stay equal.
		start := max(nextFree(e.Path("/etc/subuid")), nextFree(e.Path("/etc/subgid")), 100000)
		ids := fmt.Sprintf("%d-%d", start, start+subIDCount-1)
		if _, err := e.Must(ctx, "usermod", "--add-subuids", ids, "--add-subgids", ids, AgentUser); err != nil {
			return err
		}
	}
	if inGroup(ctx, e, ServiceUser, "docker") {
		_, err := e.Must(ctx, "gpasswd", "-d", ServiceUser, "docker")
		return err
	}
	return nil
}

// hasSubIDs reports whether an /etc/subuid-style file gives user a non-empty range.
func hasSubIDs(path, user string) bool {
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) != 3 || f[0] != user {
			continue
		}
		_, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil && count > 0 {
			return true
		}
	}
	return false
}

// nextFree is one past the end of the highest range in the file; 0 when it has none.
func nextFree(path string) int {
	b, _ := os.ReadFile(path)
	end := 0
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) != 3 {
			continue
		}
		start, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil {
			end = max(end, start+count)
		}
	}
	return end
}
```

In `installer/internal/answers/answers.go`, change the `Docker` field's comment to `// Docker gives flocc-agent rootless Podman for checks that start containers (testcontainers), on apt hosts. The key keeps its old name so existing host files still load.`

In `installer/internal/steps/list.go`, change `RootSteps` to:

```go
func RootSteps() []Step {
	return []Step{Packages{}, Browsers{}, Tailscale{}, User{}, AgentAccount{}, AgentAccess{}, ClaudeHome{}, Containers{}, Scratch{}}
}
```

`Containers` runs after `AgentAccount`, which creates the user `usermod --add-subuids` names. Grep for other `HostDocker` references (`grep -rn HostDocker installer`) and rename them.

In `plugin/hooks/hooks.json`, delete the second `PreToolUse` entry (the `flocc guard` one Task 2 added); the idle-clearing entry stays. The guard now runs from managed settings only, so it runs once per call.

- [ ] **Step 5: Run the tests**

Run: `go test ./deploy/... ./installer/... && uv run pytest -q tests/test_plugin.py`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l deploy installer && go vet ./...
git add deploy installer plugin/hooks/hooks.json tests/test_plugin.py
git commit -s -m "Root phase: flocc-agent, the wrapper, managed settings and rootless Podman"
```

---

### Task 14: The agent phase

`flocc-setup --phase agent` runs as `flocc-agent`, between the root and service phases: git identity, mise, Claude Code with the allowlisted plugins and its user settings, the host file's tools, and the session tmux server.

**Files:**
- Modify: `installer/cmd/flocc-setup/main.go` (`checkPhase`, `handoff`, `handoffArgs`, `run`), `installer/cmd/flocc-setup/main_test.go`
- Create: `installer/internal/steps/agent_phase.go`, `installer/internal/steps/agent_phase_test.go`
- Modify: `installer/internal/steps/list.go` (`AgentSteps`), `installer/internal/steps/claude.go` (`pushSettings`)

**Interfaces:**
- Consumes: Task 13's constants and `deploy.AgentUnits`.
- Produces: `steps.AgentSteps() []Step` = `Identity{}, Mise{}, Claude{}, Tools{}, AgentTmux{}, PodmanSocket{}`; `stageFor(user string) string`; `handoff(ctx, r, stage, user string, args []string, doc []byte) error`; `handoffArgs(phase string, fromStdin bool) []string`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/agent_phase_test.go`:

```go
package steps

import (
	"context"
	"os"
	"testing"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestMiseIsInstalledWhenMissing(t *testing.T) {
	f := host.NewFake().OnName("sh", 0, "")
	e := testEnv(t, f)
	if done, _, _ := (Mise{}).Check(context.Background(), e); done {
		t.Fatal("done without mise")
	}
	if err := (Mise{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("sh -c curl -fsSL https://mise.run | sh") {
		t.Fatal(f.Calls)
	}
}

func TestAgentTmuxInstallsAndStartsTheUnit(t *testing.T) {
	f := host.NewFake().OnName("systemctl", 0, "").OnName("tmux", 0, "")
	e := testEnv(t, f)
	if err := (AgentTmux{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	want, _ := deploy.Files.ReadFile("flocc-tmux.service")
	got, _ := os.ReadFile(e.HomePath(".config/systemd/user/flocc-tmux.service"))
	if string(got) != string(want) {
		t.Fatal("unit not written")
	}
	if !f.Ran("systemctl --user enable --now flocc-tmux.service") {
		t.Fatal(f.Calls)
	}
}

func TestAgentTmuxKeepsAServerUpInLoopMode(t *testing.T) {
	f := host.NewFake().OnName("sh", 0, "")
	e := testEnv(t, f)
	e.Mode = Loop
	if err := (AgentTmux{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("sh -c umask 027 && exec tmux -L flocc new-session -d -s flocc-keep 'sleep infinity'") {
		t.Fatal(f.Calls)
	}
}

func TestPodmanSocketIsStartedForFloccAgent(t *testing.T) {
	f := host.NewFake().OnName("systemctl", 0, "")
	e := testEnv(t, f)
	e.Answers.Docker = true
	e.Distro.Manager = host.Apt
	if err := (PodmanSocket{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("systemctl --user enable --now podman.socket") {
		t.Fatal(f.Calls)
	}
}

func TestPodmanSocketIsSkippedWithoutContainersOrSystemd(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, _ := (PodmanSocket{}).Check(context.Background(), e); !ok || detail != "none asked for" {
		t.Fatalf("%v %q", ok, detail)
	}
	e.Answers.Docker, e.Distro.Manager, e.Mode = true, host.Apt, Loop
	if ok, detail, _ := (PodmanSocket{}).Check(context.Background(), e); !ok || detail != "needs systemd; checks get no container socket" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestAgentSessionsSkipTheBypassWarning(t *testing.T) {
	if !pushSettings["skipDangerousModePermissionPrompt"] {
		t.Fatal("the bypass warning would stop the first session")
	}
}
```

In `installer/cmd/flocc-setup/main_test.go`, add:

```go
func TestAgentPhaseRunsAsFloccAgent(t *testing.T) {
	if err := checkPhase("agent", 0); err == nil {
		t.Fatal("agent phase allowed as root")
	}
	if err := checkPhase("agent", 1001); err != nil {
		t.Fatal(err)
	}
}

func TestRootHandsOffToTheAgentThenTheService(t *testing.T) {
	f := host.NewFake().OnName("chown", 0, "").OnName("chmod", 0, "").OnName("sudo", 0, "")
	stage := t.TempDir()
	os.WriteFile(filepath.Join(stage, "flocc-setup"), []byte("x"), 0o755)
	for _, h := range []struct{ user, phase string }{{"flocc-agent", "agent"}, {"flocc", "service"}} {
		if err := handoff(context.Background(), f, stage, h.user, handoffArgs(h.phase, true), []byte("doc")); err != nil {
			t.Fatal(err)
		}
	}
	var order []string
	for _, c := range f.Interactives {
		order = append(order, c.String())
	}
	want := []string{
		"sudo -iu flocc-agent /home/flocc-agent/.cache/flocc-setup/flocc-setup --phase agent --answers -",
		"sudo -iu flocc /home/flocc/.cache/flocc-setup/flocc-setup --phase service --answers -",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("%v", order)
	}
}
```

Adjust the existing `handoff` and `handoffArgs` tests in `main_test.go` to the new signatures (`handoff(ctx, r, stage, steps.ServiceUser, args, doc)` with `doc` a `[]byte`, `handoffArgs("service", fromStdin)`).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/steps/ -run 'Mise|AgentTmux|PodmanSocket|BypassWarning' ./installer/cmd/flocc-setup/...`
Expected: FAIL.

- [ ] **Step 3: The agent-phase steps**

`installer/internal/steps/agent_phase.go`:

```go
package steps

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/NovusEdge/flocc/deploy"
)

// Mise installs mise for flocc-agent; Tools then sets the host file's tools on it, and the repos'
// ./setup and checks find them through its shims.
type Mise struct{}

func (Mise) Name() string { return "mise" }

func (Mise) Check(ctx context.Context, e *Env) (bool, string, error) {
	if _, err := os.Stat(e.HomePath(miseRel)); err != nil {
		return false, "not installed", nil
	}
	return true, e.Real(miseRel), nil
}

func (Mise) Apply(ctx context.Context, e *Env) error {
	return e.UI.Spin("Installing mise", func() error {
		_, err := e.Must(ctx, "sh", "-c", "curl -fsSL https://mise.run | sh")
		return err
	})
}

// AgentTmux runs the tmux server the sessions live in, as flocc-agent. Ticks reach it through
// flocc-agent-run with tmux -N, which never starts a server.
type AgentTmux struct{}

func (AgentTmux) Name() string { return "Session server" }

func (AgentTmux) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode == Loop {
		if e.Ok(ctx, "tmux", "-N", "-L", "flocc", "list-sessions") {
			return true, "tmux server up", nil
		}
		return false, "no tmux server", nil
	}
	for _, u := range deploy.AgentUnits {
		want, _ := deploy.Files.ReadFile(u)
		got, err := os.ReadFile(e.HomePath(filepath.Join(".config/systemd/user", u)))
		if err != nil || !bytes.Equal(got, want) {
			return false, u + " not installed or out of date", nil
		}
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", "flocc-tmux.service")); err != nil || r.Code != 0 {
		return false, "flocc-tmux.service not running", nil
	}
	return true, "flocc-tmux.service running", nil
}

func (AgentTmux) Apply(ctx context.Context, e *Env) error {
	if e.Mode == Loop {
		// The panes inherit the server's umask, as they would the unit's UMask.
		_, err := e.Must(ctx, "sh", "-c", "umask 027 && exec tmux -L flocc new-session -d -s flocc-keep 'sleep infinity'")
		return err
	}
	dir := e.HomePath(".config/systemd/user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, u := range deploy.AgentUnits {
		b, _ := deploy.Files.ReadFile(u)
		if err := os.WriteFile(filepath.Join(dir, u), b, 0o644); err != nil {
			return err
		}
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "--now", "flocc-tmux.service"}} {
		if _, err := e.MustCmd(ctx, e.userctl(c...)); err != nil {
			return err
		}
	}
	// enable --now returns before the server exists; the service phase's doctor asks for it.
	for i := 0; i < 10 && !e.Ok(ctx, "tmux", "-N", "-L", "flocc", "list-sessions"); i++ {
		e.Sleep(500 * time.Millisecond)
	}
	return nil
}

// PodmanSocket starts flocc-agent's podman.socket, the Docker-compatible API that
// flocc-agent-run points DOCKER_HOST at. The unit ships with the podman package the root phase
// installed; socket activation starts the API on the first connection.
type PodmanSocket struct{}

func (PodmanSocket) Name() string { return "Container socket" }

func (PodmanSocket) Check(ctx context.Context, e *Env) (bool, string, error) {
	switch {
	case !e.Answers.Docker || e.Distro.Manager != host.Apt:
		return true, "none asked for", nil
	case e.Mode != Systemd:
		return true, "needs systemd; checks get no container socket", nil
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", "podman.socket")); err != nil || r.Code != 0 {
		return false, "podman.socket not running", nil
	}
	return true, "podman.socket running", nil
}

func (PodmanSocket) Apply(ctx context.Context, e *Env) error {
	_, err := e.MustCmd(ctx, e.userctl("enable", "--now", "podman.socket"))
	return err
}
```

Add `"github.com/NovusEdge/flocc/installer/internal/host"` to `agent_phase.go`'s imports.

In `installer/internal/steps/list.go`, add:

```go
// AgentSteps run as flocc-agent, before the service phase, whose doctor reads their outcome
// through flocc-agent-run.
func AgentSteps() []Step {
	return []Step{Identity{}, Mise{}, Claude{}, Tools{}, AgentTmux{}, PodmanSocket{}}
}
```

In `installer/internal/steps/claude.go`, change `pushSettings` and its comment to:

```go
// pushSettings are the /config toggles "Push when actions required" and "Push when Claude
// decides", and the bypass warning's acceptance, which sessions also get from --settings.
var pushSettings = map[string]bool{"inputNeededNotifEnabled": true, "agentPushNotifEnabled": false, "skipDangerousModePermissionPrompt": true}
```

and change `minClaude`'s comment to `// The hooks' StopFailure event and stop_hook_active input need this version or later.` (the value stays `2.1.295`).

In `installer/internal/steps/tools.go`, change the two "for the service user" messages to "for this user".

- [ ] **Step 4: Hand off to the agent phase**

In `installer/cmd/flocc-setup/main.go`:
- Replace `const serviceStage = ...` with:

```go
// stageFor is where user keeps its copy of the stage.
func stageFor(user string) string { return "/home/" + user + "/.cache/flocc-setup" }
```

  and change `if stage == serviceStage {` to `if stage == stageFor(steps.ServiceUser) {`.
- `checkPhase`:

```go
func checkPhase(phase string, euid int) error {
	switch {
	case phase == "root" && euid != 0:
		return errors.New("the root phase runs as root; flocc ship runs it with sudo")
	case phase == "agent" && euid == 0:
		return errors.New("the agent phase runs as " + steps.AgentUser + ", not root")
	case (phase == "service" || phase == "login") && euid == 0:
		return errors.New("the " + phase + " phase runs as " + steps.ServiceUser + ", not root")
	case phase != "root" && phase != "service" && phase != "login" && phase != "agent":
		return fmt.Errorf("unknown phase %q", phase)
	}
	return nil
}
```

- `handoffArgs`:

```go
func handoffArgs(phase string, fromStdin bool) []string {
	args := []string{"--phase", phase}
	if fromStdin {
		args = append(args, "--answers", "-")
	}
	return args
}
```

- `copyScript(dir string, names []string)` stays; `handoff` becomes:

```go
// handoff lets user read the stage and copy it itself. Root never writes inside a user's home:
// an agent could plant a symlink there and have root write through it. doc is the answers
// document as root read it, piped on; nil leaves the phase the terminal.
func handoff(ctx context.Context, r host.Runner, stage, user string, args []string, doc []byte) error {
	names, err := stageNames(stage)
	if err != nil {
		return err
	}
	for _, c := range []host.Cmd{
		{Name: "chown", Args: []string{"-R", "root:" + user, stage}},
		{Name: "chmod", Args: []string{"-R", "g+rX,o-rwx", stage}},
		{Name: "sudo", Args: []string{"-iu", user, "sh", "-c", copyScript(rootStage, names)}},
	} {
		if _, err := host.Must(ctx, r, c); err != nil {
			return err
		}
	}
	var in io.Reader
	if doc != nil {
		in = bytes.NewReader(doc)
	}
	return r.Interactive(ctx, host.Cmd{Name: "sudo", Args: append([]string{"-iu", user, stageFor(user) + "/flocc-setup"}, args...), Stdin: in})
}
```

- In `run`: the login/service phase help text of `--phase` becomes `"root, agent, service or login; the root phase hands off to the agent and service phases itself"`; change `list := steps.ServiceSteps()` to

```go
	list := steps.ServiceSteps()
	if *phase == "agent" {
		list = steps.AgentSteps()
	}
```

  and replace the `if err == nil && *phase == "root" { ... }` block with:

```go
	if err == nil && *phase == "root" {
		var piped []byte
		if fromStdin {
			piped = doc
		}
		for _, h := range []struct{ user, phase string }{{steps.AgentUser, "agent"}, {steps.ServiceUser, "service"}} {
			args := handoffArgs(h.phase, fromStdin)
			if *events {
				args = append(args, "--events")
			}
			if err = handoff(ctx, runner, stage, h.user, args, piped); err != nil {
				break
			}
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode() // the agent or service phase already reported its error
		}
	}
```

  The agent phase ends through `finish(ctx, env, err, false, ...)` (only the service phase prints the summary): the existing `return finish(ctx, env, err, *phase == "service", ...)` already does that.

- [ ] **Step 5: Run the tests**

Run: `go test ./installer/... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l installer
git add installer
git commit -s -m "Agent phase: Claude Code, mise, tools and the session server as flocc-agent"
```

---

### Task 15: Service and login phases for the two-user host

The service phase publishes `/opt/flocc` (the flocc build flocc-agent runs, the plugin and the plugin allowlist), drops the steps flocc-agent took over, removes the one-user host's worktrees and tmux unit, and reports the Claude login through the wrapper. The login phase signs flocc-agent's Claude in and takes its Remote Control consent through the wrapper.

**Files:**
- Modify: `installer/internal/steps/repo.go`, `installer/internal/steps/list.go` (`ServiceSteps`)
- Modify: `installer/internal/steps/claude.go` (`claudeLogin`, `agentClaude`)
- Modify: `installer/internal/steps/run.go`
- Create: `installer/internal/steps/old_worktrees.go`, `installer/internal/steps/old_worktrees_test.go`
- Modify: `installer/internal/steps/login.go` (`Login`; delete `trustWorktrees`)
- Test: `installer/internal/steps/repo_test.go` or the file holding Repo's tests, `run_test.go`, `login_test.go`, `claude_test.go`, `summary_test.go`

**Interfaces:**
- Consumes: Task 13's constants; Task 12's `flocc-agent-run claude` allowlist (`--version`, `auth status`, `auth login [--claudeai [--email ADDR]]`, `remote-control`).
- Produces: `steps.ServiceSteps()` = `Repo{}, ClaudeLogin{}, Secrets{}, Config{}, OldWorktrees{}, Run{}`; `agentClaude(args ...string) host.Cmd`; `publishCmds(e *Env, dir string) []host.Cmd`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/old_worktrees_test.go`:

```go
package steps

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const flocc = "/home/flocc/.local/bin/flocc status --json"

func TestOldWorktreesGoOnceNothingIsClaimed(t *testing.T) {
	f := host.NewFake().On(flocc, 0, `{"tick_lock": "/l", "claims": []}`).OnName("git", 0, "")
	e := testEnv(t, f)
	os.MkdirAll(e.HomePath("worktrees/NF-1"), 0o755)
	os.MkdirAll(e.HomePath("src/backend"), 0o755)
	if done, _, _ := (OldWorktrees{}).Check(context.Background(), e); done {
		t.Fatal("done with ~/worktrees present")
	}
	if err := (OldWorktrees{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.HomePath("worktrees")); err == nil {
		t.Fatal("not removed")
	}
	if !f.Ran("git -C /home/flocc/src/backend worktree prune") {
		t.Fatal(f.Calls)
	}
}

func TestOldWorktreesWaitForLiveClaims(t *testing.T) {
	f := host.NewFake().On(flocc, 0, `{"tick_lock": "/l", "claims": [{"issue": "NF-1", "phase": "impl"}]}`)
	e := testEnv(t, f)
	os.MkdirAll(e.HomePath("worktrees/NF-1"), 0o755)
	err := (OldWorktrees{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "NF-1") {
		t.Fatalf("%v", err)
	}
}
```

In the Repo step's tests, add a case that Apply runs `publishCmds`: `mise exec -- uv tool install --force --reinstall --managed-python --python 3.13 .` with `UV_TOOL_DIR=/opt/flocc/tools`, `UV_TOOL_BIN_DIR=/opt/flocc/bin`, `UV_PYTHON_INSTALL_DIR=/opt/flocc/python`, `UV_LINK_MODE=copy` in its `Env`, and the copy script; and that Check is false while `/opt/flocc/.flocc-commit` differs from the bundle head.

In the Run step's tests: the systemd Apply no longer runs `systemctl --user enable --now flocc-tmux.service`; with an old `~/.config/systemd/user/flocc-tmux.service` on disk, Apply runs `systemctl --user disable --now flocc-tmux.service` and removes the file, and Check reports `old flocc-tmux.service still installed` until then.

In `login_test.go` and `claude_test.go`/`summary_test.go`, every fake response keyed on `/home/flocc/.local/bin/claude auth status` becomes `sudo -n -H -u flocc-agent /usr/local/libexec/flocc-agent-run claude auth status`; the interactive login becomes `sudo -n -H -u flocc-agent /usr/local/libexec/flocc-agent-run claude auth login --claudeai` (with `--email <addr>` when given); the consent command is `sudo -n -H -u flocc-agent /usr/local/libexec/flocc-agent-run claude remote-control` with `Dir` `/home/flocc-agent/worktrees`. Delete the `trustWorktrees` tests.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/steps/...`
Expected: FAIL (undefined `OldWorktrees`, old command strings).

- [ ] **Step 3: Claude through the wrapper**

In `installer/internal/steps/claude.go`, add:

```go
// agentClaude runs flocc-agent's claude through the wrapper: the login and the Remote Control
// consent are flocc-agent's, whose sessions use them.
func agentClaude(args ...string) host.Cmd {
	return host.Cmd{Name: "sudo", Args: append([]string{"-n", "-H", "-u", AgentUser, AgentRun, "claude"}, args...)}
}
```

and in `claudeLogin`, replace `e.Output(ctx, e.claude(), "auth", "status")` with:

```go
	r, err := e.Run.Run(ctx, agentClaude("auth", "status"))
	out := ""
	if err == nil && r.Code == 0 {
		out = r.Stdout
	}
```

using `out` in the `json.Unmarshal`. `e.claude()` and `claudeVersion` stay for the `Claude` step, which runs in the agent phase as flocc-agent.

In `installer/internal/steps/login.go::Login`, replace `host.Cmd{Name: e.claude(), Args: args}` with `agentClaude(args...)`, delete the `trustWorktrees(e)` call (flocc-agent-run trusts the directory before `remote-control`), and replace the consent command with:

```go
	consent := agentClaude("remote-control")
	consent.Dir = AgentHome + "/" + worktreesRel
	if err := o.Consent(ctx, consent); err != nil {
		return err
	}
```

Delete `trustWorktrees`; keep `trustFolder` and `editClaudeJSON` only if something else uses them (`grep -rn "trustFolder\|editClaudeJSON" installer`), else delete them and their tests.

- [ ] **Step 4: Publish /opt/flocc in the Repo step**

In `installer/internal/steps/repo.go`, add:

```go
// publishCmds put the flocc build, the plugin and the plugin allowlist in /opt/flocc, where
// flocc-agent reads them and cannot change them. Not editable, and with its own Python: flocc's
// home is mode 700.
func publishCmds(e *Env, dir string) []host.Cmd {
	env := []string{
		"UV_TOOL_DIR=" + OptDir + "/tools",
		"UV_TOOL_BIN_DIR=" + OptDir + "/bin",
		"UV_PYTHON_INSTALL_DIR=" + OptDir + "/python",
		"UV_LINK_MODE=copy",
	}
	copyScript := "rm -rf " + OptDir + "/plugin " + OptDir + "/deploy && cp -r plugin " + OptDir + "/plugin" +
		" && mkdir -p " + OptDir + "/deploy && cp deploy/agent-plugins.txt " + OptDir + "/deploy/" +
		" && git rev-parse HEAD > " + OptDir + "/.flocc-commit"
	return []host.Cmd{
		{Name: e.Real(".local/bin/mise"), Args: []string{"exec", "--", "uv", "tool", "install", "--force", "--reinstall", "--managed-python", "--python", "3.13", "."}, Dir: dir, Env: env},
		{Name: "sh", Args: []string{"-c", copyScript}, Dir: dir},
	}
}
```

In `Repo.Check`, after the `.local/bin/flocc` check, add:

```go
	if b, _ := os.ReadFile(e.Path(OptDir + "/.flocc-commit")); strings.TrimSpace(string(b)) != want {
		return false, OptDir + " not published at " + short(want), nil
	}
	if _, err := os.Stat(e.Path(OptDir + "/bin/flocc-agent")); err != nil {
		return false, "flocc-agent not published", nil
	}
```

(import `strings`). In `Repo.Apply`, append `publishCmds(e, dir)...` to the command list, and replace the final `return os.MkdirAll(e.HomePath(worktreesRel), 0o755)` with `return nil` (worktrees are flocc-agent's now).

If `~/.config/flocc/permissions.toml` exists, `Repo.Apply` removes it: add `os.Remove(e.HomePath(".config/flocc/permissions.toml"))` (ignore `fs.ErrNotExist`) before `return nil`.

- [ ] **Step 5: The old worktrees and the old tmux unit**

`installer/internal/steps/old_worktrees.go`:

```go
package steps

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// OldWorktrees removes ~/worktrees from a one-user host once nothing is claimed. Its worktrees are
// git worktrees of the service's clones, which the service now only fetches into.
type OldWorktrees struct{}

func (OldWorktrees) Name() string { return "Old worktrees" }

func (OldWorktrees) Check(ctx context.Context, e *Env) (bool, string, error) {
	if _, err := os.Stat(e.HomePath(worktreesRel)); errors.Is(err, fs.ErrNotExist) {
		return true, "none in " + e.Real(worktreesRel), nil
	}
	return false, e.Real(worktreesRel) + " is from the one-user layout", nil
}

func (OldWorktrees) Apply(ctx context.Context, e *Env) error {
	st, err := readStatus(ctx, e)
	if err != nil {
		return fmt.Errorf("flocc status: %w", err)
	}
	if len(st.Claims) > 0 {
		var keys []string
		for _, c := range st.Claims {
			keys = append(keys, c.Issue)
		}
		return fmt.Errorf("%s still claimed; release them with flocc release, then run flocc ship again", strings.Join(keys, ", "))
	}
	if err := os.RemoveAll(e.HomePath(worktreesRel)); err != nil {
		return err
	}
	clones, _ := filepath.Glob(e.HomePath("src/*"))
	for _, c := range clones {
		real := e.Real(filepath.Join("src", filepath.Base(c)))
		e.Run.Run(ctx, host.Cmd{Name: "git", Args: []string{"-C", real, "worktree", "prune"}})
	}
	return nil
}
```

In `installer/internal/steps/run.go`:
- In `Check` (systemd), delete the `is-active --quiet flocc-tmux.service` test and add before `if !timerOn(ctx, e)`:

```go
	if _, err := os.Stat(e.HomePath(".config/systemd/user/flocc-tmux.service")); err == nil {
		return false, "old flocc-tmux.service still installed", nil
	}
```

- In `Apply` (systemd), replace the `[]host.Cmd{e.userctl("daemon-reload"), e.userctl("enable", "--now", "flocc-tmux.service")}` loop and the wait loop after it with:

```go
		// 0.12 ran the session server as this user; it is flocc-agent's now.
		old := filepath.Join(dir, "flocc-tmux.service")
		if _, err := os.Stat(old); err == nil {
			e.Run.Run(ctx, e.userctl("disable", "--now", "flocc-tmux.service"))
			if err := os.Remove(old); err != nil {
				return err
			}
		}
		if _, err := e.MustCmd(ctx, e.userctl("daemon-reload")); err != nil {
			return err
		}
```

- In loop mode, the `flocc-loop` pane's server stays (it hosts the tick loop); change its comment to `// Idle until doctor passes; the loop pane lives on this user's own tmux server.`

In `installer/internal/steps/list.go`:

```go
func ServiceSteps() []Step {
	return []Step{Repo{}, ClaudeLogin{}, Secrets{}, Config{}, OldWorktrees{}, Run{}}
}
```

(`Identity` and `Tools` are the agent phase's: flocc commits nothing and runs no repo code.)

- [ ] **Step 6: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -l installer
git add installer
git commit -s -m "Service phase publishes /opt/flocc; login and its status go through the wrapper"
```

---

### Task 16: Docs, changelog, ledger, and the full check

**Files:**
- Modify: `CHANGELOG.md` (the `## Unreleased` section)
- Modify: `docs/book/getting-started.md`, `docs/book/commands.md`, `docs/book/architecture.md`, `docs/book/troubleshooting.md`, `docs/book/setup/running-a-host.md`, `docs/vm-setup.md`
- Modify: `.docket/` (through the docket CLI only)

- [ ] **Step 1: CHANGELOG**

Append to the bullets under `## Unreleased` in `CHANGELOG.md`:

```markdown
- Sessions run in `bypassPermissions`. Auto mode, `permissions.toml`, the host file's
  `[permissions]` table and its judge keys are gone; a leftover `permissions.toml` is ignored with
  a warning and ship deletes it.
- A PreToolUse guard denies `AskUserQuestion`, `EnterPlanMode`, `ExitPlanMode`, `git push`,
  GitHub writes through `gh` (`gh api` with `-f`/`-F` and no `-X` counts as a POST) and Linear
  tools, and keeps the reviewer to `review.md`. It replaces the pre-commit hook's review check.
  The guard and deny rules for the three dialog tools live in root-owned
  `/etc/claude-code/managed-settings.json`, which the sessions cannot change or turn off.
- A turn that ends without `flocc done` is refused once; ending again marks the session idle and
  the run ends blocked, with a comment on the issue. An API error other than a usage limit does
  the same. The idle and stall notices, `flocc press`, `flocc say`, `idle_escalate_s` and
  `stall_escalate_s` are gone. A crash still gets one restart.
- An issue's `Run:` line picks the archetypes, `executor, reviewer` by default, from `tester`,
  `executor` and `reviewer`. An unknown name or order skips the issue with one comment. Without a
  tester the publisher skips the tester-file guard. Fixers are added by the service as before.
- Four skills, executor, fixer, tester and reviewer, replace implementer, tester and reviewer.
  Every archetype ends with `flocc done`, which replaces `flocc review-done`. A summary with a
  "Needs the owner" heading ends the run blocked, except in a review fix round.
- Sessions, worktrees, the repo's `./setup` and check, and the Claude login belong to a second
  user, `flocc-agent`, which cannot read the service's keys, ledger or config, nor write
  `/opt/flocc`. The service reaches it only through `sudo` and
  `/usr/local/libexec/flocc-agent-run`. `flocc setup` and `flocc ship` create the user, the
  `flocc-work` group and the sudoers rule, move an existing Claude login to `flocc-agent`, and run
  a new `agent` phase.
- With `docker = true`, checks that start containers use rootless Podman as `flocc-agent`
  (`DOCKER_HOST` points at its `podman.socket`; testcontainers' Ryuk is off). Neither user may be
  in the `docker` group, which is root-equivalent: ship takes `flocc` out of it and doctor fails
  if `flocc-agent` is in it. An existing Docker daemon is left installed and untouched. A check
  that runs the `docker` CLI itself needs it installed by hand.
- The publisher fetches the branch from flocc-agent's worktree into the service's clone and pushes
  from there, so the publish token never enters a session.
- When an issue reaches Done or Canceled, or is released without `--keep-worktree`, its worktree,
  session files and scratchpads go; the branch stays on GitHub.
- `worktree_root` and `plugin_dir` in `config.toml` are fixed by the layout and ignored with a
  warning.
```

- [ ] **Step 2: The book**

Run `grep -rn -i -E "permissions\.toml|auto mode|flocc press|flocc say|review-done|implementer|handoff\.md|idle_escalate|stall_escalate|~/worktrees|flocc-tmux|AskUserQuestion" docs/book docs/vm-setup.md README.md`. For each hit, state the new behaviour from the CHANGELOG bullets above in that page's own style: `commands.md` drops `press`, `say` and `review-done`, and describes `flocc done`, `flocc guard`, `flocc stop --failure` and the `Run:` line; `architecture.md` gets a short "Two users" section with the Global Constraints' layout table, the sentence that the guard is a convenience and the user boundary is the security line, and one paragraph on managed settings and rootless Podman (why neither user is in the docker group), and its "Access and trust" paragraph that says agents could read the Linear key is rewritten to the two-user boundary; `troubleshooting.md` replaces the stall and press advice with "a blocked run says why on its issue; reply there and move it back to Todo", and adds "`sudo: a password is required` from flocc-agent-run: the sudoers rule is missing; run flocc ship"; `running-a-host.md` and `vm-setup.md` list the three setup phases and that `flocc login` signs in `flocc-agent`. Keep each page's existing headings.

- [ ] **Step 3: The ledger**

With the docket skill (`/home/novusedge/.claude/plugins/cache/NovusEdge/docket/0.31.0/bin/docket`), from the worktree:
- Record decisions, each with `scope` set to the files named: the guard's deny list (`src/flocc/guard.py`, `deploy/managed-settings.json`); the guard and dialog denies in managed settings, not the plugin, and no `allowManagedHooksOnly` (`deploy/managed-settings.json`, `installer/internal/steps/agent_user.go`); rootless Podman for flocc-agent instead of the docker group, Ryuk off, stopped containers pruned on remove, dockerd never touched (`installer/internal/steps/containers.go`, `src/flocc/agentrun.py`), referencing d176; the refuse-once Stop hook and the StopFailure marker (`src/flocc/cli.py`, `src/flocc/supervisor.py`); `Run:` order validation and the archetype-to-phase mapping (`src/flocc/sequence.py`); "Needs the owner" blocks except in the review-fix round (`src/flocc/supervisor.py`), referencing d161; the eleven wrapper subcommands and the token in the request (`src/flocc/agentrun.py`, `deploy/flocc-agent-run`); reading agent files through `agentfs` and never running git in an agent repo except `fetch` with `safe.directory=*` (`src/flocc/agentfs.py`, `src/flocc/worktree.py`, `src/flocc/publisher.py`); the agent setup phase and the login moving to flocc-agent (`installer/`).
- Supersede d156 (press and say) and d179 (the stall notice) with the guard and Stop-hook decisions; correct d167 (background tasks now stop the Stop hook from refusing or marking idle; the supervisor's idle and stall checks are gone); correct d33, d81 and d65 to say the trust, onboarding and plugin disabling happen in `flocc-agent-run start` as flocc-agent; correct d59 (reaping now also removes the session files and the claim).
- Record a claim that a PreToolUse deny applies in `bypassPermissions`, and one that a user-level `disableAllHooks` leaves managed hooks running, each with evidence "docs read 2026-10-30; confirmed in owner step 5" left for the owner to confirm.

Then:

```bash
git add .docket
git commit -s -m "docket: decisions for bypass sessions, the Run: sequence and two users"
```

- [ ] **Step 4: The full check**

Run `just check`, with `shellcheck` taken from `uvx --from shellcheck-py shellcheck` where the desktop lacks it (`PATH="$(dirname "$(uvx --from shellcheck-py which shellcheck)"):$PATH" just check` if `just` calls `shellcheck` by name).
Expected: ruff, pyrefly, shellcheck (`setup` and, if `just lint` is extended to it, `deploy/flocc-agent-run`), gofmt and `go vet` clean; `uv run pytest -q` and `go test ./...` PASS. Add `deploy/flocc-agent-run` to the justfile's `lint` recipe (`shellcheck setup deploy/flocc-agent-run`) in this step if it is not there.

- [ ] **Step 5: Sweep for leftovers**

```bash
grep -rn -E "permission_allow|permission_environment|judge_|autoModeEnvSetup|review-done|cmd_press|cmd_say|_check_stall|_check_idle|idle_escalate|stall_escalate|\"--permission-mode\", \"auto\"|implementer skill|handoff\.md|clean_env|installed_plugins_file|claude_json" src tests plugin installer deploy docs/book README.md
```

Expected: nothing (old dated specs and plans under `docs/superpowers/` are history and stay).

- [ ] **Step 6: Commit**

```bash
git add CHANGELOG.md docs justfile
git commit -s -m "Docs and changelog for bypass sessions, the Run: sequence and two users"
```

