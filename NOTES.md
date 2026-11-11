# Project notes (state as of 2026-11-08)

Running notes for whoever picks this up next, including Claude after a context
reset. Design is in [PLAN.md](PLAN.md), setup in [INSTALL.md](INSTALL.md) and
usage in [README.md](README.md).

## Current state and next steps (2026-11-11 night, after the network overhaul)

**2026-11-11 04:30: backups after the moves all OK (22:15 file server, 23:00
off-site 125G in 121 min). Firewall review round 1 done; round 2 after the
NEW-only counters have run for several hours.**

- **Network overhaul: all moves DONE 2026-11-11** (details and rollback info:
  docs/network-overhaul.md, status lines at the top of "Final runbook").
  - Servers VLAN 10 (10.10.10.0/23): hardware in 10.10.10.x (Proxmox host
    10.10.10.15, iDRAC 10.10.10.10), VMs at 10.10.11.<VMID> (n8n .102, HA
    .103, Solar .100, Recipes .107, Finance .111, fileserver .112, Autopilot
    .110 - stopped). DMZ: CPU-Share 10.10.20.104. Storage VLAN 40: SAN
    10.10.40.10/.11, Proxmox 10.10.40.100. Cameras 10.10.4.10/.11, Lutron
    10.10.3.11. Main keeps the desktops (101 .185, 106 .254), TVs, phones.
  - DNS `<name>.localdomain` for every server; `proxmox` and `idrac` records
    do NOT resolve (fix). Proxmox UI: https://10.10.10.15:8006.
  - Proxmox login allow-list ON (8006/22/8007/consoles only from Main + VPN).
- **Left to do (recap tomorrow), in order:**
  1. Firewall review round 2 (round 1 done 2026-11-11 04:30, see
     docs/network-overhaul.md): OBSERVE rules now count NEW connections
     only; re-read their counters vs /root/net-overhaul/observe-baseline-*.tsv
     on n8n, allow what is needed, then switch them to BLOCK (NEW-only! a
     plain block would also drop replies and cut HA off from IoT). Remove
     old IPs from the OBJ groups.
  2. **SOON: per-VM firewalls** (Proxmox VM firewall, one allow-list per
     VM; the VMs on Servers can reach each other freely today). See To do.
  3. iDRAC IP Blocking (user will do it: Network > Advanced, 10.10.1.0/24).
  4. Fix the `proxmox`/`idrac` DNS records; Collabora (broken before the
     move); change the Solar `solar_ro` DB password; SAN: PSU 1 failed (user
     may leave it out to save power when racked); tidy the 16 migrated
     legacy UDM policies.
     SAN 2026-11-11: user inserted the second control module (slot 0 =
     secondary/standby, same FW V7.0.4, cache battery "low voltage" while
     charging - recheck) and cabled it to switch port 26 -> set native
     Storage VLAN 40 (port 31 = active controller eth3). Also a disk in
     slot 8: "foreign" (old label), SMART ok - needs `member select eql1
     disk select 8 clear-label` (wipes that disk) to join; ASK the user.
  5. Off-site: delete the old per-VM Drive folders; `--fast-list`.
  6. **Internal HTTPS - DONE 2026-11-11** (docs/internal-https.md):
     proxmox (no port needed), unifi, pbs, ha, idrac .lan.stoneyshome.com
     all work with trusted certs. Open: roll the Cloudflare
     token (first ~30 chars were printed once); move the public n8n
     router to the cfdns resolver; remove the HA SSH add-on's password;
     UDM encrypted DNS upstream + rule "DNS only via the UDM" (with the
     firewall round 2). Original plan text:
     Needs from the user: a Cloudflare API token (Edit zone DNS,
     stoneyshome.com only) in a root-only file on n8n. Use two kinds of
     names: machine names `<host>.lan.stoneyshome.com` -> the server's own
     IP (SSH/PuTTY, SMB) and web names -> the Traefik proxy on n8n. (The user
     tried SSH to n8n.stoneyshome.com - that is the public Cloudflare name,
     web only.)
