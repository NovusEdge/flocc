# Tailnet

Hosts join your Tailscale tailnet tagged `tag:flocc-host`. A tagged node has no owner identity, so a policy that grants the tag no destinations keeps a compromised host off your other devices. flocc can join and tag hosts with no browser login if you give it a Tailscale OAuth client. This page covers the client and the policy it checks.

`flocc setup` runs a Tailnet section after the first Keys step and before Hosts, because `host create` depends on it. flocc never writes your tailnet policy. It generates one for you to paste, and checks it.

## Create the OAuth client

1. Open `https://login.tailscale.com/admin/settings/oauth`, choose Generate, and create a client.
2. Give it these scopes:

   | Scope | Tag | Lets flocc | If a copy leaks |
   |---|---|---|---|
   | Auth keys (write) | `tag:flocc-host` | Mint a one-off tagged key for each new host | Machines can join as `tag:flocc-host`, which reaches nothing |
   | Devices core (write) | `tag:flocc-host` | Tag an existing host | Machines can be retagged `tag:flocc-host` |
   | Policy file (read) | none | Check your policy and print a merged one | The policy can be read |

3. Run `flocc keys` and paste the client secret, which starts with `tskey-client-`.

The client secret alone is enough. The `id:secret` form also works. An empty paste skips the client. flocc checks the paste by exchanging it for an access token, which changes nothing.

You can also supply the client from `FLOCC_TAILSCALE_OAUTH_SECRET`, and optionally `FLOCC_TAILSCALE_OAUTH_CLIENT_ID`. See [Keys](keys.md#take-keys-from-the-environment-or-a-env-file).

`flocc setup` asks for the client on the first run only, when you have no host files yet. The client is optional, so a later run does not ask again for as long as you decline it. `flocc keys` still offers it.

## Without a client

Everything still works. Hosts join with the login link and you tag them by hand at `https://login.tailscale.com/admin/machines`. Setup lists the missing client under "still yours to do".

When no node on the tailnet carries the tag yet, setup also writes a policy to paste. See [The policy](#the-policy). Once a host is tagged, later `host create` runs advertise the tag themselves.

## What setup does

1. **Client.** Looks for the stored client. On the first setup it offers to store one.
2. **Tailnet nodes.** Reads `tailscale status --json` on the desktop for every node and its tags.
3. **Policy.** With a client, reads your policy and checks it. Without one, writes a policy to paste if no node carries the tag.
4. **Hosts.** For each host file, finds the node by its SSH name. A node already tagged is done. An untagged one is tagged through the client, but only after the policy passes. Without the client, or before the policy passes, the host gets a pending row naming the machines page.

The order matters. Until the `ssh` accept rule for the tag exists, `ssh root@<host>` stops working the moment the tag lands, because the default SSH rule covers only your own devices.

## The policy

With a client, setup reads your policy and checks that it has:

- A `tagOwners` entry for `tag:flocc-host`.
- No grant or ACL with the tag as a source. The tag must be able to reach nothing.
- No grant or ACL from `*`. Tailscale's default grant from `*` covers tagged nodes too, so it counts against the policy.
- An `ssh` rule with action `accept` from `autogroup:member` to `tag:flocc-host`, for the users `root` and `autogroup:nonroot`.
- A test that denies the tag ports 22 and 443 on each of your devices.

When something is missing, setup merges the missing parts into your policy and writes the result to `~/.cache/flocc/tailnet-policy.json` (mode 600). Your other rules are kept as they are. Setup names what it added, and asks you to paste the file at `https://login.tailscale.com/admin/acls/file`.

It then asks "Pasted and saved the policy?" and checks again, up to three times. The step stays pending until the check passes.

The merge adds only what is missing:

| Added | Content |
|---|---|
| `tagOwners` | `tag:flocc-host` owned by `autogroup:admin` |
| `ssh` | An `accept` rule from `autogroup:member` to the tag, for `root` and `autogroup:nonroot` |
| `hosts` and `tests` | An alias for each of your untagged devices, and a test that the tag cannot reach those aliases on ports 22 and 443 |
| `sshTests` | A test that you can SSH to the tag as `root` |
| `src` of `*` | Narrowed to `autogroup:member` in grants and ACLs |

A grant or ACL with the tag as its source is reported but never rewritten. Remove it yourself.

The file starts with a one-line comment saying it was merged from the JSON the Tailscale API returns. Comments in the policy you pasted last are not kept, so compare the file with your policy before you paste. A policy that is not a plain JSON object is refused.

### When there is no client

Setup cannot read your policy. If no node carries the tag, it writes a policy for a tailnet that has nothing else to `~/.cache/flocc/tailnet-policy.json`, and tells you to paste it at the policy page. Pasting replaces the policy there. If you have rules of your own, add only its `tag:flocc-host` parts.

The generated policy gives every member access to everything, keeps SSH to your own devices as it was, and gives the tag nothing:

```json
{
	"tagOwners": {"tag:flocc-host": ["autogroup:admin"]},
	"grants": [
		{"src": ["autogroup:member"], "dst": ["*"], "ip": ["*"]}
	],
	"ssh": [
		{"action": "check",  "src": ["autogroup:member"], "dst": ["autogroup:self"], "users": ["autogroup:nonroot", "root"]},
		{"action": "accept", "src": ["autogroup:member"], "dst": ["tag:flocc-host"], "users": ["root", "autogroup:nonroot"]}
	],
	"tests": [
		{"src": "tag:flocc-host", "deny": ["<each of your devices>:22", "<each of your devices>:443"]}
	],
	"sshTests": [
		{"src": "<your login>", "dst": ["tag:flocc-host"], "accept": ["root"]}
	]
}
```

Setup fills `hosts`, the tests and the `sshTests` login from your tailnet.

If a node already carries the tag, the policy defines it and setup leaves your policy alone.

## Messages you may see

| Message | Meaning |
|---|---|
| The client lacks Policy file read | Add the scope to the OAuth client, or make a new one |
| The tailnet policy lacks `tag:flocc-host` | Paste the merged policy, then run `flocc setup` again |
| The client lacks Devices core on `tag:flocc-host` | Tag the host by hand at the machines page, or fix the client's scope |
| Tailscale refused the client; make a new one | The client was revoked or the secret is wrong |

## Tagged and untagged nodes

Tagged nodes have key expiry off, so there is nothing to disable in the admin console. A node that comes up untagged still needs key expiry disabled by hand, or its key expires after 180 days.

For an Incus VM, [`flocc host create`](setup/host-create.md#joining-the-tailnet-in-step-5) mints a one-off key (pre-approved, tagged, valid 10 minutes) with the client and runs `tailscale up` with it. A Daytona sandbox gets the same key through its answers file when a client is stored, and joins by login otherwise.

{% hint style="info" %}
Three details were written from Tailscale's documentation and tested against fakes, not confirmed on a live tailnet: whether `tailscale up --advertise-tags` accepts the tag on a fresh node, the exact error `tailscale up` prints for a tag the policy does not define (create matches on the word "tags"), and whether Devices core can tag a user-owned node without a re-login.
{% endhint %}
