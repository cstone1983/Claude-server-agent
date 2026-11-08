# Project notes (state as of 2026-11-08)

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

**App inventory (what runs where):** `/srv/homelab/INVENTORY.md` on server
`n8n`. Read it with `read_file`. It isn't kept in this public repo on purpose.

## Servers (all on agent v0.1.18, timezone America/New_York)

| Name | Role / notes | OS | Disk `/` |
|---|---|---|---|
| n8n | hub, tag `hub`; runs n8n + cloudflared | Ubuntu 24.04.5 | 97 GB, 43% used (extended today); an unused 500 GB `sdb` is to be removed in Proxmox |
| vm-fiance | personal-finance-dashboard + starhaul (11 containers, 2 Postgres) | **Ubuntu 26.04.1** (upgraded today) | 97 GB, 41% (extended today) |
| solar_perdiction | solar-calc (hostname `solar-calc`), 10.10.1.156, 64 GB RAM | Ubuntu 24.04.5 | 97 GB, 16% (extended today) |
| AI-recipes | | Ubuntu 24.04.5 | 97 GB, 10% (extended today) |
| cpu-share | VM 104, 10.10.1.134; new and empty, 24 vCPU / 64 GB; to become an **Akash compute provider** | **Ubuntu 26.04.1** | 498 GB |
| Proxmox | node `starwars`, 10.10.1.15, tag `proxmox`, careful-mode policy | Proxmox VE **8.4.21**, kernel 6.8.12-43 | see below |
| jessy-desktop | desktop, tag `Desktop` | Ubuntu 24.04.5 | 98 GB, 22% (no LVM) |
| Ubuntu-Anthony | desktop, tag `Desktop` | Ubuntu 24.04.5 | 98 GB, 28% (no LVM) |

- All VMs run on Proxmox.
- **Ubuntu 26.04.1 LTS** is available for all the 24.04 machines. Only
  vm-fiance has been upgraded.
- All servers are fully updated (including kernel and firmware) and rebooted.
  AI-recipes and solar_perdiction moved to kernel 6.8.0-149 on 2026-11-07,
  and autoremove ran on AI-recipes.

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

## Proxmox host (updated 2026-11-08)

- **Updates:** switched from the enterprise repo to the free no-subscription
  repo (old files in `/root/*.bak-20261107`), then updated 8.3.0 → 8.4.21
  and rebooted with pve-safe-reboot.
  - **Upgraded to Proxmox VE 9.2.26 (Debian trixie) on 2026-11-08**; see
    "Proxmox 9 upgrade" below.
- **Boot:** GRUB via `EFI\proxmox\shimx64.efi` (Boot0008); the fallback
  `EFI/BOOT` is now kept in sync. Secure Boot is off. A leftover "Windows
  Boot Manager" entry is second in the boot order.
- **Disks:** 8 SAS 10k drives on a PERC H730.
  - **RAID 1** (2 × 900 GB): Proxmox, plus an empty 702 GB LVM-thin `data`.
  - **RAID 5** (6 drives): the ZFS pool `VM`.
  - Drives 4 (Seagate S401HE8E) and 5 (Hitachi KPW6NXAF) each have 1 grown
    defect: watch them.
- **VMs:** 100 Solar-Calc, 101 Ubuntu-Anthony, 102 n8n, 103 haos16.0, 104
  CPU-Share, 106 Jessy, 107 Recipies, 110 Autopilot-Climate (stopped), 111
  Fiance. All are onboot except 110.
  - Memory is overcommitted: 245 GB assigned on a 188 GB host.
- **Backups (working since 2026-11-07):**
  - **Local:** storage `backups` is the 700 GB LV `pve/backups` at
    `/mnt/pve-backups` (RAID 1, separate from the VM pool). Job
    `backup-79526ae3-5f24` runs nightly at 23:30, snapshot mode, zstd, keep
    last 1, for VMs 100, 102, 103, 107, 110 and 111. The first full set was
    79 GB and took 32 min.
  - **Off-site:** `/usr/local/sbin/pve-offsite-backup` (source in
    `scripts/`) runs from `pve-offsite-backup.timer` at 23:35 and waits for
    vzdump.
    - It uploads with rclone remote `gcrypt:`, a crypt over `drive:`. The
      Google Drive scope is `drive.file`; `root_folder_id` is the user's
      folder "backup/proxmox vm"; file and folder names stay readable.
    - Layout: `<vmid>-<name>/daily|weekly|bimonthly`, keeping 1 each.
      Weekly refreshes on Sunday; 2-monthly on the 1st of odd months.
      Copies between slots are server-side.
    - `_host-config/` keeps the last 7 tarballs of `/etc/pve`, network and
      fstab.
    - Bandwidth limit: off at night, 1 MB/s from 04:30 to 23:00. The
      connection is 40 Mbps up, so 79 GB takes about 4.5 h.
    - Log: `/var/log/pve-offsite-backup.log`. Status (OK/FAILED):
      `/var/lib/pve-offsite-backup.status`.
    - rclone needs `RCLONE_CONFIG=/root/.config/rclone/rclone.conf` (the
      agent runs with a minimal env). Never run `rclone config update` on
      `drive`, because it triggers a browser re-auth.
    - **Keys:** the crypt passwords are only in the user's password manager
      and in `rclone.conf` on the host. If they're lost, the off-site copies
      are unrecoverable.
  - **To do:**
    - a test restore from Drive;
    - qemu-guest-agent in the Ubuntu VMs (only HA has it);
    - failure alerts (the job's mail goes through legacy sendmail and
      probably never arrives).
