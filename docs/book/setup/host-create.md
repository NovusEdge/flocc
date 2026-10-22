# Create a host as an Incus VM

`flocc host create` makes a flocc host as an Incus VM on a Linux machine you reach over SSH with your own account, such as a laptop on the tailnet. Agents run unreviewed code, so the host is a VM rather than a container.

```bash
flocc host create [--cpus 4] [--memory 8] [--disk 40] <machine> <name>
```

`<name>` becomes the Incus instance, the Tailscale name and the flocc host name. It is 2 to 63 lowercase letters, digits and `-`, starting with a letter. Memory and disk are in GiB, and disk is at least 10. The defaults are 4 CPUs, 8 GiB of memory and 40 GiB of disk. To size a new host like an existing one, read `incus config show <vm> --expanded`.

## Requirements

- On the machine, your account needs `incus-admin` membership or passwordless sudo for Incus.
- When firewalld or Docker runs there, create builds `flocc-setup`, stages it and runs the firewall and Docker steps as root in one `ssh -t ... sudo`. Sudo asks for your password once per run, and not at all with passwordless sudo. A password needs a terminal. A run stopped after sudo accepts the password leaves `/tmp/flocc-create.XXXXXX/setup.log` on the machine; one stopped at the password prompt leaves only the staged binary in `/tmp/flocc-create.XXXXXX`.
- The desktop must be on the tailnet.
- Run create inside the flocc checkout, since its ship step ships that commit. Create checks this before it touches the machine.

## What create does

Create checks each step before changing anything, so running the same command again continues a host that stopped half-made. In order, it:

1. Checks that Incus has the managed bridge `incusbr0`, a root pool with room for the disk, and the memory free. Otherwise it stops with the commands to run. It never sets Incus up itself. The commands carry `sudo` when your account reaches Incus through sudo rather than `incus-admin`.
2. Scopes firewalld, when it runs. See [What create changes on the machine](#what-create-changes-on-the-machine).
3. Lets the VM through Docker's forward drop, when Docker runs, or is enabled or socket-activated while stopped.

   Steps 2 and 3 run together in one root pass, shown as the row "Root pass" with a Firewalld row and a Docker row drawn from its report once sudo finishes. A machine with neither firewalld nor Docker skips the pass and its password.
4. Checks that no tailnet node already has `<name>`, launches `images:debian/13` as a VM with `boot.autostart=true`, and waits for an IPv4 address and outbound HTTPS.
5. Joins the VM to the tailnet with Tailscale SSH, tagged `tag:flocc-host`. See [Tailnet](../tailnet.md).
6. Waits until the desktop's tailnet lists the node. It runs `flocc host add <name> --ssh root@<name>` unless `hosts/<name>.toml` exists, then `flocc ship -H <name>` and `flocc login -H <name>`.
7. Takes the Incus snapshot `installed`, replacing an older one only after the new one exists. `incus snapshot restore <name> installed` returns the VM to it. Each successful run retakes it.

Create opens the first SSH connection to the machine in your terminal, so a Tailscale SSH check-mode re-auth URL shows there. The approval covers the rest of the run. The log is `~/.cache/flocc/create-<name>.log`.

The snapshot holds the host's tokens and Claude login as of that run. After a restore, run `flocc ship -H <name>` again if keys were rotated since. On a `dir` storage pool a VM snapshot is a full copy of the root image, which step 1's room check does not count.

If the VM has no HTTPS, check the firewalld and Docker rules below, then run create again.

### Joining the tailnet in step 5

With the Tailscale OAuth client stored, create mints a one-off key (pre-approved, tagged, valid 10 minutes) and runs `tailscale up` with it on stdin, with no terminal step. Without the client it asks for a pasted one-off key, or falls back to the QR code or link login.

It adds `--advertise-tags=tag:flocc-host` only when another node on the tailnet already carries the tag, which shows the policy defines it. Otherwise the VM joins untagged. A pasted key reaches the VM on stdin and is neither stored nor logged. If the policy does not define the tag, this step fails and names the Tailnet section of `flocc setup`.

A VM already logged in is renamed and given SSH instead of logging in again. The control server must confirm the new name within 15 seconds or create stops, naming the stale node to remove.

## What create changes on the machine

The machine may be shared with other work, so create adds rules scoped to `incusbr0` and never starts, stops or restarts Docker. A Docker restart can kill another session's image build.

| What | Where | Effect |
|---|---|---|
| zone `flocc-incus` | firewalld | `incusbr0` moves into it. It allows only `dhcp` and `dns` (Incus's dnsmasq), so the VM reaches none of the machine's own services. |
| policy `flocc-incus-out` | firewalld | Forwards `flocc-incus` to the zone of the default route's interface. |
| policy `flocc-no-docker` | firewalld, when Docker made its `docker` zone | Rejects `flocc-incus` to `docker`, ahead of Docker's `docker-forwarding` policy, which accepts forwarding from every zone into Docker's bridges and published ports. |
| `flocc-docker-user.service` | `/etc/systemd/system/` | A oneshot with `After=` and `PartOf=docker.service` that adds three rules to `DOCKER-USER` after every Docker start (ip6tables too, where Docker made that chain). See below. |

The three `DOCKER-USER` rules, in order, are `-i incusbr0 -j DOCKER-FORWARD`, `-i incusbr0 -j ACCEPT` and `-o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT`. The jump hands the VM's traffic to Docker's own forwarding chain first, so the VM reaches published container ports on every Docker network and no others. The rest returns to the `ACCEPT`.

- `DOCKER-FORWARD` exists from Docker 28.0 on, and create stops on an older Docker.
- The unit has no `ExecStop`, so the rules stay when Docker stops. A stopped Docker leaves `FORWARD` at `DROP`, and removing the rules would cut the VM off.
- With Docker stopped, the unit is enabled but not started, and runs at Docker's next start.

### firewalld reloads

A new zone or policy needs one `firewall-cmd --reload`, which create runs whenever the runtime or the permanent configuration differs from the wanted one. A reload drops every firewalld change that was never made permanent, whoever made it. On a machine other people use, compare `firewall-cmd --list-all-zones` with the same command under `--permanent` first, and look at any runtime-only entry other than Docker's bridge bindings.

`flocc-no-docker` covers only zone `docker`, so while Docker runs create also checks that `docker0` and each `br-<12 hex digits>` bridge are in it. A reload unbinds them until Docker binds them again, and create waits up to 10 seconds for that after its own reload. If a bridge is still in another zone, create stops naming the bridge and its zone, and leaves Docker alone. Bind it with `sudo firewall-cmd --zone=docker --change-interface=<bridge>`, which lasts until the next reload, or restart Docker when nothing depends on it. Then run create again.

### Backends that create refuses

With firewalld's iptables backend, Docker's `DOCKER-USER` chain accepts the VM's traffic before `flocc-no-docker` sees it, so create stops before it changes anything. Set `FirewallBackend=nftables` in `/etc/firewalld/firewalld.conf`, restart firewalld, then run create again. Docker with its nftables firewall backend has no `DOCKER-USER` chain, so create stops there too.

Without firewalld, nothing keeps the VM off the machine's services, and create says so.

### Limits to know

firewalld forwards by zone, so the VM can also reach any other interface in the uplink's zone. That includes `tailscale0` when it is in that zone: traffic forwarded out through it is masqueraded as the machine, and create's Firewalld line says when this holds. If the default route moves to an interface in another zone, such as Wi-Fi to Ethernet, run create again to move the policy.

The new zone has no intra-zone forwarding, so with `br_netfilter` loaded (Docker loads it) two VMs on `incusbr0` cannot reach each other.

A host VM has no swap, so a test suite that grows past the VM's memory makes the guest thrash instead of the OOM killer stepping in. The VM can stop answering `incus exec` and Tailscale for a long time. Installing `earlyoom` in the guest has it kill the largest process first.

## Remove create's changes

This takes it all off a machine. The VMs on `incusbr0` then have no way out until the bridge gets another zone.

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
