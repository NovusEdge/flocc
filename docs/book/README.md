# flocc

flocc runs Claude Code sessions on Linear issues and opens pull requests for the result. You label an issue, assign it to yourself and move it to Todo. A host you run picks it up, works on it in its own git worktree, and opens a draft PR. You review and merge.

It is built for one owner running a few hosts, not for a team sharing one service.

## What you run

| Part | Where it runs | What it does |
|---|---|---|
| `flocc` (Go) | Your desktop | Sets hosts up, stores your keys, ships the service to a host, shows the fleet |
| `flocc` (Python service) | Each host | Polls Linear every 3 minutes, runs Claude sessions in tmux, publishes PRs |

A host is any machine or VM you can reach over SSH, usually on a Tailscale tailnet. Sessions run as an unprivileged `flocc` user.

## Before you start

You need:

- A Linear workspace and a personal API key for it.
- Two GitHub fine-grained tokens: a read-only one for the agents and one that can push and open pull requests.
- A Claude account with a claude.ai login for each host. A `claude setup-token` token is not enough, because it cannot open Remote Control sessions.
- A machine to host on. `flocc host create` builds an Incus VM on a Linux machine you reach over SSH. A Daytona sandbox also works.

{% hint style="warning" %}
flocc drives your Claude subscription from a script. Check Anthropic's current terms before you point it at work repositories, and agree it with your employer first.
{% endhint %}

## Where to go next

1. [Getting started](getting-started.md) takes you from nothing to a first PR.
2. [Keys](keys.md) and [Tailnet](tailnet.md) explain the two parts of setup that need accounts outside flocc.
3. [Commands](commands.md) lists every desktop command.
4. [Troubleshooting](troubleshooting.md) covers what to do when a step fails.
