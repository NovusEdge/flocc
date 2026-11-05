# Run a host

This page covers what you tune after a host is shipped: how it ticks, its config, disk use, several hosts at once, and blocked tool calls.

## How a host ticks

Debian, Ubuntu, Arch, Fedora and RHEL VMs run in systemd mode. A timer ticks every 3 minutes as `flocc`, and a user unit of `flocc-agent` keeps the tmux server up.

`flocc ship` sets a host up in three phases: `root` creates `flocc-agent`, the `flocc-work` group, the wrapper `/usr/local/libexec/flocc-agent-run`, its sudoers rule, the managed settings and, with `docker = true`, rootless Podman; `agent` runs as `flocc-agent` and installs Claude Code, the plugins, mise, the host file's tools and the session tmux server; `service` runs as `flocc` and publishes `/opt/flocc`, then runs doctor. See [Two users](../architecture.md#two-users).

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
| `review_model` | `opus` | Model for the reviewer session |
| `digest_hour` | 18 | Hour of the daily digest comment |
| `min_free_disk_gb` | 2.0 | New starts are held below this free space |
| `skip_unmapped` | false | Ignore issues that match none of this host's repositories |
| `steer_authors` | none | Linear user or app ids, not names, whose comments on a running issue steer the session like yours |
| `shared_uv_cache` | false | One uv cache for every setup |

Each `[repos.<name>]` table maps a repository. It takes `path`, `base_branch`, `draft` and `commit_trailer`. A private repository of a GitHub Free organization cannot have draft PRs, so set `draft = false` there.

## Disk use

A worktree with its virtual environment can reach 1.4 GB, and uv's download cache (`~/.cache/uv`) grows with every repository's `./setup`. That can fill a small disk (a Daytona sandbox has 10 GB) and break SSH and the installer. Three things limit it:

- Each worktree's `./setup` runs with `UV_CACHE_DIR` pointing at `<worktree_root>/.uv-cache-<KEY>` and `UV_LINK_MODE=hardlink`. That directory is deleted when setup ends, whether it succeeds, fails or times out. Every setup therefore downloads its packages fresh, and the virtual environment keeps its own files. The cache sits on the worktree's filesystem so uv can still link instead of copy.
- `min_free_disk_gb` holds new starts while the disk holding `worktree_root` has less free space than that. `flocc doctor` warns below twice the value and fails below it, and a doctor failure also stops a fresh install from starting the timer.
- Each tick removes the worktree and session files, not the branch, of a finished run whose Linear issue is completed or canceled, or no longer exists. Running and blocked claims, and issues in any other state, keep theirs, so a rerun from Todo still reuses it. A timed-out run with uncommitted changes, new files included, keeps its worktree. A removed worktree is recreated from the kept branch if the issue is rerun.

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

When `./setup` fails in a fresh checkout of a repo's base branch, the failure is main's or the host's, and every issue of that repo would repeat it. flocc comments once on the issue that hit it, pushes, and holds the repo's other issues. The hold ends when a new commit lands on the base branch, after 24 hours, or when `flocc -H <host> canary <repo>` passes. Run that after fixing something on the host, which moves no commit. Every `flocc ship` clears all holds with `flocc canary --forget`. A `./setup` that times out holds only its own issue.

## What the host carries for the repos

Three host-file keys install what repos' setups and checks need and the service user cannot install itself:

```toml
playwright = "1.64.0"   # chromium's system libraries and fonts, on apt hosts
docker = true           # rootless Podman for flocc-agent's container checks (on apt hosts)

[tools]                 # mise global versions for flocc-agent
go = "1.27.1"
```

Use the Playwright version the repos pin. A tool in `[tools]` must be one mise can install, at an exact version or a prefix such as `1.27`; `flocc ship` sets it with `mise use -g`, and `latest` never reads back as set. The `docker` key keeps its name for old host files. It installs Podman, gives `flocc-agent` subordinate IDs and starts its `podman.socket`, and ship takes `flocc` out of the `docker` group, which is root-equivalent. An existing Docker daemon is left installed and is never stopped or restarted. A check that runs the `docker` CLI itself needs the CLI installed by hand.

## Blocked tool calls

Host sessions run in `bypassPermissions`, so no permission dialog can hold one, and there is no `[permissions]` table. A guard hook in the root-owned managed settings denies the dialog tools (`AskUserQuestion`, `EnterPlanMode`, `ExitPlanMode`), `git push`, GitHub writes through `gh` and Linear tools, and it keeps the reviewer to `review.md`. The agent hears the reason and works around it. A `permissions.toml` left by an older version is ignored with a warning, and ship deletes it.

## When a run needs you

The service comments on the issue when a run blocks, when `./setup` fails at prepare, and when a turn ends without `flocc done`. Problems that belong to no issue, such as a failed tick, an expiring GitHub token or a lapsed Claude login, go to the digest issue and the service log. Nothing is pushed to your phone.

A session does not ask you questions. Where the spec leaves a choice open it takes the default, and it passes what needs you as `--needs-owner` questions to `flocc done`. That ends the run blocked with the questions on the issue; in the review fix round it goes into the final comment instead. To steer a running session, comment on the issue; the service types the comment into the session.

## Upgrade notes

- `worktree_root`, `plugin_dir`, `idle_escalate_s` and `stall_escalate_s` are ignored with a warning: the two-user layout fixes the paths, and the idle and stall notices are gone. Ship needs the host drained of claims, because it removes `~flocc/worktrees`; release them first.
- `ntfy_url`, `context_restart_pct` and a repo's `shared_paths` are ignored with a warning; delete them from `config.toml`. An `agent` block with an `allow:` or `paths:` line is skipped with a "Skipping:" comment until the line is removed. The `escalation` and `needs-decision` labels are no longer used; delete them in Linear if you like.
- `digest_issue` is required. A config without it fails to load, which fails every tick and the agents' pre-commit hook, so commits in running sessions are refused. Add it before upgrading.
- A host must be on 0.3.0 or later before you ship 0.5.0 to it. 0.5.0 no longer reads the old names or refuses a host that still has them. A branch whose tester commits carry only the trailer from before the rename is no longer recognised, unless the host already recorded the tests. Finish or merge such issues on a 0.4.x host before upgrading it. `CHANGELOG.md` has the trailer's exact text.
- An agent token imported from a host older than 0.5.0 needs Actions (read) and Commit statuses (read) added to its permissions.

## Check a new host with a scratch issue

Run this once by hand before you trust the timer with real issues.

1. Create a scratch GitHub repository with a `justfile` whose `check` runs its tests. Map it with `flocc host add`, and create one issue in a test Linear team: labelled `claude` and the repository's label, assigned to you, in Todo, with an `## Acceptance criteria` list.
2. Run `flocc -H my-host tick`. It takes the same lock as the timer's ticks. The issue should move to In Progress, and a `-test` session should appear in `flocc -H my-host status`.
3. Keep ticking until a PR exists and Linear has the PR comment, then until the review has run. The PR should have a `flocc review` comment, and the issue a `Review: …` comment.
4. Exercise a rerun: block a second issue, then run `flocc release ENG-13`. The issue should be back in Todo, and the next tick restarts it on the kept branch.

When every step passes, remove the scratch issue and repository mapping.
