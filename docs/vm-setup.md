# Host setup

`flocc ship -H <name>` sets a host up and is safe to run again: it checks every step, skips the
finished ones and resumes at the first that is not. `<name>` is a host the desktop knows, added
with `flocc host add` (see The desktop command below); its host file holds the SSH destination,
such as the host's Tailscale name, or the Daytona sandbox. It needs `go` (from `mise install`),
`gh` logged in on the desktop, and, for Daytona, the Daytona CLI logged in with `daytona login`.

`flocc ship -H <name>` runs without a terminal: it sends the host file's answers and the stored
keys as one answers document on stdin over one SSH session, and nothing prompts; the ntfy topic's
test push happened once, in `flocc keys`. The SSH session is forced to have no TTY (`ssh -T`, so a
`RequestTTY` setting cannot echo the document back). The document must have `team_key`,
`digest_issue` and at least one `[[repos]]`; a host file without them is refused with a pointer to
`flocc host import -H <name>`. The SSH user must be root or have passwordless sudo, and `sudo` must be
installed: ship checks `sudo -n true` before it stages anything and stops otherwise. Answers mode
skips the Tailscale login (it only starts `tailscaled`), so on a host that is not yet on your
tailnet run `tailscale up --ssh --hostname=<name>` there yourself and disable key expiry
(`flocc host create` does both for an Incus VM). No secret is written on the host
except the service's own secret files (mode 600, in `~flocc/.config/flocc/`), and none appears in a
command line, a `setup.log` or the output; the installer also replaces the secret values of the
answers document with `<token>` in its log (secrets typed into `--interactive` forms are not in
that set; no step logs them). The host keeps its answers, without secrets, in
`~flocc/.config/flocc/answers.toml`, and its secret files.

The document looks like this:

```toml
team_key = "NF"
digest_issue = "NF-186"
ntfy_topic = "flocc-a-long-random-topic"
shared_team = false

[[repos]]
name = "nimblefox"
url = "https://github.com/NovusEdge/nimblefox"
base_branch = "main"
check = "just check"
draft = true

[secrets]
linear_key = "lin_api_…"
agent_token = "github_pat_…"
publish_token = "github_pat_…"
```

`flocc ship -H <name> --interactive` runs over `ssh -t` and asks on the host's terminal instead: the
Tailscale login, the three secrets, the config form and the ntfy test push. It sends no answers
document; afterwards it copies the host's answers back into the host file, as `flocc host import`
does. Everything either mode runs is logged to `/tmp/flocc-setup/setup.log` (root steps)
and `~flocc/.cache/flocc-setup/setup.log` (the rest). The root phase must run from
`/tmp/flocc-setup`, where `flocc ship` puts it; running `flocc-setup` by hand from anywhere else is
refused.

Ship never logs in to Claude. It ends with a summary: the version on the host, the Claude login
(full, token-only or none), the GitHub token expiries, each failed doctor check, the timer and the
live sessions. It exits non-zero when a doctor check fails, except the Claude login while that
login is pending. On a fresh
host the login is pending, so the tmux unit runs, the timer stays off and ship exits 0. The command
to run next is on the summary's `claude login` row, as `flocc login -H <name>` with the host you
gave ship; each failed doctor check prints below the other rows.

## Claude login

```bash
flocc login -H <name> [--email you@example.com]
```

Sessions need a full claude.ai login: a `claude setup-token` token cannot open Remote Control
sessions. `flocc login` runs on the host as `flocc`, over `ssh -t`:

1. It takes the tick lock for the whole login. If a tick is running it waits for it; if sessions
   are running it says so and asks whether to wait for them or stop, because running sessions
   rewrite `~/.claude.json` from memory and could drop the Remote Control consent. While it waits
   for sessions it lets the lock go, so ticks keep supervising them.
2. Without a claude.ai login it runs `claude auth login --claudeai`. The sign-in URL opens in your
   desktop browser and prints as a QR code. Sign in and paste the code at the prompt in the same
   terminal. If `claude auth status` still does not show a claude.ai login, it stops there.
3. It marks `~/worktrees` trusted (sessions trust only their own worktree, so `remote-control`
   would stop at the folder-trust dialog), then runs `claude remote-control` once under a pty
   (`script`), answers its `Enable Remote Control?` prompt with `y`, waits for its connected
   line and stops it. If no connected line appears within 60 seconds it fails and shows the output;
   run `claude remote-control` as `flocc` by hand to see what it waits for.
4. It checks `claude auth status`, runs doctor and starts the timer, then prints the summary.

