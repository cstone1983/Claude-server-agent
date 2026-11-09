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
      Google Drive scope is `drive` (full; was `drive.file` until 2026-11-09,
      which only sees files the same OAuth client created); `root_folder_id`
      is the user's folder "backup/proxmox vm"; file and folder names stay
      readable.
    - **OAuth client (2026-11-09):** `drive:` uses the user's own Google Cloud
      OAuth client (project "cstone1983", Google Auth Platform app "rclone",
      Desktop client). Before that it used rclone's shared default client,
      whose per-minute quota caused ~1,900 rateLimitExceeded/list failures on
      the first PBS sync. The client id/secret/token live only in
      `rclone.conf`; the old config is in `rclone.conf.bak-202611091704`.
      Re-auth if ever needed: `rclone authorize drive <id> <secret>
      --auth-no-open-browser` on the host, the user signs in on a PC and puts
      the failing `http://127.0.0.1:53682/?state=...` URL into a root-only file,
      which is curl'ed on the host; then `rclone config update drive ... token`.
    - **Published (2026-11-09):** the app is "In production" (External,
      unverified: the sign-in shows Google's "unverified app" warning, which is
      fine for the owner's own account), so the refresh token does not
      expire. Publishing required a home page + privacy policy on a verified
      domain: stoneyshome.com is verified in Google Search Console (TXT record
      added in Cloudflare by Search Console; do not remove it), and the pages
      are served by the n8n workflow "rclone App Pages (Google OAuth)":
      https://n8n.stoneyshome.com/webhook/rclone and /webhook/privacy (keep
      that workflow active). Re-signed after publishing at 17:18.
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
    (it retries while n8n and the tunnel come back). The SEL shows no memory faults (the old RAM issue is
  probably on another server). All 8 disks Online.

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

## Anthony's desktop (VM 101, Ubuntu-Anthony)

- 14.3 GB RAM, 16 vCPUs. Runs the Minecraft server (`minecraft.service`,
  /opt/minecraft, user minecraft, port 25565, forwarded on the UDM to
  10.10.1.185). 2026-11-09: Java heap lowered from `-Xmx16384M -Xms16300M`
  (more than the VM has, it squeezed the desktop) to `-Xmx8G -Xms8G`; unit
  backup /etc/systemd/system/minecraft.service.bak-*.

## Network overhaul and internal names (2026-11-09)

- Plan and runbook: `docs/network-overhaul.md` (zones, IP plan, per-server
  swap steps, hub-continuity rules). Window: Wed 11 / Thu 12 Nov evening.
- Firewall: zone-based since 2026-11-09 (user migrated). Networks: Main
  (untagged, 10.10.1.0/24), Guest 2, IoT 3, Cameras 4, Servers 10
  (10.10.10.0/23: 10.10.10.x infrastructure, 10.10.11.<VMID> VMs), DMZ 20
  (10.10.20.0/24), Storage 40 (VLAN only, SAN iSCSI later). "test" (VLAN 5)
  deleted.
- Local DNS on the UDM: `<name>.localdomain` for n8n, homeassistant, solar,
  recipes, finance, cpushare, proxmox, idrac, fileserver. The n8n credentials
  (HA, the SSH ones) use these names, so a server move = one DNS edit.
- Helpers: `net-check` on n8n (read-only connectivity test, run before/after
  every change); `vm-net-move` and `pve-net-apply` on Proxmox (self-reverting
  VLAN move / host network change, run detached with systemd-run).
- **Internal HTTPS proxy = the Traefik on the n8n VM** (decided 2026-11-09,
  not built yet; plan in `docs/internal-https.md`). It already serves
  n8n.stoneyshome.com (compose in /home/stonec/n8n-compose). It will also hold
  the wildcard `*.lan.stoneyshome.com` (Let's Encrypt DNS-01 via a scoped
  Cloudflare token) and route the internal names to each service, so every
  `<name>.lan.stoneyshome.com` resolves (UDM split DNS) to n8n's IP. If n8n is
  down, internal HTTPS names are down too; every service is still reachable by
  its IP. Proxmox gets its own certificate through its built-in ACME client.

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
- [x] **Backup failure alerts (done 2026-11-08/09):** n8n workflow "Backup Alerts (n8n)"
      (id Uv9JSW26gTbNid2Z), webhook /webhook/backup-report guarded by header
      X-Backup-Token (token in /etc/backup-report.conf on Proxmox, root-only).
      Telegram (credential "Telegram account", chat 6897458189) on error/warning;
      07:30 dead-man check for proxmox-vzdump, fileserver-files, pbs-offsite
      (state in workflow static data). Senders: PVE + PBS notification webhook
      target `n8n-backup` with matcher `n8n-backup-all` (names must differ),
      body {"source","severity","title","message"}; scripts call
      /usr/local/sbin/backup-report <source> <ok|warning|error> <msg>.