- **History:** the old job (VMs 101, 102, 103 to `local` at 21:00) had
  failed every night because the disk was full; the disk was cleared on
  2026-11-07.
  - Not backed up, by the user's choice: 101, 104 and 106.
- **Cleanup done:** n8n's unused 500 GB disk was deleted. About 300 GB of
  year-old snapshots are still there; delete them only if the user agrees.
- **CPU-Share firewall (Akash isolation), done 2026-11-08.** Rules in
  `/etc/pve/firewall/104.fw`, VM 104 `net0` only.
  - Outbound: internet only; LAN/private ranges (10/8 incl. VLANs, ipfilter
    ipset, IPv6, multicast) dropped. DHCP (udp 67) to the UDM must stay.
  - Inbound: Akash ports, 80/443 and the NodePort range.
  - Datacenter firewall is on; the host firewall is off
    (`/etc/pve/nodes/starwars/host.fw` `enable: 0`) so 8006/22 are unaffected.
  - Verified: cpu-share reaches 1.1.1.1 but not 10.10.1.15.
  - VM 104 CPU type is x86-64-v3. Memory trimmed: VM 100 to 8 GB, VM 102
    to 16 GB.
- **BBR** congestion control on the host (`/etc/sysctl.d/90-bbr.conf`,
  `/etc/modules-load.d/bbr.conf`); parallel per-VM uploads raised off-site
  speed from about 10 to about 34 Mbps.

## Proxmox 9 upgrade (2026-11-08)

- Now **pve-manager 9.2.26** on trixie. Apt sources are deb822
  `proxmox.sources` / `ceph.sources` (squid, no-subscription); the PVE 8
  sources are in `/root/apt-sources-pve8/`, a config tarball in
  `/root/pve8-pre-upgrade-20261108-0808.tar.gz`.
- **Kernel 7.0.14-28 resets the R630** right after "Loading initial
  ramdisk" (UEFI0078 machine-check messages, boot loop). Booting
  **6.8.12-43-pve** works, and it is pinned in
  `/etc/default/grub.d/proxmox-kernel-pin.cfg`. Retry 7.0 once after the
  BIOS update, watching the iDRAC console; GRUB can still pick 6.8.
- **NIC rename:** `pve-network-interface-pinning` renamed eno3 to **nic0**
  (`/usr/local/lib/systemd/network/50-pve-nic0.link`, MAC
  24:6e:96:8a:39:9c); `vmbr0` uses nic0.
- **Lessons:** run long apt/dpkg work detached with `systemd-run` (an agent
  timeout killed the microcode install and left dpkg half-configured);
  chrony needed a restart after the reboot.
- **Hardware:** R630, Service Tag 9P7V8N2, iDRAC at 10.10.1.10.
  Updated 2026-11-08: iDRAC 2.63.60.61 → **2.86.86.86** (final), BIOS
  2.12.1 → **2.19.0** (latest). Clean boot afterwards, no machine-check
  errors.
  - The 2.86 iDRAC `.EXE` failed with **RED007** (signature) on the old
    iDRAC: extract it (`.\file.EXE /s /e=C:\dir`) and upload
    `payload\firmimg.d7`. After that the BIOS `.EXE` installed normally
    ("Install Next Reboot", then pve-safe-reboot).
  - HTML5 console "Certificate may not be verified": open
    https://10.10.1.10:5900 once and accept the certificate.
  - After a host reboot the Proxmox agent can take about 2 min to rejoin
    (it retries while n8n and the tunnel come back). The user suspects an old failing DIMM: check the iDRAC
  System Event Log. All 8 disks Online.

## UniFi UDM Pro SE (router, 10.10.1.1)

- A dedicated local admin; credentials in `/root/.config/unifi/.env` on n8n.
  The `unifi` helper (`scripts/unifi`, installed `/usr/local/bin/unifi` on
  n8n) calls the local API: `net:` = classic API, `v2:` = newer API.
  Secret fields are redacted in output; it refuses to send `<redacted>`.
