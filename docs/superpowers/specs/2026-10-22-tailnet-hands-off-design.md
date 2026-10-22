# Hands-off tailnet: tagged hosts without the QR login

Part 4 of the desktop CLI redesign (NovusEdge/flocc#49). It adds a section and steps to the event
session in `2026-10-22-cli-tui-design.md` and depends only on that spec's part 1.

## Problem

A host joins the tailnet as the owner. `flocc host create` runs `tailscale up --ssh` in the VM
with a QR or link login, or a pasted one-off auth key (`create/tailscale.go`), and Daytona hosts do
the same from `steps/tailscale.go`. The node then holds the owner's identity, so root on a host
(cardinal, or anything a host session escapes into) can open connections to the owner's devices,
including Tailscale SSH where a check is fresh.

On 2026-10-22 the owner tagged `flocc-nimble` as `tag:flocc-host` by hand, with a policy that
grants the tag no destinations; from the host, ports 22 and 443 on the desktop and laptop then
refused. Nothing in flocc does this for a new host, and the QR login is a step the owner wants gone.

## Goal

`flocc setup` and `flocc host create` bring every host onto the tailnet tagged, without a QR
login, after one-time steps the owner follows from instructions. Setup checks the result and says
what is missing.

## Decisions

| | Decision |
|---|---|
| Credential | A Tailscale OAuth client the owner creates once in the admin console, with scopes `auth_keys` and `devices:core` limited to `tag:flocc-host`, and `policy_file:read`. Stored in the desktop keyring as a new key slot. |
| No policy write | flocc never writes the policy. The owner pastes it once; setup generates it and checks it. |
| Tag | `tag:flocc-host` on every host. The owner's laptop and desktop stay user-owned. |
| Without the client | Setup still works: create uses the QR login, with `--advertise-tags` only once a node on the tailnet already carries the tag (nothing else shows the policy defines it); setup writes the generated policy to paste, and untagged hosts get a pending row with the console link. |

Rejected: a published Tailscale app with a consent flow (Tailscale's OAuth apps are alpha and
"do not support users from other tailnets"; there is no app directory); a `policy_file` write
scope (a leaked desktop copy could rewrite every access rule on the tailnet, to save one paste);
tagging the laptop (it would lose the owner's identity, and the owner uses it).

| Scope | Lets flocc | If the desktop's copy leaks |
|---|---|---|
| `auth_keys` on `tag:flocc-host` | mint a one-off tagged key per new host | machines can join as `tag:flocc-host`, which reach nothing |
| `devices:core` on `tag:flocc-host` | tag an existing host | machines can be retagged `tag:flocc-host` |
| `policy_file:read` | check the policy and print a merged file | the policy can be read |

## Setup

A Tailnet section runs after Keys and before Hosts, because `host create` needs it.

1. **Client.** The key slot asks for the OAuth client's ID and secret with the console steps:
   Settings → OAuth clients → Generate, the three scopes above, tag `tag:flocc-host`. Leaving it
   empty skips to the fallback. The check exchanges the client for an access token.
2. **Policy.** Setup reads the policy. It needs `tagOwners` for `tag:flocc-host`, no grant with the
   tag as a source, an `ssh` accept rule from `autogroup:member` to the tag for `root` and
   `autogroup:nonroot`, and the tests below. When something is missing, setup prints the policy
   with the missing parts merged in, names what it added, and gives the link
   `https://login.tailscale.com/admin/acls/file`; the step stays pending until a re-check passes.
   The owner's other rules are kept as they are.
3. **Hosts.** For each host in the host files, `tailscale status --json` on the desktop gives its
   node and tags. An untagged host is tagged through `devices:core`; without the client it gets a
   pending row with the Machines link.

The policy flocc generates for a tailnet that has nothing else (the shape the owner applied on
2026-10-22; `hosts` and the tests are filled from `tailscale status --json`):

```hujson
{
	"tagOwners": {"tag:flocc-host": ["autogroup:admin"]},
	"grants": [
		{"src": ["autogroup:member"], "dst": ["*"], "ip": ["*"]},
	],
	"ssh": [
		{"action": "check",  "src": ["autogroup:member"], "dst": ["autogroup:self"], "users": ["autogroup:nonroot", "root"]},
		{"action": "accept", "src": ["autogroup:member"], "dst": ["tag:flocc-host"], "users": ["root", "autogroup:nonroot"]},
	],
	"tests": [
		{"src": "tag:flocc-host", "deny": ["<each owner device>:22", "<each owner device>:443"]},
	],
	"sshTests": [
		{"src": "<owner login>", "dst": ["tag:flocc-host"], "accept": ["root"]},
	],
}
```

The `accept` rule has to be in place before a host is tagged: the default SSH rule covers only the
owner's own devices, so `ssh root@<host>` stops the moment the tag lands. Setup orders policy before
tagging for that reason.

## Host create

With the client, the Tailnet step mints a one-off, pre-approved, non-ephemeral key tagged
`tag:flocc-host` that expires in 10 minutes, and runs

```
tailscale up --ssh --hostname=<name> --advertise-tags=tag:flocc-host --auth-key=file:/dev/stdin
```

with the key on stdin, as the pasted-key path does now. The step needs no terminal and is no longer
`Attached`. Tagged nodes have key expiry off, so the "disable key expiry" warning goes.

Without the client it runs the QR login with `--advertise-tags=tag:flocc-host` as a handoff. If
the tag is not in the policy yet, `tailscale up` refuses; the step fails naming the Tailnet section
of `flocc setup`.

Daytona hosts get the same key through their answers file, replacing the interactive `up` in
`steps/tailscale.go`.

## Testing

- The policy check and merge run against fixture policies: empty, the default, the one applied on
  2026-10-22, and one with the owner's own rules.
- The API calls go through an interface with a fake; tests cover a refused token, a tag the policy
  does not define, and a host already tagged.
- `create` tests assert the `up` arguments with and without the client and that the key never
  reaches the log.

## To confirm before the plan

The docs did not settle these:

- Whether `--advertise-tags` belongs to `tailscale up`, `tailscale login`, or both, with an
  interactive login on a fresh node.
- The error text `tailscale up` prints for a tag the policy does not define, which the step needs
  to tell "policy not pasted yet" from other failures.
- The OAuth token and key-creation endpoints and request bodies, and whether `devices:core` can tag
  a user-owned node without a re-login, as the console does.

A throwaway VM and a throwaway OAuth client answer all three.
