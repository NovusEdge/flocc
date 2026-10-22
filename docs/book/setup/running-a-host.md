# Run a host

This page covers what you tune after a host is shipped: how it ticks, its config, disk use, several hosts at once, auto-mode blocks and the limits on subagents.

## How a host ticks

Debian, Ubuntu, Arch, Fedora and RHEL VMs run in systemd mode. A timer ticks every 3 minutes and a user unit keeps the tmux server up.

Any host whose PID 1 is not systemd runs in loop mode. `flocc run` in tmux session `flocc-loop` ticks instead, and the installer's `flocc-loop` session starts the tmux server. A process a timed-out check daemonized is not killed when its tick ends. After the host stops, `flocc ship` restarts tailscaled and the loop.

Watch a host from the desktop:

```bash
flocc -H my-host status           # claims, last tick, usage, disk, Claude login, token expiries
flocc -H my-host logs -f          # the journal in systemd mode, run.log in loop mode
flocc -H my-host attach ENG-12    # read-only tmux
```

## Configuration

The host reads `~/.config/flocc/config.toml`. `flocc ship` writes it from the host file's answers. `deploy/config.example.toml` in the repository lists every key; these are the ones you are most likely to change:

| Key | Default | Meaning |
|---|---|---|
| `trigger_label` | set at setup (`claude`) | Label that makes an issue ready |
| `session_cap` | 2 | Most concurrent sessions the host runs |
| `five_hour_max_pct`, `weekly_max_pct` | 80.0 | New issues are held once usage reaches these shares of each window |
| `default_wall_clock_s` | 10800 | Per-session time limit; an issue's `wall_clock` overrides it |
| `context_restart_pct` | 60.0 | A session past this context use writes a handoff note and restarts |
| `review_model` | `opus` | Model for the reviewer session |
| `digest_hour` | 18 | Hour of the daily digest comment |
| `min_free_disk_gb` | 2.0 | New starts are held below this free space |
| `skip_unmapped` | false | Ignore issues that match none of this host's repositories |
| `steer_authors` | none | Linear user or app ids, not names, whose comments on a running issue steer the session like yours |
| `shared_uv_cache` | false | One uv cache for every setup |

Each `[repos.<name>]` table maps a repository. It takes `path`, `base_branch`, `draft`, `commit_trailer` and `shared_paths`. A private repository of a GitHub Free organization cannot have draft PRs, so set `draft = false` there.

## Disk use

A worktree with its virtual environment can reach 1.4 GB, and uv's download cache (`~/.cache/uv`) grows with every repository's `./setup`. That can fill a small disk (a Daytona sandbox has 10 GB) and break SSH and the installer. Three things limit it:

- Each worktree's `./setup` runs with `UV_CACHE_DIR` pointing at `<worktree_root>/.uv-cache-<KEY>` and `UV_LINK_MODE=hardlink`. That directory is deleted when setup ends, whether it succeeds, fails or times out. Every setup therefore downloads its packages fresh, and the virtual environment keeps its own files. The cache sits on the worktree's filesystem so uv can still link instead of copy.
- `min_free_disk_gb` holds new starts while the disk holding `worktree_root` has less free space than that. `flocc doctor` warns below twice the value and fails below it, and a doctor failure also stops a fresh install from starting the timer.
- Each tick removes the worktree, not the branch, of a finished run whose Linear issue is completed or canceled, or no longer exists. Running and blocked claims, and issues in any other state, keep theirs, so a rerun from Todo still reuses it. A timed-out run with uncommitted changes, new files included, keeps its worktree. A removed worktree is recreated from the kept branch if the issue is rerun.

On a host with room to spare, `shared_uv_cache = true` replaces the throwaway caches with one `<worktree_root>/.uv-cache` kept across setups, so only the first setup of a dependency set downloads it. Nothing prunes that cache, and `min_free_disk_gb` still holds new starts when it fills the disk.