- Done 2026-11-08: DHCP reservation for CPU-Share, the Akash port forwards
  (enabled), three Nextcloud forwards removed, Cloudflare DNS for Akash set
  by the user.
- An early GET printed VPN private keys before redaction existed: the user
  should regenerate those VPN keys.
- Open: decide on the `rdc` port forward; a duplicate 10.10.3.100
  reservation; a dead AdGuard entry.

## Proxmox host: careful mode (the user's rule: "verify, don't assume")

**What the user means:** anything that can be verified before running a
command must be verified and double-checked. This holds on every server,
strictest on Proxmox. The same rule is in the connector's built-in
instructions (`internal/hub/server.go`), so every Claude chat gets it.

**The installer detects Proxmox and writes a local policy:**
- read-only commands run freely;
- backup work runs freely (`vzdump`, `pvesh create|set /cluster/backup…`);
- every other change asks, and "always allow" for this server whitelists it;
- `apt upgrade` is not automatic;
- nothing is hard-blocked.

**Power on the Proxmox host:**
- **Reboot:** needs the user's explicit, separate OK every time (`ask_always`).
  It can't be bulk-approved or always-allowed, and gets its own orange
  approve button on the approvals page.
- **Shutdown or poweroff:** never; the user does that by hand. Denied in
  policy.
- **Scripts that contain a reboot:** refused.
- **How to reboot:** always with **`pve-safe-reboot`** in
  `/usr/local/sbin`.
  - It shuts each running VM down cleanly, one at a time, with up to 180 s
    each. The VMs in `LAST_VMIDS` (in `/etc/pve-safe-reboot.conf`; n8n = 102)
    go last.
  - Then it reboots the host.
  - If a VM won't stop, it cancels, the host doesn't reboot, and n8n is
    still up.
  - It runs detached; the log is `/var/log/pve-safe-reboot.log`, and the
    list of VMs that were running is in
    `/var/lib/pve-safe-reboot.last-running`.
  - `pve-safe-reboot --dry-run` is a free read and shows the order.
  - Before running it, check that no VM is mid-upgrade or mid-backup.
  - Afterwards, start any VM that was running but lacks "Start at boot".
- **Refresh an older host's policy:** run `install.sh proxmox-policy --last-vms 102` as root.
  It backs up the old file and reloads the agent.

**The hub never applies "all servers" rules to servers tagged `proxmox`.**
Only rules scoped to them by name or tag apply.

**Routine for any change on Proxmox:**
1. **Look first.** Read the current state with `qm list` / `qm config <id>`,
   `pvesh get …` and `pvesm status`. Confirm the VMID matches the VM *name*
   the user means; never act on a VMID from memory.
2. **Say exactly what will happen:** the exact command, which VMs and storage
   it touches, the expected downtime and how to undo it. Flag anything
   irreversible.
3. **Make it reversible where possible.** Take a snapshot or backup before
   risky VM changes; have a fresh backup before deleting old ones.
4. **One change at a time,** then verify with the same read commands and
   report the result.
5. **Stop and ask** if anything looks different from what was expected.
   Never improvise a workaround.

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
- [ ] **Backup failure alerts to n8n** (the user asked for this later). The
      idea: an n8n workflow with a Webhook trigger, IF failure, then notify
      the user (HA push, Gmail or similar; ask which), optionally logging
      to a Data Table, plus a 07:00 "dead man's switch" if no report
      arrived. Senders:
      - a Proxmox notification webhook target (Datacenter → Notifications);
        switch the backup job from legacy-sendmail to notification-system.
        This also covers ZFS and disk events.
      - `pve-offsite-backup` POSTs OK/FAILED after each run.

      Use the LAN URL; open nothing new to the internet.
- [x] **Proxmox 9 upgrade** done 2026-11-08 (see its section). Prep done 2026-11-07 (`systemd-boot` removed,
      `intel-microcode` installed, `pve8to9 --full` shows 0 failures).
      Next:
      1. Confirm the first off-site upload (status file).
      2. Switch the sources to trixie and the PVE 9 repos (ceph to
         squid/no-subscription), backing up the sources first.
      3. Run `apt dist-upgrade` in a tmux session, answering prompts and
         keeping existing configs.
      4. Run `pve8to9` again.
      5. Reboot with `pve-safe-reboot` (with a separate OK).
      6. Verify the VMs, hub, backup job and rclone.
- [ ] **Proxmox Backup Server (PBS)** (after the Proxmox 9 upgrade; the user
      agreed on 2026-11-07). Today every night uploads full vzdump archives
      (about 79 GB, about 5 h at about 34 Mbps). PBS deduplicates and makes
      incremental backups, so nightly off-site uploads would be the changed
      chunks only (a few GB, minutes).
      - Plan: a PBS VM or install, the datastore on `/mnt/pve-backups` (or
        similar), and the nightly job pointed at PBS.
      - Off-site: either `rclone sync` of the datastore with crypt, or a PBS
        remote/sync target.
      - Keep the current vzdump + rclone setup until PBS is proven,
        including a test restore.