- [x] **File server backup:** VM 112 is in the 21:30 PBS job with its system disk only
      (scsi1 backup=0). Data: /usr/local/sbin/pve-fileserver-backup (timer 22:15)
      freezes the guest via the agent, LVM-snapshots eql-fileshare/vm-112-disk-0
      (18G COW, VG has 20G free), mounts it read-only and runs
      proxmox-backup-client (host/fileserver-share, metadata change detection)
      with the pbs-main token. VMs 101 and 106 were added to the 21:30 job too.
- [~] **PBS off-site:** /usr/local/sbin/pbs-offsite-sync (timer 23:00) rclone-syncs
      /mnt/pve-backups/pbs to gcrypt:_pbs-datastore. PBS prune moved to 12:30,
      GC to 13:00 (never during the sync). **Retire the old 23:30 vzdump job +
      pve-offsite-backup timer after the first sync is verified.**
      2026-11-09: first run (started 23:00 on the 8th) was throttled by Google
      (shared rclone client); stopped 17:04, switched to the user's own OAuth
      client, restarted 17:08 with no rate-limit errors.
      Daytime limit 10 Mbps (1220k, 04:30-23:00; was 1 MB/s), full speed at night. Upload line is 40 Mbps, usually ~35.
- [x] **Cleanup 2026-11-08:** all 7 old snapshots deleted; nextcloud-data.old removed;
      UDM: dead AdGuard reservation (10.10.1.179) removed, stale 10.10.3.100 on
      the old HA Pi cleared. The vm-fiance exited containers are leftovers from
      two failed `npm run build` image builds (no data); kept at the user's wish.
      The user deletes test VM 9103 in the UI.
- [ ] **unifi helper:** reuse the login session (the UDM rate-limits logins).
- [ ] *(old)* **Backup failure alerts to n8n** (the user asked for this later). The
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
- [~] **Proxmox Backup Server (PBS): installed on the host 2026-11-08.**
      - PBS 4.2.9 (repo `pbs.sources`, pbs-no-subscription; the re-added
        `pve-enterprise.sources` is disabled, backup in /root). Web UI :8007.
      - Datastore `main` at `/mnt/pve-backups/pbs`. Prune job
        `main-retention` 02:30 (keep 1 daily, 1 weekly, 2 monthly, the user's
        choice), GC 03:00, verify job `main-verify` Sun 05:00.
      - User `pve@pbs` + token `pve@pbs!pve`, role DatastoreBackup on
        /datastore/main for **both** user and token (token rights are capped
        by the user's). Proxmox storage `pbs-main` (127.0.0.1, fingerprint,
        keep-all, so PBS does the pruning). Secret only in
        /etc/pve/priv/storage/pbs-main.pw.
      - Test backup VM 103: 32 GB in 6m39s (~84 MB/s), OK.
      - Nightly job 21:30 → pbs-main, VMs 100,102,103,107,110,111. The old
        23:30 vzdump job + Drive upload stay until a test restore is done.
      - Test restore OK 2026-11-08: VM 103's backup restored to VM 9103
        `haos-restore-test` (new MAC, onboot 0, USB stick passthrough removed,
        never started). The user deletes 9103 in the UI (qm destroy is denied
        to the agent). Lesson: a restored copy keeps onboot and USB
        passthrough; strip them before it can ever start.
      - Still to do: an off-site copy of the datastore to Drive (rclone of
        /mnt/pve-backups/pbs, only new chunks nightly); then retire the old
        23:30 vzdump job and its upload.
      Original plan: Today every night uploads full vzdump archives
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
- [x] **Retried kernel 7.0 after the BIOS update (2026-11-08): still resets.**
      The iDRAC SEL shows machine-check errors on **both CPU 1 and CPU 2** at
      every 7.0 boot (13:37, 13:40, 13:43, 13:53 and 15:24 UTC), plus BERT
      records; there are no DIMM/memory events. Under 6.8 the EDAC counters
      show 0 errors; microcode 0x49 is final for E5-2680 v3. Conclusion:
      kernel 7.0 is incompatible with this Haswell-EP R630, not a hardware
      fault. Stay on 6.8; next try 6.17.13 (a one-time GRUB boot), then pin
      it if it works.