This covers uv inside `./setup` only. Sessions still use `~/.cache/uv`, and the npm, pnpm, pip and mise caches are not managed.

## Running more than one host

Each host keeps its own ledger, so nothing stops two hosts from claiming the same issue. Split the repositories instead: every repository is mapped on exactly one host, and an issue is picked up by the host that maps its label or project.

- Each issue must carry exactly one repository label or repository project. An issue with two repositories' labels matches on both hosts and is worked twice.
- Every host must use the same Linear key, because ready issues are read from the key's assigned issues. Hosts on different keys see different issues.
- Set `skip_unmapped = true` on each host. Without it, a host comments "No configured repo matches" on every ready issue that belongs to the other host's repositories. The installer asks "Do other hosts handle the unticked repos?" when you untick discovered repositories in the config form, and writes the key if you say yes. `shared_team = true` in an answers file does the same. Doctor prints a `skip_unmapped` line when it is on.

The cost of `skip_unmapped` is that a typo in a label no longer gets that comment, so an unmapped issue just sits in Todo.

The hosts share the owner's Claude login and so its 5-hour and weekly usage limits. Each applies `five_hour_max_pct` and `weekly_max_pct` to the same account, so size the caps with that in mind. Tailscale hostnames must differ between hosts. `flocc status -H all` shows all hosts together.

## A repo whose main fails setup

When `./setup` fails in a fresh checkout of a repo's base branch, the failure is main's or the host's, and every issue of that repo would repeat it. flocc comments once on the issue that hit it, pushes, and holds the repo's other issues. The hold ends when a new commit lands on the base branch, after 24 hours, or when `flocc -H <host> canary <repo>` passes. Run that after fixing something on the host, which moves no commit.

## What the host carries for the repos

Three host-file keys install what repos' setups and checks need and the service user cannot install itself:

```toml
playwright = "1.64.0"   # chromium's system libraries and fonts, on apt hosts
docker = true           # docker.io, the daemon, and the service user in the docker group

[tools]                 # mise global versions for the service user
go = "1.27.1"
```

Use the Playwright version the repos pin. A tool in `[tools]` must be one mise can install; `flocc ship` sets it with `mise use -g`. The docker group is root-equivalent on the VM, and it reaches sessions only once the service user's systemd manager restarts (`systemctl restart user@<uid>` as root, when no session is running).

## Auto-mode blocks

Host sessions run in auto mode. When its classifier blocks a command, the agent hears why and works around it or gives up. Nothing waits for you. After three blocks in a row Claude Code shows a prompt, which denies itself after two minutes. Every block and prompt is logged per issue in `~/.local/state/flocc/<KEY>/permissions.jsonl`.

`flocc -H <host> permissions report` counts them. Turn what you see into the host file's `[permissions]` table, then run `flocc ship -H <host>`. Sessions pick it up at their next launch.

```toml
[permissions]
environment = [
  "Each session runs in a disposable VM. The worktree, ~/worktrees and /tmp are the agent's own.",
  "Nothing on this VM is production. The agent's GitHub token is read-only.",
]
allow = ["Bash(npx playwright install)", "Bash(uv sync --frozen)"]
```

`environment` lines address whole classes of block and come first. `allow` rules are exact. A rule that wildcards an interpreter or shell (`Bash(python:*)`, `Bash(sh *)`, anything with `-c`) is dropped when the config loads, with a warning in `flocc logs`. The rest still apply.

## Subagents in agent sessions