- [x] **Removed the unused 500 GB disk** from the n8n VM (2026-11-07).
- [x] **Agent on the Proxmox host** (node `starwars`), installed 2026-11-07 with careful mode. Old notes: blocked until the user
      resets the forgotten root password (GRUB `init=/bin/bash`, then
      `passwd root`; this reboots all VMs, so snapshot first). The web shell
      user `stonec` isn't root and Proxmox has no sudo. Then:
      1. Install without `sudo`, using `--tags proxmox` and the name `proxmox`.
      2. Propose read-only rules: `qm list`, `qm config`, `pct list`,
         `pvesm status`, `pvesh get`.
      3. Add local deny rules in `/etc/csa/policy.yaml`: `qm destroy`,
         `pct destroy`, `pvecm`.
- [x] **Proxmox backups are broken.** Fixed 2026-11-07 (local job + off-site).
      - Its `local` storage was 100% full of backups from Oct 2025; the
        backup job most likely stopped once the disk filled.
      - On 2026-11-07 the user deleted every backup, leaving 77 GB free, so
        **no VM has a backup now.**
      - The user needs a backup job that saves to non-`local` storage, covers
        all VMs and uses "keep last 3".
      - VMs: 100 Solar-Calc, 101 Ubuntu-Anthony, 102 n8n, 103 haos16.0 (Home
        Assistant), 104 CPU-Share, 106 Jessy, 107 Recipies, 110
        Autopilot-Climate, 111 Fiance.
- [x] **Off-site copies of Proxmox backups on Google Drive** (done
      2026-11-07; see the Proxmox section).
- [ ] *(old plan, kept for reference)* **Off-site copies of Proxmox backups on Google Drive.** Do this after
      the local backup job works.
      - The user has a 5 TB Google plan with about 4 TB free.
      - Use `rclone` on the Proxmox host with an encrypted remote (`crypt`)
        over Google Drive. The user keeps the encryption password in their
        password manager.
      - The user does the Google sign-in (`rclone config` / `rclone authorize`)
        themselves; the token stays on the host and never goes through chat.
      - After each nightly vzdump, a systemd timer or vzdump hook uploads the
        newest backup of each VM and keeps the last 2 per VM on Drive.
      - Expect 60–90 GB per full set; the first upload will be slow.
      - Have Claude check that uploads succeed and warn when one fails.
- [x] **The join command always added `sudo`.** Fixed: it now uses
      `sudo` only when present.
- [x] **Installer variable clash.** Sourcing `/etc/os-release` overwrote
      `NAME` and `VERSION`. That was the real reason servers enrolled as
      "Ubuntu", and why release downloads silently fell back to building
      from source. Fixed by reading it in a subshell.
- [x] **BIOS/iDRAC update** done 2026-11-08.
- [ ] **Retry kernel 7.0** once (watching the iDRAC console) now that the BIOS is 2.19.0.
- [ ] **Update all agents to v0.1.24** (when the user says).
- [ ] **Old SAN: Dell EqualLogic PS6000** (raised 2026-11-08, for another day). Its
      Group Manager web UI is a Java applet, which modern browsers can't run.
      Options: the CLI over SSH/telnet or the serial port (full management);
      the standalone Group Manager Java app; or an old-Java VM. Possible use:
      iSCSI storage for Proxmox (PBS datastore and bulk/low-IO VM disks; about 16 TB raw, so about 11-13 TB usable in RAID 6/50; 1 GbE ports, use multipath). Need: its
      group/management IP, firmware version and whether the admin password
      (default grpadmin) is known.
- [ ] **Find the failing DIMM** from the iDRAC SEL and replace it.
- [ ] **Regenerate the UDM VPN keys** (exposed once in output).
- [ ] **Delete leftover approvals:** 6 stale "timedatectl show" requests on
      the approvals page.
- [ ] **Ubuntu 26.04 for the other servers,** one at a time, with a snapshot
      first, using the same procedure as vm-fiance.
- [ ] **Delete the vm-fiance pre-upgrade snapshot** in Proxmox once the user
      is satisfied.
- [x] **vm-fiance logrotate failure:** fixed 2026-11-07. A duplicate
      `/etc/logrotate.d/cloud-init` left by the 26.04 upgrade was moved to
      `/root/backups/`.
- [ ] **AI Recipes runs in Vite dev mode** under pm2. Consider a
      production build.
- [ ] **Old containers:** two exited containers on vm-fiance
      (`elastic_hofstadter`, `priceless_hodgkin`, 7 weeks old) could be
      removed.