- **WAN drops (found 2026-11-11):** every internet drop since Oct 17 starts
  with SMART_POWER_MODEM_RESTARTED: the USP-Plug (modem outlet, "auto power
  cycle" ON) restarts the modem. The plug is Wi-Fi meshed (was Porch LR,
  now Basement AP -59 dBm). User: keep auto restart, lock the plug's uplink
  to the Basement AP (to do once the UDM Network app is back after the
  2026-11-11 UDM update). Switch also auto power-cycles stuck PoE devices
  (Upstairs AP port 5 today, U7 port 48 before).
- **Wi-Fi review + tuning 2026-11-11** (backups of the old settings in
  /root/net-overhaul/review/*.before-wifi-tuning.json on n8n):
  - Fixed channels: 2.4 GHz Upstairs 1 / Basement 11 / Porch LR 6 /
    Garage LR 11 / U7 Ultra 6; 5 GHz Basement 100 (40 MHz), Porch LR 116,
    Garage LR 132, U7 Ultra 36 (80 MHz), Upstairs AUTO (fixed DFS 52 made it
    drop its config and need re-adopting; it recovered by itself).
    2.4 GHz power medium on Upstairs + Basement. Old: all auto, two APs on
    5 GHz 149 next to 189 neighbour networks, Upstairs+Basement both on 1.
  - SSID Obi-Wan: min rate 6 Mb/s (2.4) / 12 Mb/s (5), BSS transition on,
    WPA2/WPA3 transition, PMF optional. IoT + Guest unchanged.
  - IoT devices took 5-20 min to rejoin after the channel change (HA
    unavailable 698 -> 757 -> recovering). Re-check; power-cycle laggards.
  - Wi-Fi follow-up: 9 devices not back after the tuning: the 2 TVs, 2
    tablets, laptop, watch (Obi-Wan - suspect the WPA2/WPA3 transition) and
    Porch Kasa bulbs/plug/pool lights (suspect Porch LR 2.4 GHz ch 6).
    User 2026-11-11: NO revert - the TVs were simply off and the laptop is
    connected; watch the others (tablets, watch, porch Kasa) and revert only
    if they stay off. HA SSH password login DISABLED
    2026-11-11 (key only).
  - Physical faults (user): switch port 48 (U7 Ultra) and port 42 (Garage
    LR) link at 100 Mb/s - cable/jack; port 48 had ~2000 rx errors.
  - Also found: nightly UDM speed test fails ("Error"); the guest network
    is open with a click-through portal (no password) - user wants a better
    guest portal (decide: password or voucher).
- **Backups:** old 23:30 vzdump job DISABLED 2026-11-11. Nightly: 21:30 PBS
  (all 9 VMs; 2026-11-11 run OK), 22:15 file server files, 23:00 off-site
  sync to Google Drive (first full copy done 2026-11-10).
- **Game server for Anthony (planned, not started):** Pelican panel + Wings +
  the free "Modpack Manager" plugin (CurseForge/Modrinth one-click modpacks;
  needs a CurseForge API key; panel installed natively), on a new VM
  "game-server" (~6 cores, 16 GB, 100 GB) in the DMZ. Fallback: Pterodactyl.
  Waiting for the user's go. Minecraft on VM 101 stopped/disabled; UDM
  forward "Minecraft" WAN 25565 -> 10.10.1.185 still enabled;
  home.stoneyshome.com -> WAN 172.100.91.63 (dynamic, no updater).
- **Hub:** v0.1.29 on n8n and all agents. Open hub ideas: persist approvals
  across restarts; agent result buffering across Cloudflare resets; home
  portal + SSO (To do section).
- **Thermostat Pi:** IoT 10.10.3.10, reaches HA via homeassistant.localdomain.
- **Bookmarks (user's wish 2026-11-11):** the user sends a Chrome
  bookmarks export (.html) when addresses change; only UPDATE the server
  addresses in it (no reorganizing), and hand back an importable file.
  **New server addresses always go into the "Servers" folder.** Bar layout
  (2026-11-11): Lincoln Financial, Special Fund Account, then the
  "Servers" folder; everything else is under Other bookmarks. The
  "Servers" folder holds: Proxmox, Backups (PBS), UniFi,
  iDRAC, Home Assistant (all https://<name>.lan.stoneyshome.com), n8n,
  Server Agent Hub, Nextcloud, AI Recipes, Finance, Solar, Space (public
  https://<name>.stoneyshome.com names). iDRAC moved to
  https://idrac.lan.stoneyshome.com on 2026-11-11.
- User preferences: approve via the hub (remembered device); hands-off mode
  is used at times (then ask in chat before risky steps); reboots/updates
  only when the user says; keep notes updated; recommendations rather than
  option lists.

## Secrets and where they live (never in chat or in this repo)

- **Cloudflare API token "lan certificates"** (created 2026-11-11 by the
  user; permission: DNS Edit on zone stoneyshome.com only; no expiration;
  no IP filter). Stored root-only (0600) as **`/root/cf-dns-token`** on
  **Proxmox** and on **n8n**. Used for Let's Encrypt DNS-01 certificates
  (`*.lan.stoneyshome.com`: Traefik on n8n, Proxmox ACME). Reuse it for any
  other service that needs a certificate or Cloudflare DNS changes: copy the
  file as root (e.g. `install -m 600`), never print it. Revoke/rotate in
  Cloudflare -> My Profile -> API Tokens, then rewrite both files (and the
  Proxmox ACME plugin, which keeps its own copy).
- **EqualLogic SAN grpadmin password**: `/root/.config/eql/pass` on Proxmox
  (used by `/usr/local/sbin/eql-cli`); the user's password manager.
- **UniFi local admin**: `/root/.config/unifi/.env` on n8n (`unifi` helper).
- **Backup alert token**: `/etc/backup-report.conf` on Proxmox.
- **Thermostat MQTT**: `/etc/thermostat.env` on the Pi.
- **Server notifications (Telegram bot "Stoneys servers")**: n8n credential
  "Stoneys servers" (id dBIuMpWGXYcBrEnX, user's chat id 6897458189);
  workflow "Server Notifications (Telegram)" (id Unagh3QcCttjPfnq): callable
  from other workflows (text, level, source) or via a keyed POST webhook.
  Webhook path + key in `/root/.config/server-notify/env` on n8n (root-only);
  helper `/usr/local/bin/server-notify <ok|info|warn|alert> <source> <text|->`
  (scripts/server-notify). Scheduled checks: docs/server-checks.md (prompts,
  pre-approved hub rules, scripts). The user creates the two tasks in the
  Claude app (tasks created by Claude can't carry connectors). Still to do:
  move other server alerts (backup reports) to this bot.

- **SSH into Home Assistant** (Terminal & SSH add-on, port 22): key
  `/root/.ssh/ha_ssh` on n8n (root-only); the add-on accepts it only
  `from="10.10.11.102"`. Use: `ssh -i /root/.ssh/ha_ssh
  root@homeassistant.lan.stoneyshome.com` (via the hub on n8n). The add-on
  ALSO still has password login with a weak password that was shown in a
  tool output on 2026-11-11 - to be removed (ask the user first).

## What exists

- **csa-hub** runs on the **n8n** VM (10.10.11.102 since 2026-11-11; was 10.10.1.86).
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
| cpu-share | VM 104, DMZ 10.10.20.104 (since 2026-11-11); new and empty, 24 vCPU / 64 GB; to become an **Akash compute provider** | **Ubuntu 26.04.1** | 498 GB |
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

## Updates 2026-11-10

- All agents v0.1.29. OS updates: cpu-share, n8n, Proxmox (vim + PBS 4.2.10),
  Fileshare (incl. the unused PHP 8.3 packages: user chose to keep and update
  them; Apache stays on PHP 8.4), thermostat Pi. Remaining are Ubuntu phased
  updates. cpu-share logrotate fixed (duplicate cloud-init logrotate file moved
  to /root); Proxmox PBS enterprise repo disabled (nightly apt 401).

## Hub login: remembered devices (v0.1.26, 2026-11-10)

- Login/approval pages offer "Remember this device": a long-lived cookie
  (~400 days, renewed on every use, stored as a hash) that keeps the browser
  logged in and approves without a code. Ask-always requests (Proxmox reboot)
  still need a current code, on the approval page and the dashboard.
- Manage on /approvals -> "Remembered devices" (Forget). Logout forgets the
  current browser; `sudo csa-hub revoke-sessions` forgets all devices and
  ends all logins.
- Why logins "did not last 7 days": the session cookie was SameSite=Strict, so
  it was not sent when an approval link was opened from the Claude app. Now
  Lax (cross-site form posts are still refused).
- Hub updated to v0.1.26 on 2026-11-10; the agents are still v0.1.25 (no
  agent change in this release).

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

## Proxmox host memory: ZFS ARC capped (2026-11-09)

- 188 GB RAM. The ZFS cache (ARC, for the "VM" pool; root is ext4/LVM) had
  the old default limit of half the RAM and sat at 94 GB, so the host looked
  nearly full. Capped at 32 GB: `/etc/modprobe.d/zfs.conf` =
  `options zfs zfs_arc_max=34359738368`, applied live and initramfs rebuilt.
  Right after: ARC 29 GB, ~107 GB available (was ~44).
- Undo: delete the file, `echo 0 > /sys/module/zfs/parameters/zfs_arc_max`,
  `update-initramfs -u -k all`.
- VMs use far less than configured (2026-11-09: ~55 GB used of ~165 GB
  configured); KSM saves ~11 GB. CPU-Share (104) is configured with 64 GB.

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

## Thermostat Pi (RPi5, agent "thermostat-pi", IoT 10.10.3.10)

- Basement DHT22 temperature/humidity sensor; the relay (oil furnace) is
  UNWIRED, the user uses it as a sensor only. HA entity
  climate.basement_thermostat (MQTT, broker = Mosquitto on HA).
- Wi-Fi Obi-Wan-IoT since 2026-11-09 (old Obi-Wan profile kept, priority 0).
- 2026-11-10: first OS update since setup (488 packages), kernel 6.12 -> 6.18
  (6.18.60+rpt-rpi-2712, old kernel still installed); rebooted, thermostat,
  MQTT and agent came back on their own.
- Script /home/stonec/thermostat_client/thermostat.py, `thermostat.service`
  (user stonec). Rewritten 2026-11-09 (backup thermostat.py.bak-202611091907):
  MQTT by name (homeassistant.localdomain), credentials in root-only
  /etc/thermostat.env (drop-in thermostat.service.d/env.conf), auto-reconnect
  even if the broker is down at start, mode/target saved in state.json
  (default off), input validation, sensor glitch filter, relay off on stop
  and when readings go stale, availability topic thermostat/availability
  (LWT "offline"), humidity on thermostat/humidity. Dead post to the old
  server 10.10.1.206 removed.
- The MQTT password (mqtt-user) is weak and was shown once in a session
  output: change it in the Mosquitto add-on, then in /etc/thermostat.env.
- Optional HA side: availability_topic thermostat/availability on the climate
  entity; a humidity sensor from thermostat/humidity. A stale retained topic
  thermostat/status_text from an older script is still on the broker.

## Anthony's desktop (VM 101, Ubuntu-Anthony)

- 14.3 GB RAM, 16 vCPUs. Runs the Minecraft server (`minecraft.service`,
  /opt/minecraft, user minecraft, port 25565, forwarded on the UDM to
  10.10.1.185). 2026-11-09: Java heap lowered from `-Xmx16384M -Xms16300M`
  (more than the VM has, it squeezed the desktop) to `-Xmx8G -Xms8G`; unit
  backup /etc/systemd/system/minecraft.service.bak-*.
  Later the same day: stopped and disabled (not used); files in /opt/minecraft
  kept. Start again with `systemctl enable --now minecraft.service`.

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

- [ ] **SOON - Per-VM firewalls (user's request 2026-11-11).** The UDM only
      filters BETWEEN VLANs; the VMs on Servers can reach each other freely.
      Plan: Proxmox VM firewalls (filtered at the bridge, so they work inside
      one VLAN and can't be turned off from inside a compromised VM), one
      allow-list per VM: inventory listening ports per VM, decide who needs
      each, log-first (policy ACCEPT + log) for a day, then DROP. Start with
      the internet-facing ones (n8n/hub, Recipes, Fiance, Nextcloud).
      Optional extra inside each VM: ufw. CPU-Share already has one (104.fw).
- [ ] **Network overhaul: enforce the blocks** after reviewing the observe
      logs (IoT, Cameras, Servers, DMZ); keep allow rules above the blocks
      (the "HA to TVs on Main" rule was added after the observe rule). Then
      remove the old IPs from the OBJ groups. All VMs moved 2026-11-11.
- [ ] **Collabora (Nextcloud office) not working** - the built-in CODE
      server stays "starting"; discovery has returned 503 every hour since
      at least 2026-11-10 (before the network move). wopi_url is now
      http://fileserver.localdomain/apps/richdocumentscode/proxy.php?req=.
- [ ] **UDM DNS records `proxmox` and `idrac` don't resolve** (the others
      do). Recreate or check for a name clash.
- [ ] **Change the Solar read-only DB password** (`solar_ro`): it was
      printed in a tool output during the move (LAN-only, read-only user).
- [ ] **Future project: one home portal + single sign-on** (user's idea,
      2026-11-10). There are now many separate dashboards, each with its own
      login: Proxmox, PBS, iDRAC, UniFi, Home Assistant, n8n, Nextcloud, the
      hub's approvals page, Solar-Calc, AI Recipes, Finance, the EqualLogic SAN.
      Idea: one start page linking them all (status at a glance), plus one
      login in front of them. Things to weigh when we get to it:
      - Portal: a dashboard app (e.g. Homepage, Homarr, Dashy) with live
        status widgets, or a page served by the hub.
      - SSO: an identity provider (e.g. Authentik or Authelia) in front of the
        Traefik on n8n (forward-auth), with OIDC where an app supports it
        (Proxmox, PBS, Nextcloud, n8n, HA via add-on). Apps without SSO keep
        their own login behind the proxy.
      - Fits with the internal HTTPS plan (docs/internal-https.md): the same
        Traefik and `*.lan.stoneyshome.com` names; do that first.
      - Keep a break-glass path: every service must still be reachable by IP
        with its own admin login if the SSO or n8n is down.
      - Public apps behind Cloudflare could use Cloudflare Access instead of,
        or in addition to, the internal SSO.
- [ ] **Agent: keep a finished result across a hub reconnect.** Cloudflare
      resets the agents' connections now and then (all agents at once, 1-2 s);
      a command finishing in that window loses its result ("agent disconnected
      while running the command"). Buffer the result and resend it after
      reconnecting.
- [ ] **Retire the old off-site backup.** pve-offsite-backup.timer was
      disabled 2026-11-10 (it failed nightly: its VMs moved to PBS). After the
      first PBS off-site copy is verified, also disable the old 23:30 vzdump job
      (backup-79526ae3-5f24, now only VM 100) and remove both when sure.

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
      **eth3 10.10.1.30** (switch port 31 on USW Pro 48; eth0 has no link). **Moved 2026-11-11 to Storage VLAN 40: group 10.10.40.10, eth3 10.10.40.11, eth0 10.10.40.12; Proxmox 10.10.40.100 (vmbr0.40).** CLI over serial: `/usr/local/sbin/eql-cli` on Proxmox (password in root-only /root/.config/eql/pass).
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
