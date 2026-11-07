# Project notes (state as of 2026-11-07)

Running notes for whoever picks this up next, including Claude after a context
reset. Design is in [PLAN.md](PLAN.md), setup in [INSTALL.md](INSTALL.md) and
usage in [README.md](README.md).

## What exists

- **csa-hub** runs on the **n8n** VM (10.10.1.86).
  - It listens on `127.0.0.1:7420`.
  - It's published through the existing Cloudflare tunnel `n8n_tunnel` as
    the route `agents.stoneyshome.com` → `http://localhost:7420`.
- **Claude connector:** `https://agents.stoneyshome.com/mcp`, named
  "Server Agent Hub".
  - Login is the admin password plus an authenticator code.
  - The user set the connector's tools to "always allow" in Claude. Any tool
    added later starts out as "ask" again.
- **Control panel:** `https://agents.stoneyshome.com/`. Logged-out visitors
  are sent to `/login`.
  - Pages: Overview, Approvals, Rules, Servers, Activity.
  - Logins last 24 hours and survive hub restarts.
  - The look follows the personal-finance-dashboard (`globals.css` on
    vm-fiance).
- **Releases:**
  - Every merge to `main` that passes CI publishes `v0.1.N`; md-only changes
    don't trigger one. The latest is **v0.1.15**.
  - Nothing updates automatically. The hub checks GitHub hourly and shows
    "UPDATE AVAILABLE".
  - Update with the `update_server` tool (after the user says so) or with
    `sudo csa-update` on the machine.
- **Repo workflow:**
  - Develop on branch `ccr-59230fbc-jw6beu`, then open a PR to `main` and
    merge it. The user has authorized merging.
  - Before new work, reset the branch to `origin/main`
    (`git checkout -B ccr-59230fbc-jw6beu origin/main`).

## Servers (all on agent v0.1.15, timezone America/New_York)

| Name | Role / notes | OS | Disk `/` |
|---|---|---|---|
| n8n | hub, tag `hub`; runs n8n + cloudflared | Ubuntu 24.04.5 | 97 GB, 43% used (extended today); an unused 500 GB `sdb` is to be removed in Proxmox |
| vm-fiance | personal-finance-dashboard + starhaul (11 containers, 2 Postgres) | **Ubuntu 26.04.1** (upgraded today) | 97 GB, 41% (extended today) |
| solar_perdiction | solar-calc (hostname `solar-calc`), 10.10.1.156 | Ubuntu 24.04.5 | 48 GB, 32%; ~49 GB unused in its LVM (can be extended) |
| AI-recipes | | Ubuntu 24.04.5 | 48 GB, 20%; ~49 GB unused in its LVM (can be extended) |
| jessy-desktop | desktop, tag `Desktop` | Ubuntu 24.04.5 | 98 GB, 22% (no LVM) |
| Ubuntu-Anthony | desktop, tag `Desktop` | Ubuntu 24.04.5 | 98 GB, 28% (no LVM) |

- All VMs run on Proxmox.
- **Ubuntu 26.04.1 LTS** is available for all the 24.04 machines. Only
  vm-fiance has been upgraded.
- All servers are fully updated (including kernel and firmware) and rebooted.

### vm-fiance upgrade record

1. A Proxmox snapshot was taken first.
2. Postgres dumps were saved in `/root/backups/`:
   `finance-20261107-1431.sql.gz` and `starhaul-20261107-1431.sql.gz`.
3. During the upgrade, "remove obsolete packages" (78) was answered **No**.
   They can be reviewed later with `apt autoremove --dry-run`.
4. The Docker and Cloudflare sources were rewritten as deb822 files
   (`/etc/apt/sources.list.d/docker.sources` with suite `resolute`, and
   `cloudflared.sources`). The old `.list` files are in
   `/root/backups/apt-sources/`.
5. Afterwards, `/` was extended to the full disk.
6. Its Proxmox snapshot can be deleted once the user is happy.