The host must have been shipped with 0.4.0 or later. When the login lapses, the next tick sends one
tier 1 push; it pages again only after a login has been seen in between. Shipping to a host whose
login has lapsed leaves its already-enabled timer on, so ticks keep running and the one page
stands until you run `flocc login -H <name>`.

Upgrading the service on a host is `flocc ship -H <name>` again: it ships the current commit as
a git bundle, since the repo is private and the host has no credential for it. A session already
running keeps the plugin, hooks and settings it was launched with; the upgrade reaches it at its
next launch or handoff.

## The desktop command

Everything on the desktop goes through `flocc`, built from this repo:

```bash
go install ./installer/cmd/flocc     # puts flocc in ~/go/bin
```

Run `flocc ship` from inside the flocc checkout: ship sends that checkout's commit and builds the
installer from it, so it needs the checkout and Go on the desktop.

A new host takes these commands, in this order:

```bash
flocc keys                 # once per desktop: the Linear key, both GitHub tokens, the ntfy topic
flocc host add flocc-nimble
flocc ship -H flocc-nimble
flocc login -H flocc-nimble
```

`flocc keys` first is the usual order, but not required: `host add` and `ship` ask for any key
the desktop does not have yet, with the same prompt and checks.

A host that already runs flocc (shipped before 0.5.0, or from another desktop) has no answers in
a host file, only `ssh`, and `flocc ship` refuses it with a pointer to the import. Import it first,
so the desktop takes its answers, its keys and its ntfy topic instead of making new ones:

```bash
flocc host import -H flocc-nimble   # its answers, plus each key the desktop lacks
flocc keys                          # asks only for what the host did not have
```

Run `flocc keys` before the import and it mints a new ntfy topic; the next ship then moves the
host off the topic your phone follows.

The import reads each key from the host's secret files and checks it as a paste is checked
before storing it, printing "Took the ..." with a GitHub token's expiry or the Linear key's
account and teams, never a value. The import opens the SSH connection in the terminal first, so a
Tailscale check-mode approval link shows; without a terminal, a host that needs a prompt fails
within 30 seconds with a message saying so. If the connection drops partway, it stops with "run flocc host import again"
instead of asking for a new token, because GitHub shows a token only once. A host token that does
not cover a repo is not taken; the message names the token. On
github.com/settings/personal-access-tokens open it, Edit, fix the repository access, then run the
import again. An agent token from a host shipped before 0.5.0 also lacks Actions (read) and Commit
statuses (read), which 0.5.0 asks for: add both to its permissions there; the token's value stays
the same.

`flocc keys` asks for each key it does not have yet, and only in a terminal: it reads with echo
off, so a stdin that is not a terminal is refused. For each key it opens the page where the key
is made, with a QR code for a phone; GitHub's token page arrives with the name, description,
owner, permissions and the longest expiry (366 days) filled in, and the repos to tick are printed
beside it. A fine-grained token serves one owner, so map only repos of one owner per token. Paste
the key at the prompt: nothing shows while you paste, and a key is never taken as a command-line
argument, so none lands in shell history. Each paste is checked before it is stored, first by
its prefix (a Linear key in a GitHub slot is refused at once), then against the APIs without
changing anything: the Linear key must see the hosts' teams and be able to write (it sets a
digest issue's title to the title it already has, so make the key with Full access), the agent
token must read every mapped repo and must not be able to push, and the publish token must be
able to push and open pull requests. The agent token asks for Contents, Pull requests, Issues,
Commit statuses and Actions, all read-only; fine-grained tokens have no Checks permission, and
Actions read is what shows workflow runs and their logs. The publish token's page opens only
after the agent token passed. When an API gives no answer (an outage, a rate limit), the paste is
not stored, and you are asked "Check the same paste again?"; answering no ends the command, and
the next `flocc keys` asks for the key again. Last comes the ntfy topic: scan its QR code with
the ntfy app and confirm the test push. Keys live in the OS keyring (Secret Service, macOS
Keychain); without one they go in `~/.config/flocc/secrets.toml`, mode 600, and once that file
exists it is used even after a keyring appears. `flocc keys check` names the store in use and
runs every check again.

When a stored token does not cover a repo, typically the publish token after `host add` mapped a
new repo, nothing needs pasting: on github.com/settings/personal-access-tokens open the token
("flocc agent" or "flocc publish"), Edit, add the repo under Repository access (the token's value
stays the same), then run `flocc keys check`.

