# Getting started

This page takes you from a fresh checkout to a pull request opened by a host. Each step links to the page that covers it in full.

## 1. Install the desktop command

1. Clone the flocc repository and run `./setup` in it. It installs `mise`, the pinned tools (Go and uv) and the Python dependencies.
2. Build the command:

   ```bash
   go install ./installer/cmd/flocc     # puts flocc in ~/go/bin
   ```

3. Put `~/go/bin` on your `PATH`.

Run `flocc ship`, `flocc setup` and `flocc host create` from inside the checkout. They ship that checkout's commit to the host, so they need the checkout and Go on the desktop.

## 2. Run the setup

```bash
flocc setup
```

`flocc setup` needs a terminal. It walks through the whole setup and asks as it goes. Run it again at any point: each step checks first and repairs only what is missing.

In order, it:

1. Checks that you are in a flocc checkout.
2. Asks for the Linear key and the GitHub agent token. See [Keys](keys.md).
3. Checks the Tailscale OAuth client and your tailnet policy. See [Tailnet](tailnet.md).
4. Creates or adds a host, then ships the service to it. See [Set up the desktop and a host](setup/README.md).
5. Asks for the GitHub publish token.
6. Offers to remove old installs it finds on the machine.
7. Signs in to Claude on each host.

## 3. Prepare a repository

The host works in a worktree of the repository's base branch. Two things must hold in every mapped repository:

- Everything `./setup` and the check command write is gitignored. A worktree that is not clean after `./setup` fails the start of an issue.
- The repository provides a check command, `just check` by default. The service runs it before it pushes.

`flocc host add` maps repositories to the host: it lists the ones the agent token can read and offers them ticked.

## 4. Write an issue the host will pick up

The host starts an issue only when all of these hold:

| Condition | Detail |
|---|---|
| Assigned to you | The owner of the Linear API key |
| Labelled `claude` | The label `flocc host add` creates, set by `trigger_label` |
| In an unstarted state | Todo, not Backlog or In Progress |
| In the host's team | The team key in the host file |
| Mapped to a repository | A label or the Linear project is named exactly like a mapped repository |
| Has acceptance criteria | A heading `Acceptance criteria` followed by a list |

An issue with no matching repository gets one comment and is left alone. An issue without acceptance criteria gets one comment and is skipped until its description changes.

A session reads only the issue's title, description and new comments. It does not open Linear attachments or follow links, so put specs in the repository and name their paths in the description.

An example description:

````markdown
Add rate limiting to the export endpoint.

## Acceptance criteria

- Requests over 10 per minute per user get a 429.
- The limit is configurable.
- Tests cover both cases.

```agent
model: opus
wall_clock: 2h
depends_on: [ENG-120]
```
````

The optional `agent` block takes these keys:

| Key | Meaning |
|---|---|
| `model` | Model for the session |
| `effort` | Reasoning effort: `low`, `medium`, `high`, `xhigh` or `max` |
| `wall_clock` | Time limit, such as `90m` or `2h` |
| `depends_on` | Issues that must be done first |
| `mcp` | MCP servers the session may use, from the host's `[mcp_servers.*]` config |
## 5. Watch it run

The host polls every 3 minutes. Add the `claude` label last. It starts paid, unattended work.

```bash
flocc                               # the dashboard
flocc -H my-host status             # claims, last tick, usage, disk
flocc -H my-host logs -f
flocc -H my-host attach ENG-12      # read-only view of the session
```

The issue moves to In Progress, an executor session does the work, and the service runs the check, pushes, opens a draft PR and comments the link on the issue. A reviewer session then posts a review on the PR. You merge by hand. A `Run: tester, executor, reviewer` line in the description adds a tester session that writes failing tests first.

## Run an issue again

Move the issue back to Todo once its run has finished: blocked, timed out or PR opened. The next tick restarts it on the same branch. A move to Todo while the run is still going does nothing. From a terminal, `flocc release ENG-12` does the same. Your comments posted since the last run stopped go into the restarted session's context, so you can write what to change before moving the issue. A comment on a running issue goes to the live session instead.
