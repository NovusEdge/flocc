# Troubleshooting

Most desktop commands check each step before they change it, so the first fix for a failed step is to run the same command again. `flocc -H <host> doctor` checks a host's config and setup, and `flocc -H <host> logs` shows what the service did.

## An issue is not picked up

Check each condition. The host starts an issue only when all hold.

1. The issue is assigned to the owner of the Linear API key.
2. It carries the `claude` label, or whatever `trigger_label` is set to.
3. It is in an unstarted state such as Todo, in the host's team.
4. A label or the Linear project is named exactly like a repository mapped on that host. If none matches, the service comments "No configured repo matches this issue's labels or project" once, unless `skip_unmapped = true`.
5. The description has an `Acceptance criteria`, `Done when` or `Definition of done` heading followed by a list. If not, the service comments once and skips the issue until the description changes.
6. The `agent` block, if any, parses. An unknown key, a `wall_clock` that is not like `90m` or `2h`, a missing closing fence or an `mcp` server the host does not configure gets a "Skipping:" comment. `allow` and `paths` are unknown keys now; delete those lines from older issues.
7. Admission allows it: the host is under `session_cap`, usage is below `five_hour_max_pct` and `weekly_max_pct`, free disk is above `min_free_disk_gb`, and every `depends_on` issue is done. `flocc -H <host> logs` prints `held, <reason>` for an issue that waits.

If the host's tick has not run, `flocc -H <host> status` shows the last tick. In systemd mode the timer ticks every 3 minutes.

## An issue is blocked or timed out

The claim stays until you release it. Fix what the issue's comment or the digest names, then move the issue back to Todo or run `flocc release ENG-12`. The next tick restarts it on the kept branch. A move to Todo while the run is still going does nothing.

A run that touched files the tester committed is refused at publish. Run `flocc release ENG-12 --accept-tests` to approve the tester's files as they stand on the branch.

A tester file the implementer fixed because its test was wrong, and recorded with `flocc tests-amended <path> --reason ...`, does not block. It goes into the PR's "Amended tests" section with the reason, and the reviewer judges it. An amendment that removes or skips a test blocks, and so does any touched tester file that is not recorded.

If a repo requires a label on PRs with amended tests, as nimblefox's `tests-amended` does, set `amended_label` on the repo in `config.toml` and flocc adds it.

A failed check gets `check_fix_rounds` (default 2) fix sessions before the claim blocks. A failure the base branch shows too holds the repo instead; `flocc canary <repo>` or a new commit on the base lifts it.

A rerun stops before any session starts if its kept branch and GitHub's copy both have commits the other lacks. Keep what's needed from the worktree, then release the issue again.

A failed `./setup` or a worktree that is not clean after `./setup` fails the start of the issue, and the service comments on the issue. Gitignore everything `./setup` and the check write.

## Keys

| Symptom | Fix |
|---|---|
| `flocc keys: a key on the command line stays in your shell history` | Run `flocc keys` and paste at the prompt, or use the environment or `.env`. See [Keys](keys.md) |
| A paste is refused by prefix | Linear keys start `lin_api_`, GitHub fine-grained tokens `github_pat_`. A classic token (`ghp_`, `gho_`) is refused |
| "Check the same paste again?" | The API gave no answer. Answer yes to retry. Answering no ends the command; run it again later |
| A token does not cover a repository | Edit the token on GitHub, add the repository under Repository access, then run `flocc keys check`. The value stays the same |
| The Linear key cannot write | Make a new key with Full access, then `flocc keys rotate linear` |
| "the OS keyring did not answer; unlock it and run again" | Unlock the keyring. flocc falls back to `~/.config/flocc/secrets.toml` after 30 seconds on first use |
| A value in `.env` is not taken | Only the `FLOCC_*` names in [Keys](keys.md) are read, only from the root of a flocc checkout. An environment variable of the same name overrides the file |

## Setup and ship