`flocc host add <name> [--ssh DEST | --daytona SANDBOX]` writes
`~/.config/flocc/hosts/<name>.toml`, the only place a host's answers live: the repos the agent
token can read (offered ticked), the Linear team (picked for you when the key sees one), the
digest issue (another host's on the same team, else an open issue titled `flocc digest`, else a
new one to pin), and the labels `claude`, `escalation`, `needs-decision` and one per repo, created
when missing. It then checks the keys again against every host's repos and says what a token
lacks. The SSH destination defaults to `root@<name>`; `all` cannot be a host name. `flocc host
import -H <name>` builds the file from a host's own saved answers instead; `flocc ship -H <name>
--interactive` runs the host's own forms and copies its answers back.

`flocc keys rotate github` reopens both token pages (an empty paste keeps a token), checks the new
ones, and only then stores them and writes them into every host's secret files over SSH; Ctrl-C
during the checks changes nothing. It lists the hosts it could not reach. Once those are up, run
the command again, paste nothing and answer yes to "Push the stored keys to ... again?", or run
`flocc ship -H <host>`, which replaces a host's key file whenever the desktop's key differs, so it
brings that host's keys in line with the desktop. Like `host import`, rotate opens each host's
SSH connection in the terminal first, so a Tailscale check-mode approval link shows. Do not revoke the old token until the sessions running now have ended:
each reads `GH_TOKEN` when it starts. `flocc status -H all` shows the new expiry after each host's
next daily token check. `flocc keys rotate linear` does the same for the Linear key.

Any other command runs on a host as `flocc`, with the host's own grammar, so every remedy line a
host prints works on the desktop with `-H` added:

```bash
flocc -H flocc-nimble doctor
flocc release NF-12 --keep-worktree   # the host holding NF-12
flocc -H flocc-nimble logs -f
flocc -H flocc-nimble attach NF-12-review
flocc status -H all                   # every host, with its Claude login and token expiries
flocc host run -H flocc-nimble -- tmux -L flocc ls
```

Without `-H` the host is `$FLOCC_HOST`, then the only host, then, for a command naming an issue
key, the host whose `flocc status --json` holds that claim. `-H` is taken from anywhere before
`--`, so an argument that is literally `-H` goes after `--`. `-H all` runs only `status`, `doctor`
and `logs` (without `-f`). SSH to a host, `flocc ship` and `flocc login` included, shares one
connection per host for ten minutes, so a Tailscale check-mode approval covers a whole run.
`flocc ship`, `host import`, `keys rotate`, `doctor` and `logs` under `-H all`, and the lookup of
the host holding an issue open that connection in the terminal first, so the approval link shows.
Without a terminal, a host that needs a prompt fails within 30 seconds with a message saying so.
`status -H all` does not open connections first: a host waiting on an approval shows as
unreachable there, and `flocc -H <name> status` brings up its link.
`flocc status` for one host also shows its Claude login (full, token-only, none or unknown) and
the GitHub token expiries. `ship`, `login`, `keys` and `host` are desktop commands; the host's
`flocc` refuses them.

## Hosts

Debian, Ubuntu, Arch, Fedora and RHEL VMs run in systemd mode: a timer ticks every 3 minutes
and a user unit keeps the tmux server up. Any host whose PID 1 is not systemd runs in loop mode:
`flocc run` in tmux session `flocc-loop` ticks instead (the installer's `flocc-loop` session
starts the tmux server), a process a timed-out check daemonized is not killed when its tick
ends, and after the host stops `flocc ship` restarts tailscaled and the loop.

A Daytona container for a smoke test, about $0.17 an hour while running:

```bash
daytona create --snapshot daytona-medium --name flocc-smoke --auto-stop 0
flocc host add flocc-smoke --daytona flocc-smoke
flocc ship -H flocc-smoke              # or --interactive
flocc login -H flocc-smoke
# when done
daytona stop flocc-smoke
```

`--auto-stop 0` matters: a running tick loop does not count as activity, so the default
15-minute auto-stop ends it mid-run. A stopped sandbox bills only its disk. If
`daytona-medium` has less than 2 vCPU and 4 GiB, pass `--cpu 2 --memory 4096` instead of
`--snapshot`.

## Making a host with flocc host create

`flocc host create` makes a flocc host as an Incus VM on a Linux machine you reach over SSH with
your own account, such as a laptop on the tailnet. Agents run unreviewed code, so the host is a VM
rather than a container:

```bash
flocc host create [--cpus 4] [--memory 8] [--disk 40] <machine> <name>
```

`<name>` becomes the Incus instance, the Tailscale name and the flocc host name; it is 2 to 63
lowercase letters, digits and `-`, starting with a letter. Memory and disk are in GiB (disk at
least 10); size a host like the existing one (`incus config show <vm> --expanded`). On the machine
your account needs `incus-admin` membership or passwordless sudo for Incus, and passwordless sudo
when firewalld or Docker runs there: create runs every root command as `sudo -n` and stops with
instructions rather than wait for a password. This desktop must be on the tailnet. Run create
inside the flocc checkout, since its ship step ships that commit; create checks this before it
touches the machine.

Create checks each step before changing anything, so running the same command again continues a
host that stopped half-made. In order, it:

1. checks that Incus has the managed bridge `incusbr0`, a root pool with room for the disk, and the
   memory free, and otherwise stops with the commands to run (it never sets Incus up itself); the
   commands carry `sudo` when your account reaches Incus through sudo rather than `incus-admin`;
2. scopes firewalld, when it runs (below);
3. lets the VM through Docker's forward drop, when Docker runs, or is enabled or socket-activated
   while stopped (below);
4. checks that no tailnet node already has `<name>`, launches `images:debian/13` as a VM with
   `boot.autostart=true`, and waits for an IPv4 address and outbound HTTPS;
5. joins the VM to the tailnet with Tailscale SSH: scan the QR code or open the link, or paste a
   one-off auth key, which reaches the VM on stdin and is neither stored nor logged. A VM already
   logged in is renamed and given SSH instead of logging in again; the control server confirms the
   new name within 15 seconds or create stops, naming the stale node to remove;
6. waits until this desktop's tailnet lists the node, runs `flocc host add <name> --ssh
   root@<name>` unless `hosts/<name>.toml` exists, then `flocc ship -H <name>` and
   `flocc login -H <name>`;
7. takes the Incus snapshot `installed`, replacing an older one only after the new one exists;
   `incus snapshot restore <name> installed` returns the VM to it. Each successful run retakes it.

The snapshot holds the host's tokens and Claude login as of that run, so after a restore run
`flocc ship -H <name>` again if keys were rotated since. On a `dir` storage pool a VM snapshot is a
full copy of the root image, which the Incus step's room check does not count.

Create opens the first SSH connection to the machine in your terminal, so a Tailscale SSH
check-mode re-auth URL shows there, and the approval covers the rest of the run. The log is
`~/.cache/flocc/create-<name>.log`. If the VM has no HTTPS, check the firewalld and Docker rules
below, then run create again.

### What create changes on the machine

The machine is shared with other work, so create adds rules scoped to `incusbr0` and never starts,
stops or restarts Docker: on 2026-10-20 a Docker restart killed another session's image build.

| What | Where | Effect |
|---|---|---|
| zone `flocc-incus` | firewalld | `incusbr0` moves into it. It allows only `dhcp` and `dns` (Incus's dnsmasq), so the VM reaches none of the machine's own services. |
| policy `flocc-incus-out` | firewalld | Forwards `flocc-incus` to the zone of the default route's interface. |
| policy `flocc-no-docker` | firewalld, when Docker made its `docker` zone | Rejects `flocc-incus` to `docker`, ahead of Docker's `docker-forwarding` policy, which accepts forwarding from every zone into Docker's bridges and published ports. |
| `flocc-docker-user.service` | `/etc/systemd/system/` | A oneshot with `After=` and `PartOf=docker.service` that adds `-i incusbr0 -j DOCKER-FORWARD`, `-i incusbr0 -j ACCEPT` and `-o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT` to `DOCKER-USER`, in that order, after every Docker start (ip6tables too, where Docker made that chain). The jump hands the VM's traffic to Docker's own forwarding chain first, so the VM reaches published container ports on every Docker network and no others; the rest returns to the `ACCEPT`. `DOCKER-FORWARD` exists from Docker 28.0 on, and create stops on an older Docker. It has no `ExecStop`, so the rules stay when Docker stops: a stopped Docker leaves `FORWARD` at `DROP`, and removing them would cut the VM off. With Docker stopped it is enabled but not started, and runs at Docker's next start. |

A new zone or policy needs one `firewall-cmd --reload`, which create runs whenever the runtime or
the permanent configuration differs from the wanted one. A reload drops every firewalld change that
was never made permanent, whoever made it, so on a machine other people use, compare
`firewall-cmd --list-all-zones` with the same command under `--permanent` first and look at any
runtime-only entry other than Docker's bridge bindings. `flocc-no-docker` covers only zone
`docker`, so while Docker runs create also checks that `docker0` and each `br-<12 hex digits>`
bridge are in it. A reload unbinds them until Docker binds them again, and create waits up to 10 s
for that after its own reload. If a bridge is still in another zone, create stops naming the bridge
and its zone, and leaves Docker alone: bind it with
`sudo firewall-cmd --zone=docker --change-interface=<bridge>`, which lasts until the next reload, or
restart Docker when nothing depends on it, then run create again.

With firewalld's iptables backend, Docker's `DOCKER-USER` chain accepts the VM's traffic before
`flocc-no-docker` sees it, so create stops before it changes anything: set
`FirewallBackend=nftables` in `/etc/firewalld/firewalld.conf`, restart firewalld, then run create
again. Docker with its nftables firewall backend has no `DOCKER-USER` chain, so create stops there
too. Without firewalld nothing keeps the VM off the machine's services, and create says so.

firewalld forwards by zone, so the VM can also reach any other interface in the uplink's zone. That
includes `tailscale0` when it is in that zone: traffic forwarded out through it is masqueraded as
the machine, and create's Firewalld line says when this holds. If the default route moves to an
interface in another zone (Wi-Fi to Ethernet), run create again to move the policy.

The new zone has no intra-zone forwarding, so with `br_netfilter` loaded (Docker loads it) two VMs
on `incusbr0` cannot reach each other.

To take it all off a machine (the VMs on `incusbr0` then have no way out until the bridge gets
another zone):

```bash
sudo systemctl disable --now flocc-docker-user.service
sudo rm /etc/systemd/system/flocc-docker-user.service && sudo systemctl daemon-reload
# the unit has no ExecStop, so its rules stay until removed; the ip6tables lines fail harmlessly
# where Docker made no IPv6 DOCKER-USER chain
sudo iptables -D DOCKER-USER -i incusbr0 -j DOCKER-FORWARD
sudo iptables -D DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -D DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
sudo ip6tables -D DOCKER-USER -i incusbr0 -j DOCKER-FORWARD
sudo ip6tables -D DOCKER-USER -i incusbr0 -j ACCEPT
sudo ip6tables -D DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
sudo firewall-cmd --permanent --delete-policy=flocc-no-docker
sudo firewall-cmd --permanent --delete-policy=flocc-incus-out
sudo firewall-cmd --permanent --zone=flocc-incus --remove-interface=incusbr0
sudo firewall-cmd --permanent --delete-zone=flocc-incus
sudo firewall-cmd --reload
```

### Verified on zer0-day-nimble, 2026-10-21

The laptop ran Docker 29 with its iptables backend under firewalld 2.5.2 (nftables), a kairos-demo
stack and the host flocc-nimble. A trial host made there with `--cpus 2 --memory 4 --disk 20` and
then deleted showed:

- The VM reached HTTPS over v4 and resolved DNS. Every port the laptop listens on, other than DNS,
  refused it. Docker containers' own addresses dropped every ping and port it tried.
- After create's reload, Docker's bridges were back in zone `docker`, and `DOCKER-USER` held the
  three rules for v4 and v6. Creating a Docker network left them alone.
- The laptop's own broad settings then came off by hand: `FORWARD` to `DROP` for v4 and v6, and
  `ip-forward-no-drop` out of `daemon.json` (kept as `daemon.json.pre-flocc`). Both VMs kept v4
  egress, and the kairos-demo containers kept the internet and each other.
- Not exercised: a container port published on `0.0.0.0` (kairos-demo binds `127.0.0.1`), and
  v6 egress, which the laptop's uplink lacks.

A host VM has no swap, so a test suite that grows past the VM's memory makes the guest thrash
instead of the OOM killer stepping in: flocc-nimble stopped answering `incus exec` and Tailscale
for over 30 minutes. Installing `earlyoom` in the guest has it kill the largest process first.

### The VM on the tailnet

The VM joins the tailnet as you, untagged, with Tailscale SSH on. Under the default policy it can
therefore reach every device on the tailnet and, through the machine's uplink, the LAN. The rules
above keep it off the machine itself and off Docker's containers, not off the rest of the network.
Closing that means tagging the node with a policy that denies its outbound tailnet traffic, which
changes how hosts log in; that decision is open. After the join, disable key expiry for `<name>` at
https://login.tailscale.com/admin/machines (create prints this reminder).

## What the installer cannot do

- Linear: pin the digest issue. `flocc host add` creates the digest issue and the labels
  `claude`, `escalation`, `needs-decision` and one per mapped repo when they are missing, but
  `host import` does not: a host file written by import or by hand needs them in the team already.
  A repo's label is named exactly as its `[repos.<name>]` key; a repo without one is mapped but
  gets no issues. Doctor fails on a missing `claude`, `escalation` or `needs-decision` label or
  digest issue and warns about missing repo labels.
- GitHub tokens are made in the browser. `flocc keys` opens each page with the permissions
  filled in and prints the repos to tick.
- In the Tailscale admin console, disable key expiry for the host; an untagged node's key
  otherwise expires after 180 days.
- In each mapped repo, gitignore everything `./setup` and the check write, or every issue fails
  at prepare. If the mapped repos need tools outside `~/.local/bin`, the mise shims and the
  system directories, add them to `Environment=PATH` in `deploy/flocc.service` and to
  `loopCommand` in `installer/internal/steps/run.go`.
- On your desktop, add the same scratchpad backstop the installer writes on hosts:
  `echo 'e /tmp/claude-* - - - 2d' | sudo tee /etc/tmpfiles.d/claude-scratch.conf`. The service
  deletes a run's recorded scratchpads when its claim stops running; this line catches anything
  left over. Loop-mode hosts have no tmpfiles timer and rely on the service alone.
- `deploy/agent-plugins.txt` decides which of your plugins agent sessions get. A plugin listed
  there must be enabled on your desktop, or `flocc ship` stops and names it.

Watch it from the desktop with `flocc -H <name> status` (claims, last tick, usage, disk, Claude login, token expiries),
`flocc -H <name> logs -f` (the journal in systemd mode, `run.log` in loop mode) and
`flocc -H <name> attach NF-123` (read-only tmux).

### Fleet view

`flocc status -H all` shows every host in two tables: one row per host (mode, last tick and exit
status, running sessions against the cap, 5h and weekly usage, free disk, Claude login, token
expiry) and one row per live claim (phase, age, context use, whether its tmux session is up).
`flocc status -H all --watch --interval 30s` redraws until Ctrl-C.

The hosts are every sandbox in the active Daytona organization, read with the Daytona CLI's login,
plus every host in `~/.config/flocc/hosts/` reached over SSH (`flocc host add` writes them;
Daytona hosts come from the Daytona API).

Each started host answers `flocc status --json`, which only reads state: it takes no tick
lock and calls neither Linear nor GitHub. (On a WAL ledger, opening it read-only still creates
the `-shm` and `-wal` files beside it; the ledger file itself is not changed.) Stopped sandboxes
are listed with their state. The `est $/h` column prices started sandboxes only, from vCPU,
memory and disk beyond 5 GiB at Daytona's list rates, so it is an estimate; stopped and archived
sandboxes show `-` because their rate is not confirmed. A host file whose `ssh` starts with `-`
is skipped with a note, since ssh would read it as an option. An
unreachable host gets a row and an error line, and the rest still render. Cells are colored for usage at
the configured `five_hour_max_pct` and `weekly_max_pct`, disk under 15% free, a last tick older
than 10 minutes, and a non-zero tick exit. LOGIN is the host's Claude login, red unless it is a
full claude.ai login (yellow `unknown` when the host could not read it); TOKENS is the GitHub
token that expires first, yellow under 14 days and red under 3 days or when GitHub rejected it.
Without a Daytona login the Daytona hosts are skipped
with a note.

### Running more than one host

Each host keeps its own ledger, so nothing stops two hosts from claiming the same issue. Split
the repos instead: every repo is mapped on exactly one host, and an issue is picked up by the host
that maps its label or project. That holds only if each issue carries exactly one repo label or
repo project: an issue with two repos' labels matches on both hosts and is worked twice. It also
assumes every host uses the same Linear key, because ready issues are read from the key's
assigned issues; hosts on different keys see different issues.

Set `skip_unmapped = true` on each host. Without it, a host comments "No configured repo matches"
on every ready issue that belongs to the other host's repos. The installer asks "Do other hosts
handle the unticked repos?" when you untick discovered repos in the config form, and writes the key
if you say yes (`shared_team = true` in an answers file does the same). Doctor prints a
`skip_unmapped` line when it is on. The cost is that a typo in a label no longer gets that comment,
so an unmapped issue just sits in Todo.

The hosts share the owner's Claude login and so its 5h and weekly usage limits; each applies
`five_hour_max_pct` and `weekly_max_pct` to the same account, so size the caps with that in mind.
`flocc status -H all` shows all hosts together. Tailscale hostnames must differ between hosts.

## Auto-mode blocks

Host sessions run in auto mode. When its classifier blocks a command, the agent hears why and
works around it or gives up; nothing waits for you. After three blocks in a row Claude Code shows
a prompt, which denies itself after two minutes. Every block and prompt is logged per issue in
`~/.local/state/flocc/<KEY>/permissions.jsonl`.

`flocc -H <host> permissions report` counts them. Turn what you see into the host file's
`[permissions]` table, then `flocc ship -H <host>`; sessions pick it up at their next launch.

    [permissions]
    environment = [
      "Each session runs in a disposable VM. The worktree, ~/worktrees and /tmp are the agent's own.",
      "Nothing on this VM is production. The agent's GitHub token is read-only.",
    ]
    allow = ["Bash(npx playwright install)", "Bash(uv sync --frozen)"]

`environment` lines address whole classes of block and come first. `allow` rules are exact; a
rule that wildcards an interpreter or shell (`Bash(python:*)`, `Bash(sh *)`, anything with `-c`)
is refused when the config loads.

## Disk use

A worktree with its venv can reach 1.4 GB, and uv's download cache (`~/.cache/uv`) grows with every
repo `./setup`, which can fill a small disk (Daytona's is 10 GB) and break SSH and the installer.
Three things limit it:

- Each worktree's `./setup` runs with `UV_CACHE_DIR` pointing at `<worktree_root>/.uv-cache-<KEY>`
  and `UV_LINK_MODE=hardlink`, and that directory is deleted when setup ends, whether it succeeds,
  fails or times out. Every setup therefore downloads its packages fresh, and the venv keeps its own
  files. The cache is on the worktree's filesystem so uv can still link instead of copy.
- `min_free_disk_gb` (default 2.0) holds new starts while the disk holding `worktree_root` has less
  free space than that. `flocc doctor` warns below twice the value and fails below it, and
  a doctor FAIL also stops a fresh install from starting the timer.
- Each tick removes the worktree (not the branch) of a finished run whose Linear issue is
  completed or canceled, or no longer exists. Running and blocked claims, and issues in any other
  state, keep theirs, so a rerun from Todo still reuses it. A timed-out run with uncommitted
  changes, new files included, keeps its worktree. A removed worktree is recreated from the kept
  branch if the issue is rerun.

On a host with room to spare, `shared_uv_cache = true` replaces the throwaway caches with one
`<worktree_root>/.uv-cache` kept across setups, so only the first setup of a dependency set
downloads it. Nothing prunes that cache; `min_free_disk_gb` still holds new starts when it fills
the disk.

The cache change covers uv inside `./setup` only. Sessions still use `~/.cache/uv`, and the npm,
pnpm, pip and mise caches are not managed.

## Subagents in agent sessions

By default a session may launch only the agents in `plugin/agents/`, and only as often as its
phase allows; grants (below) can add `general-purpose` or a named workflow. The budgets (which
agents, the phase's total launches, how many running at once) are in
`src/flocc/gate.py`; the review phase gets none. A hook refuses any other launch and tells
the agent why. Every launch, allowed or refused, is appended to `<state_dir>/launches.jsonl`, and
`flocc status` shows each running claim's count as `launches N/limit`.

To allow more, put an `allow:` line in the issue's `agent` block, or in a comment of yours on the
issue (a `steer_authors` comment works too). Items are comma-separated:

```
allow: launches 14, agent code-mapper, workflow <name>, fix-rounds 2
```

A comment is applied once the supervisor has delivered it to the live session. A `launches` grant
in the agent block applies to the implementer phase; from a comment it applies to the phase
running when the comment arrives. `fix-rounds` raises the implementer's fix rounds.

The items `agent general-purpose` and `workflow <name>` change the session's launch flags, so
from a comment they take effect at the next handoff; the other items apply within one tick. Until
the handoff the gate refuses them with the rule `next-session`. A granted workflow starts once per
run and counts as one launch. `launches N` is the phase's total launches, workflows included.
Comment grants do not survive a release: a new claim discards them and rewrites only the agent
block's `allow:` line. To keep a grant across a release, put it in the agent block.

The implementer and fix sessions are the only ones connected to Remote Control. When one of them
needs an answer it asks with `AskUserQuestion`, which is meant to arrive in the Claude app with a
push. That is not yet confirmed on a host. A question left unanswered for two hours is escalated
by the service as tier 2 regardless. While a question is open, a reply on the issue is held and
delivered after the question is answered, and the context handoff request waits too.

Implementer and fix sessions still use `flocc escalate`, with a default, for reversible
choices. Tester and reviewer sessions are not on Remote Control and escalate with `--waiting` for
decisions that block them.

## Upgrading

`digest_issue` is now required. A config without it fails to load, which fails every tick and the
agents' pre-commit hook, so commits in running sessions are refused. Add it before upgrading.

A host must be on 0.3.0 or later before you ship 0.5.0 to it: 0.5.0 no longer reads the old names
or refuses a host that still has them. In particular, a branch whose tester commits carry only the
trailer from before the rename (the old product name, then `: <KEY> test`) is no longer
recognised, unless the host already recorded the tests. Finish or merge such issues on a 0.4.x
host before upgrading it; CHANGELOG.md has the trailer's exact text.

An agent token imported from a pre-0.5.0 host needs Actions (read) and Commit statuses (read)
added to its permissions; see The desktop command.

## Smoke test

Run this once by hand before trusting the timer with real issues. It uses your accounts, so no
automated run covers it.

### Plugin evals

Run the escalation evals in `plugin/evals/` (`escalate-irreversible`, `escalate-reversible`,
`escalate-shared-cause`) before the first real issue:

```bash
claude plugin eval plugin --trust-plugin --no-publish --ablation none -j 3 \
  --allow-tools "Bash(flocc escalate:*)"