By default a session may launch only the agents in `plugin/agents/`, and only as often as its phase allows. Grants, below, can add `general-purpose` or a named workflow. The budgets (which agents, the phase's total launches, how many run at once) are in `src/flocc/gate.py`. The review phase gets none. A hook refuses any other launch and tells the agent why.

Every launch, allowed or refused, is appended to `<state_dir>/launches.jsonl`, and `flocc status` shows each running claim's count as `launches N/limit`.

To allow more, put an `allow:` line in the issue's `agent` block, or in a comment of yours on the issue. A comment from a `steer_authors` author works too. Items are comma-separated:

```
allow: launches 14, agent code-mapper, workflow <name>, fix-rounds 2
```

- A comment is applied once the supervisor has delivered it to the live session.
- A `launches` grant in the agent block applies to the implementer phase. From a comment it applies to the phase running when the comment arrives. `fix-rounds` raises the implementer's fix rounds.
- `agent general-purpose` and `workflow <name>` change the session's launch flags. From a comment they take effect at the next handoff, and until then the gate refuses them with the rule `next-session`. The other items apply within one tick.
- A granted workflow starts once per run and counts as one launch. `launches N` is the phase's total launches, workflows included.
- Comment grants do not survive a release: a new claim discards them and rewrites only the agent block's `allow:` line. To keep a grant across a release, put it in the agent block.

## Questions and escalations

The implementer and fix sessions are the only ones connected to Remote Control. When one needs an answer it asks with `AskUserQuestion`, which is meant to arrive in the Claude app with a push. A question left unanswered for two hours is escalated by the service as tier 2. While a question is open, a reply on the issue is held and delivered after the question is answered, and the context handoff request waits too.

Implementer and fix sessions still use `flocc escalate`, with a default, for reversible choices. Tester and reviewer sessions are not on Remote Control and escalate with `--waiting` for decisions that block them.

| Tier | Examples | Linear record | Push |
|---|---|---|---|
| 1. Interrupt now | A secret about to be pushed, a failure blocking every run, a lapsed Claude login | `escalation` label | ntfy, high priority |
| 2. Interrupt if idle | An irreversible decision blocking this agent | `needs-decision` label and a comment with the question, default and deadline | ntfy, normal priority, only when the session would otherwise sit idle |
| 3. Digest | Reversible decisions made alone, draft PRs ready, failed or capped runs | One daily comment on the digest issue | None |
| 4. Log only | Retries that succeeded | None | None |

Answer a tier 2 question in a Linear comment, which the service forwards to the session, or directly through Remote Control.

## Upgrade notes

- `digest_issue` is required. A config without it fails to load, which fails every tick and the agents' pre-commit hook, so commits in running sessions are refused. Add it before upgrading.
- A host must be on 0.3.0 or later before you ship 0.5.0 to it. 0.5.0 no longer reads the old names or refuses a host that still has them. A branch whose tester commits carry only the trailer from before the rename is no longer recognised, unless the host already recorded the tests. Finish or merge such issues on a 0.4.x host before upgrading it. `CHANGELOG.md` has the trailer's exact text.
- An agent token imported from a host older than 0.5.0 needs Actions (read) and Commit statuses (read) added to its permissions.

## Check a new host with a scratch issue

Run this once by hand before you trust the timer with real issues.

1. Create a scratch GitHub repository with a `justfile` whose `check` runs its tests. Map it with `flocc host add`, and create one issue in a test Linear team: labelled `claude` and the repository's label, assigned to you, in Todo, with an `## Acceptance criteria` list.
2. Run `flocc -H my-host tick`. It takes the same lock as the timer's ticks. The issue should move to In Progress, and a `-test` session should appear in `flocc -H my-host status`.
3. Keep ticking until a PR exists and Linear has the PR comment, then until the review has run. The PR should have a `flocc review` comment, and the issue a `Review: …` comment.
4. Exercise escalation. In a session run `flocc escalate --tier 1 --cause smoke --summary test --action ignore`, then tick. You should get one ntfy push and the `escalation` label. Repeat the command and tick again: you should get a second comment and no second push.
5. Exercise a rerun: block a second issue, then run `flocc release ENG-13`. The issue should be back in Todo, and the next tick restarts it on the kept branch.

When every step passes, remove the scratch issue and repository mapping.