| Symptom | Fix |
|---|---|
| `flocc setup asks as it goes; run it in a terminal` | Setup needs a terminal. It exits 2 without one |
| `flocc ship ships the flocc checkout it runs in; cd into it` | Run `ship`, `setup` and `host create` inside the flocc git checkout |
| "<name> has no answers yet" | Run `flocc host import -H <name>` for a host that already runs flocc, or `flocc host add <name>` if it was never shipped |
| Ship stops at `sudo -n true` | Give the SSH user passwordless sudo, or use root |
| A connection fails within 30 seconds without a terminal | The host needs a Tailscale check-mode approval. Run `flocc -H <name> status` in a terminal to bring up the approval link |
| `flocc ship` exits non-zero | A doctor check failed. Each failing check prints below the summary rows with its fix. A pending Claude login alone exits 0 |
| A plugin in `deploy/agent-plugins.txt` stops ship | Enable that plugin on your desktop |
| A running session still has old plugins or settings | Sessions keep what they launched with. The upgrade reaches them at their next launch |

## Claude login

| Symptom | Fix |
|---|---|
| The login row says `token-only` or `none` | Run `flocc login -H <name>`. A `claude setup-token` token cannot open Remote Control sessions |
| `flocc login` waits for sessions | Running sessions rewrite `~/.claude.json` from memory and could drop the Remote Control consent. Wait for them, or stop |
| No connected line within 60 seconds | Run `claude remote-control` as `flocc` on the host by hand to see what it waits for |
| The digest issue says the login lapsed | The tick reports it once, and again only after a login has been seen. Run `flocc login -H <name>` |
| "login" shows `unknown` in setup | The host could not read its login. Setup shows it and does not act |

## Host create

| Symptom | Fix |
|---|---|
| Create stops with commands to run | Incus lacks `incusbr0`, a root pool with room, or free memory. Run the printed commands, then create again. It never sets Incus up itself |
| `sudo on <machine> asks for a password; run create in a terminal` | Run create in a terminal, or give the account passwordless sudo |
| `the root pass failed: ... its log is /tmp/flocc-create.XXXXXX/setup.log` | Read that log on the machine, fix what it names, then run create again |
| The VM has no HTTPS | Check the firewalld and Docker rules in [Create a host](setup/host-create.md#what-create-changes-on-the-machine), then run create again |
| Create names a Docker bridge and its zone | Bind it with `sudo firewall-cmd --zone=docker --change-interface=<bridge>`, or restart Docker when nothing depends on it, then run create again |
| Create stops on firewalld's iptables backend, or Docker's nftables backend | Set `FirewallBackend=nftables` in `/etc/firewalld/firewalld.conf` and restart firewalld. Docker's own nftables firewall backend has no `DOCKER-USER` chain and is not supported |
| Create stops on an older Docker | `DOCKER-FORWARD` exists from Docker 28.0 on |
| The tailnet step fails naming the Tailnet section | The policy does not define `tag:flocc-host`. See [Tailnet](tailnet.md) |
| "naming the stale node to remove" | A node of that name already exists in the tailnet. Remove it in the admin console |

Create keeps a log at `~/.cache/flocc/create-<name>.log`.

## Tailnet

| Symptom | Fix |
|---|---|
| The Policy step stays pending | Paste `~/.cache/flocc/tailnet-policy.json` at `https://login.tailscale.com/admin/acls/file`, then answer yes to check again. Setup checks up to three times per run |
| "the client lacks Policy file read" | Add the scope to the OAuth client, or make a new one |
| A host is not tagged | Tag it at `https://login.tailscale.com/admin/machines`, or fix the policy and client scopes and run `flocc setup` again |
| `ssh root@<host>` stops working after tagging | The policy lacks the `ssh` accept rule for the tag. Paste the merged policy |

## Disk

A full disk breaks SSH and the installer. `flocc doctor` warns below twice `min_free_disk_gb` and fails below it. See [Run a host](setup/running-a-host.md#disk-use) for the caches and how worktrees are removed.
