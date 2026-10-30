# Keys

flocc holds four keys on your desktop. `flocc keys` makes, checks and stores them, and `flocc ship` sends them to each host.

| Key | Stored as | Used for | `.env` name |
|---|---|---|---|
| Linear API key | `linear_key` | The service reads issues, comments and moves issues | `FLOCC_LINEAR_API_KEY` |
| GitHub agent token | `agent_token` | Read-only. Agent sessions run `gh` with it | `FLOCC_GITHUB_AGENT_TOKEN` |
| GitHub publish token | `publish_token` | The service pushes branches, opens PRs and posts the review | `FLOCC_GITHUB_PUBLISH_TOKEN` |
| Tailscale OAuth client | `tailscale_oauth` | Optional. Joins and tags hosts without a browser login | `FLOCC_TAILSCALE_OAUTH_SECRET`, and optionally `FLOCC_TAILSCALE_OAUTH_CLIENT_ID` |

## Make the keys

```bash
flocc keys
```

`flocc keys` asks for each key the desktop does not have yet, and only in a terminal. A paste shows as dots, so a stdin that is not a terminal is refused. A key is never taken as a command-line argument, so none lands in shell history. `flocc keys` refuses an argument that looks like a key.

For each key it opens the page where the key is made in your browser and prints what to choose there. Paste the key at the prompt. An empty paste stops the command, except for the optional Tailscale client, where it skips the key.

`flocc setup` asks for the same keys at the points where it needs them, with the same prompts and checks.

### Take keys from the environment or a .env file

`flocc setup` and `flocc keys` take each key from the environment or a `.env` file before they prompt.

The `.env` is read from the root of the flocc git checkout you run flocc inside, and only when that checkout is flocc's. Outside a flocc checkout only the environment counts. A variable of the same name in the environment overrides the file.

The repository ships `.env.example`. The `setup` script in the checkout root copies it to `.env` (mode 600) when `.env` is missing, and `flocc setup` run in the checkout does the same.

The recognised names are in the table above. Other names in the file are ignored. The format is `KEY=VALUE`, with an optional `export ` before the name, `#` comments, and quotes around a value stripped:

```bash
# .env
export FLOCC_LINEAR_API_KEY="lin_api_…"
FLOCC_GITHUB_AGENT_TOKEN=github_pat_…
```

Each value is handled like this:

- A value equal to the stored key is skipped.
- A new or changed value is checked as a paste is. See [How a key is checked](#how-a-key-is-checked).
- A changed Linear key or GitHub token replaces the stored one, and flocc offers to push it to every host, as `flocc keys rotate` does.
- A value that fails its check is shown masked, with the reason, and the stored key stays. The mask is the key's public prefix, its last four characters and its length, for example `lin_api_…ab12, 48 characters`. A value with no known prefix shows only its length.

{% hint style="warning" %}
A `.env` that is readable by group or others is set to mode 600, with a note. Keep it out of version control: it holds live keys.
{% endhint %}

## Where each key is made

### Linear API key

flocc opens `https://linear.app/settings/account/security`. Create a key named `flocc` with Full access, because the service writes comments and moves issues. The key must see the team of each host.

### GitHub tokens

flocc opens GitHub's fine-grained token page with the name, description, resource owner and the longest expiry (366 days) filled in. It prints the repositories to tick beside it, because GitHub cannot prefill those. Choose "Only select repositories" and tick them.

A fine-grained token serves one owner, so map only repositories of one owner per token.

| Token | Page name | Permissions |
|---|---|---|
| Agent | `flocc agent` | Contents, Pull requests, Issues, Commit statuses and Actions, all read-only |
| Publish | `flocc publish` | Contents (write) and Pull requests (write) |

Fine-grained tokens have no Checks permission. Actions (read) is what shows workflow runs and their logs, and Commit statuses (read) covers the rest.

The publish token's page opens only after the agent token has passed its check.

### Tailscale OAuth client

See [Tailnet](tailnet.md). flocc opens `https://login.tailscale.com/admin/settings/oauth`.

## How a key is checked

Each paste is checked before it is stored, first by its prefix and then against the service's API without changing anything.

| Key | Prefix | Live check |
|---|---|---|
| Linear | `lin_api_`. A GitHub token here is refused at once | The key must see the hosts' teams and be able to write. It sets a digest issue's title to the title it already has |
| GitHub agent | `github_pat_`. A classic token (`ghp_`, `gho_`) is refused | Reads every mapped repository and must not be able to push |
| GitHub publish | `github_pat_` | Must be able to push and open pull requests on every mapped repository |
| Tailscale | `tskey-client-` | The client is exchanged for an access token |

If an API gives no answer (an outage or a rate limit), the paste is not stored and you are asked "Check the same paste again?". Answering no ends the command, and the next `flocc keys` asks for the key again.

When a working GitHub token does not cover a repository, nothing needs pasting. On github.com/settings/personal-access-tokens open the token (`flocc agent` or `flocc publish`), choose Edit, add the repository under Repository access (the token's value stays the same), then run `flocc keys check`. This is typical for the publish token after `host add` mapped a new repository.

## Check the stored keys

```bash
flocc keys check
```

This names the store in use and runs every check again. It reports each failing key with the next step, and exits 1 if any key fails. A rejected GitHub or Linear key is reported with `flocc keys rotate github` or `flocc keys rotate linear` as the fix.

## Rotate a key

```bash
flocc keys rotate github     # the agent and publish tokens
flocc keys rotate linear     # the Linear key
```

`rotate github` reopens both token pages. An empty paste keeps a token. It checks the new ones and only then stores them, so Ctrl-C during the checks changes nothing. It then writes them into every host's secret files over SSH, each file mode 600, written beside the old one and renamed over it.

- It lists the hosts it could not reach. Once those are up, run the command again, paste nothing and answer yes to "Push the stored keys to ... again?". Or run `flocc ship -H <host>`, which replaces a host's key file whenever the desktop's key differs.
- Like `host import`, rotate opens each host's SSH connection in the terminal first, so a Tailscale check-mode approval link shows.
- Do not revoke the old GitHub token until the sessions running now have ended. Each session reads `GH_TOKEN` when it starts.
- `flocc status -H all` shows the new expiry after each host's next daily token check.

`rotate linear` does the same for the Linear key.

## Where keys are stored

Keys live in the OS keyring (Secret Service, or macOS Keychain). Without one they go in `~/.config/flocc/secrets.toml`, mode 600.

- If `secrets.toml` already exists, flocc uses it even after a keyring appears, so keys never split between two stores. A file readable by group or others is set to mode 600, with a note.
- A locked keyring can show an unlock prompt and wait. flocc tells you after 2 seconds, and gives up after 30 and falls back to the file. Later keyring calls time out after a minute with "the OS keyring did not answer; unlock it and run again".
- `flocc keys check` prints which store is in use.

On each host the keys are files in `~flocc/.config/flocc/`, mode 600.