- [ ] **Move the pinned kernel 6.8 to 6.17** (6.8 is the PVE 8 kernel).
- [ ] **Power supply 2:** the SEL shows input-power losses in Mar/Apr 2025. Check that both PSUs are plugged into separate live circuits and that redundancy shows OK.
- [x] **Hands-off mode** (v0.1.25, PR #35, 2026-11-08): the approvals page can
      auto-approve actions for 30 min-12 h on all servers or one server.
      Proxmox is excluded unless ticked; it never covers Proxmox reboots
      (ask_always), rule requests or policy denials. Marked "auto" in recent
      decisions and audited (`approval_auto_granted`). State is in hub state
      `auto_approve`. All agents + hub on v0.1.25.
- [x] **Agents updated to v0.1.24** (2026-11-08). Approvals-page login now lasts
      7 days (`dashboard_session_hours: 168` in /etc/csa-hub/hub.yaml).
- [ ] **File server, VM 112 `fileserver`** (created 2026-11-08): Ubuntu 24.04.2 from
      ISO, 4 cores / 8 GB, 32 GB system disk on `VM`, **5100 GB data disk on
      `eql-fileshare`** (LVM on the EqualLogic iSCSI volume `fileshare`, 5 TB
      thin, snap-reserve 0, ACL = Proxmox IQN only; Proxmox storages
      `eql-fileshare-iscsi` + `eql-fileshare`).
      Plan: data at /srv/share (users/<name> private, public/ shared);
      **Samba + Nextcloud on the same files** (Nextcloud external storage
      "Local" with $user). A tiny custom Nextcloud app listens for
      UserCreated / PasswordUpdated / UserDeleted events and calls one
      sudo-restricted helper that creates or updates the Samba user (same
      password) and the folder with permissions; it only manages members of
      the "fileshare users" group; deletion only disables Samba (the folder
      is kept). Samba-side password changes are disabled. Outside access
      via Cloudflare Tunnel.
      **Built 2026-11-08:** 10.10.1.80 (DHCP, no reservation yet), Ubuntu 24.04,
      data = XFS on sdb at /srv/share (fstab UUID). Samba shares "My Files"
      (/srv/share/users/%U) + "Public" (@fsusers, setgid + default ACLs).
      Nextcloud 35.0.2 at http://10.10.1.80 (Apache/PHP 8.3, MariaDB, Redis,
      cron), data dir /var/lib/nextcloud-data; external storage "My Files"
      ($user) + "Public" for Nextcloud group `fileshare`; www-data is in
      fsusers. Sync: /usr/local/sbin/fs-user (sudo for www-data only) + app
      apps/fssync. End-to-end tested OK (create, password change, disable,
      enable, delete; Samba <-> Nextcloud files both ways). Note: occ
      --password-from-env needs `sudo -u www-data env OC_PASS=...` (sudo
      strips env). Name: **\\fileserver**. UDM DHCP reservation 10.10.1.80 (client
      named fileserver) + UDM local DNS A record `fileserver` (resolves as
      fileserver.localdomain); Samba `netbios name = FILESERVER`, nmbd on,
      bound to lo + 10.10.1.0/24; wsdd via a custom unit
      /etc/systemd/system/wsdd.service (the Ubuntu package ships none) on
      enp6s18. Samba/SMB stays LAN-only (no router forwards; the user wants it
      that way). The UDM rate-limits logins: space out `unifi` calls.
      Exception: `EXISTING_OK="stonec"` in fs-user lets the existing system
      account stonec (the user's Linux admin) be a file-share user (Samba has
      its own passwords, unix password sync = no, so the Linux login is
      unaffected); stonec was set up once by hand + added to Nextcloud group
      fileshare.
      **Public:** https://nextcloud.stoneyshome.com via Cloudflare tunnel
      (cloudflared service on the file server, hostname -> HTTP localhost:80;
      Samba is NOT published). Nextcloud: trusted_domains += the public name +
      fileserver.localdomain, trusted_proxies 127.0.0.1/::1,
      overwritecondaddr ^(127.0.0.1|::1)$ (HTTPS overwrite only for tunnel
      traffic, so http://fileserver still works on the LAN), HSTS via
      conf nextcloud-security. www-data crontab */5 cron.php (was missing).
      **PHP 8.4.27** since 2026-11-08 (ppa:ondrej/php; Apache mod php8.4, CLI
      via update-alternatives; php8.3 still installed for rollback:
      a2dismod php8.4 && a2enmod php8.3 && update-alternatives --set php
      /usr/bin/php8.3). Setup checks clean except: Talk HPB (not installed
      on purpose: peer-to-peer calls need no open ports; HPB would need UDP
      port forwards; add a hosted TURN if a call fails), mail not
      set, 2FA not enforced (recommend TOTP for stonec).
      **Layout changed 2026-11-08 (user's request):** Nextcloud datadirectory
      is now **/srv/share/.nextcloud** (SAN); oc_storages local:: id updated;
      the old dir is kept at /var/lib/nextcloud-data.old (80 MB, delete
      once confirmed). Samba "My Files" = /srv/share/.nextcloud/%U/files =
      the user's own Nextcloud files (the "My Files" external mount and
      /srv/share/users were removed). Public unchanged (/srv/share/public,
      external mount + Samba). filesystem_check_changes=1 (Samba edits show
      immediately), skeleton/template dirs disabled. Permissions:
      .nextcloud 0770 + g:fsusers:--x; user home 0750 + u:<user>:--x;
      files dir ACL u:<user>:rwX + u:www-data:rwX (+ defaults); appdata and
      internal files 0750/0640 (www-data only). fs-user has a `fixperms`
      action. Retested: both directions, Public, privacy (other users,
      appdata, log, data folder all blocked). Users stonec and jessy exist.
      HTTPS/trusted domain for the public name, first real users.
- [ ] **CPU-Share / Akash provider:** the app doesn't work on Ubuntu 26.04; parked
      by the user (no snapshot or backup of VM 104 exists).
- [ ] **Old SAN: Dell EqualLogic PS6000** (raised 2026-11-08, for another day). Its
      Group Manager web UI is a Java applet, which modern browsers can't run.
      Options: the CLI over SSH/telnet or the serial port (full management);
      the standalone Group Manager Java app; or an old-Java VM. Possible use:
      iSCSI storage for Proxmox (PBS datastore and bulk/low-IO VM disks; about 16 TB raw, so about 11-13 TB usable in RAID 6/50; 1 GbE ports, use multipath). Service Tag 6V9CVL1; two Type 7 control modules (C0/C1), each with
      4 x 1 GbE and a serial port; active/standby, so 4 ports carry traffic
      (about 400 MB/s); needs a storage switch or VLAN plus multipath. **Set up 2026-11-08:** password recovery (Ctrl-P → `EQL>` prompt,
      `setenv RESETPASSWORD 1`, `reload`), then `reset` (DeleteAllMyDataNow),
      then `setup`. Group `eqlgroup` at **10.10.1.31**, member `eql1` on
      **eth3 10.10.1.30** (switch port 26 on USW Pro 48; eth0 has no link).
      The grpadmin password is the user's (in their password manager; the
      temp file was shredded). Firmware V7.0.4; GUI is a Java applet.
      Disks: 15 × 1 TB SATA 7200 (Hitachi HUA722010CLA330 / Seagate
      ST1000NM0011), all healthy; slot 15 spare; **slot 8 empty/missing**
      (the user may add it later, hot-add). RAID policy **raid6** (~10.5 TB).
      Warnings: **PSU 1 has no AC/failed**, CMOS coin battery low (2.65 V),
      the cache battery was charging, and the second control module (slot 0)
      was pulled for the reset; reinsert it. Was: plan (2026-11-08): console via a USB-serial adapter + null-modem DB9
      cable into the R630 (`/dev/ttyUSB0`, picocom, 9600 8N1) through the
      Proxmox agent session tools; no laptop needed. The user is OK using the
      factory default login `grpadmin` for now: keep the SAN's Ethernet off
      the LAN until the password is changed (the new password goes in via a
      root-only file, never chat). A Windows agent with serial tools is a
      possible separate project. Need: its
      group/management IP, firmware version and whether the admin password
      (default grpadmin) is known.
- [x] **Failing DIMM?** Not on the R630 (SEL clean, EDAC 0 errors). The user thinks the memory issue is on their *other* server; check that one when it comes up.
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