## Allow rules in place (hub "always allow")

- **Troubleshooting kit** (all servers):
  - `systemctl restart|start|reload|reset-failed …` and
    `systemctl daemon-reload`;
  - `docker restart|start …`, `docker compose restart …`,
    `docker system df …`, `docker image prune -f`;
  - `apt-get clean`, `journalctl --vacuum-time=14d`, `dpkg --configure -a`,
    `apt-get install -f -y`, `resolvectl flush-caches`,
    `timedatectl set-ntp true`.
- **App upgrades:**
  `apt-get upgrade -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold`
  (all servers).
- **Approved with "always" along the way:**
  - the DB backup script and one reboot command (vm-fiance);
  - `timedatectl set-timezone America/New_York`;
  - `apt-get full-upgrade …` on jessy-desktop and n8n;
  - the reboot command on jessy-desktop.

  The user may want to review or trim these on the Rules page.
- **Built into the agent, no rule needed:**
  - read-only commands;
  - `apt`/`apt-get` `update|upgrade` with any safe flags, but no package
    names, and hook, path and config options are blocked.

## How things work (gotchas learned)

- **Tool-call timeout:** MCP tool calls time out after about 60 s.
  - Approved actions run in the background; `check_approval` returns
    "still running" after about 40 s.
  - Keep `session_send` `wait_sec` at 45 or less.
  - To follow long jobs, prefer reading a log file with the `logs` tool over
    `session_send`, which returns all buffered output.
- **Interactive sessions:** use `session_start` / `session_send` /
  `session_close`. Run long installers inside `tmux` so they survive an agent
  restart.
- **Rebooting:** use `shutdown -r +1 "<msg>"`, which returns before the
  reboot. Reboot the hub (n8n) last.
- **Update timing:** right after a release, GitHub's "latest" link can lag a
  minute, and `csa-update` may then say "up to date". Wait and retry.
- **DNS:** the router (UniFi, 10.10.1.1) cached a negative DNS answer for
  `agents.stoneyshome.com` after the route was first added. There's no UniFi
  connector, and Home Assistant has no UniFi integration.
- **Installer:**
  - Ubuntu Desktop lacks curl. The join command printed by the hub installs
    it first.
  - The installer asks for a friendly name at the end. It rejects generic
    names ("Ubuntu", "localhost", …) and drains pasted line breaks first.
  - On Proxmox hosts, run the command without `sudo`.
- **Desktops** (jessy-desktop, Ubuntu-Anthony): rebooting closes whatever is
  open on them.

## To do / ideas

- [ ] **Persist pending approvals.** They're held in memory, so a hub restart
      loses them; this happened with the timezone approvals.
- [ ] **`timedatectl show …`** should be a built-in read. Today only bare
      `timedatectl status|show` is allowed, with no extra arguments.
- [ ] **Remove the unused 500 GB disk** from the n8n VM in Proxmox (user).
- [ ] **Optional disk extends:** AI-recipes and solar_perdiction, using
      `lvextend -r -l +100%FREE /dev/ubuntu-vg/ubuntu-lv`, live.
- [ ] **Optional agent on the Proxmox host** (`--name proxmox --tags proxmox`,
      no sudo), plus read-only rules for `qm list`, `qm config`, `pct list`,
      `pvesm status` and `pvesh get`.
- [ ] **Delete leftover approvals:** 6 stale "timedatectl show" requests on
      the approvals page.
- [ ] **Ubuntu 26.04 for the other servers,** one at a time, with a snapshot
      first, using the same procedure as vm-fiance.
- [ ] **Delete the vm-fiance pre-upgrade snapshot** in Proxmox once the user
      is satisfied.
- [ ] **Old containers:** two exited containers on vm-fiance
      (`elastic_hofstadter`, `priceless_hodgkin`, 7 weeks old) could be
      removed.