```

If that invocation is rejected, `claude plugin eval --help` shows the current arguments. Pass means
every eval's graders succeed. The sandbox usually cannot run `flocc`, so the graders judge
the escalation the agent chose, and step 7 below exercises the real command. Fix the failing skill and rerun before going on.

### Steps

1. Create a scratch GitHub repo with a `justfile` whose `check` runs its tests, map it in the
   config, and create one issue in a test Linear team: labelled `claude` and the repo's label,
   assigned to you, in Todo, with an `## Acceptance criteria` list and an
   `` ```agent `` block containing `wall_clock: 30m`.
2. Run `flocc tick` by hand; it takes the same lock as the timer's ticks. Pass: the issue
   moves to In Progress, and `NF-…-test` appears in `tmux -L flocc ls` and in the Claude
   app. A tick run from a shell leaves behind any process a timed-out check daemonized; under
   the timer, or with `systemctl --user start flocc.service` (systemd mode only), systemd
   kills it when the tick ends.
3. Before anything else, check the tester session's tools. In the app, run `/mcp` in the first
   launched session and list its tools. Pass: no claude.ai connectors, Linear in particular,
   because `--strict-mcp-config` is unverified in interactive sessions. If any connector shows up,
   stop the rollout: `systemctl --user stop flocc.timer`, kill the session, and do not
   proceed, since it could write to Linear through the connector. Record the result in the spec's
   "Verified and unverified" section. Add a Linear MCP server to the `flocc` user's own
   Claude config only after this passes, and run this step again after each Claude Code upgrade.
4. In the app, confirm the tester session is steerable. Check `ListAgents` from a second session
   and note whether `--name` changed the listed name; record the answer in the spec.
5. Confirm lookup by human key resolves. Pass: the tick in step 2 found and claimed the issue by
   its `NF-…` key without an error in `journalctl --user -u flocc`, and
   `flocc release NF-…` (step 8) accepts the same key. The Linear query is
   `issue(id: "NF-1")`; a "not found" or schema error means the key lookup in `linear.py` is
   wrong, so fix it and add the real response as a fixture.
6. Keep running `flocc tick` until a PR exists and Linear has the PR comment. It is a
   draft unless the repo sets `draft = false`, which a private repo of a GitHub Free
   organization needs. Then keep ticking until `NF-…-review` has run (model from `review_model`,
   default `opus`; set it in the config to change it). Pass: the PR has a `🤖 flocc review`
   comment from the publish token's account, and the issue has a `Review: …` comment.
7. Exercise escalation: in a session, run

   ```bash
   flocc escalate --tier 1 --cause smoke --summary test --action ignore
   ```

   then tick. Pass: one ntfy push and the `escalation` label. Repeat the command and tick again.
   Pass: a second comment and no second push.
8. Confirm `issueAddLabel`, `User.isMe` and the `comments_since` filter worked. If any failed, fix
   `linear.py` and add the real response as a fixture.
9. Exercise a rerun: block a second issue (kill its session twice, one tick apart), then run
   `flocc release NF-…`. Pass: the issue is back in Todo, and the next tick restarts it on
   the kept branch. For a run that has finished (review done), timed out or blocked, dragging
   the issue back to Todo in Linear does the same: the next tick releases the old claim, comments
   once, and restarts. A running claim, review phase included, ignores the move; use `release`.
10. Check that one failed tick does not stop the timer (systemd mode). Empty the agent token file
    (`: > <agent_gh_token_file>`, after copying it aside), then run:

    ```bash
    systemctl --user start flocc.service
    journalctl --user -u flocc -n 20
    systemctl --user list-timers flocc.timer
    ```

    Pass: the journal shows the tick failing on the empty token, and `list-timers` still shows a
    NEXT time for the timer. Wait for that time and check the journal for a new tick. Restore the
    token file afterwards.
11. Recapture the status fixture. Copy a live session's `<state_root>/NF-…/status.json` over
    `tests/fixtures/status.json`, then run:

    ```bash
    uv run pytest tests/test_usage.py -q
    ```

    Pass: the tests pass on the real capture. If they fail, the real field paths differ from
    `rate_limits.five_hour/seven_day.used_percentage`, `resets_at` or
    `context_window.used_percentage`: fix `_CONTEXT_PATH` or `latest_usage` in `usage.py`, rerun,
    and commit the fixture with the fix.

When every step passes, remove the scratch issue and repo mapping, then label a real issue.
