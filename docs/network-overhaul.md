# Network overhaul plan (draft 2026-11-08)

Goal: separate the network into zones with clear "who may start connections to
whom" rules, without breaking services. Replies to an allowed connection always
flow back (stateful firewall), so "Trusted may connect to IoT, IoT may not
connect to Trusted" is a single allow rule plus a default block.

## Today (inventory 2026-11-08)

- Gateway UDM Pro SE, switch USW Pro 48 PoE, APs: 2 x U6 Lite, 2 x U6 LR,
  1 x U7 Ultra. All support VLANs and the zone-based firewall: no new hardware.
- Networks: Main 10.10.1.0/24 (untagged), Guest VLAN 2 (open Wi-Fi, isolated),
  IoT VLAN 3, Cameras VLAN 4, test VLAN 5, OpenVPN 192.168.3.0/24.
- Firewall: legacy rules mode. Active rules are only "established/related" and
  "Main -> all networks: allow". There are NO block rules (the "Block IoT/Guest
  to Gateway" groups exist but nothing uses them), so IoT and Cameras can most
  likely reach Main today. Only Guest is isolated (guest network type).
- Everything that matters is on Main: Proxmox (10.10.1.15), iDRAC (.10), the
  SAN (.30/.31), all VMs, PCs, phones.

## Target zones

| Zone | Members | May start connections to |
|---|---|---|
| Trusted (Main) | PCs, phones, the two desktop VMs (101, 106) | everything they manage: Servers, IoT, the UDM; internet |
| Servers (new VLAN 10) | VMs: n8n/hub, file server, HA, apps; Proxmox host and iDRAC (Phase 7) | internet; IoT (HA controls devices); UDM API (n8n); not Trusted |
| DMZ (new VLAN 20) | CPU-Share / Akash only | internet only |
| IoT (VLAN 3) | plugs, lamps, TVs... | internet (or less); nothing local except DNS/DHCP/NTP; specific HA ports only if a device must push to HA |
| Cameras (VLAN 4) | cameras | the UDM only (UniFi Protect, DHCP, DNS, NTP); no internet |
| Guest (VLAN 2) | visitors | internet only |
| Storage (new VLAN 40) | SAN iSCSI (EqualLogic) | Proxmox host only; no gateway, not routed |

## Paths that must keep working (dependency map)

1. Internet egress for every server: all public apps use outbound Cloudflare
   tunnels (n8n/hub, file server, Solar-Calc, AI Recipes, finance).
2. Hub <-> agents: agents connect out to agents.stoneyshome.com (via Cloudflare
   to n8n), so they survive IP/VLAN changes as long as internet works.
3. n8n -> UDM API (10.10.1.1:443): the `unifi` helper. Must be allowed BEFORE
   n8n moves, or Claude loses router access mid-change.
4. n8n -> Home Assistant (HVAC, home events, alarm workflows).
5. Solar-Calc (VM 100) -> solar Postgres on n8n (10.10.1.86:5432, socat).
6. Home Assistant -> IoT devices (Kasa etc.), HA -> UniFi Protect on the UDM.
7. Proxmox <-> SAN iSCSI (10.10.1.30/.31): the file server's data. Moving the
   host or the SAN breaks it, so they stay put until a planned window.
8. Trusted -> Proxmox UI :8006, iDRAC, HA, Nextcloud, \\fileserver (Samba).
9. Port forwards: Minecraft -> 10.10.1.185 (VM 101), RDP -> 10.10.1.20 (laptop),
   Akash -> 10.10.1.133 (VM 104).
10. Claude's own access: hub on n8n (VM 102) + Proxmox agent. n8n moves LAST of
    the VMs; the Proxmox host does not move in this project's main phases.

## Order of operations

Every step: one change, then verify, with a stated rollback. Nothing in phases
0-3 changes any server's address.

- Phase 0 (no impact): UDM config backup (Settings > System > Backups, download
  a copy); record current state; give servers DNS names on the UDM so later IP
  changes are one-line updates.
- Phase 1 (no impact): create the Servers (VLAN 10) and DMZ (VLAN 20) networks.
  Nothing uses them yet.
- Phase 2 (decision): switch the UDM to the zone-based firewall (UniFi's
  built-in migration keeps today's behavior), then add the ALLOW rules for the
  dependency map first, block rules second.
- Phase 3 (low risk, instant rollback = disable rule): Cameras -> UDM only.
  IoT -> no local access except DNS/DHCP/NTP. Test HA's integrations and casting
  right after; add a narrow allow only where a device must reach HA.
- Phase 4: DMZ: move CPU-Share (parked; Akash already broken) to VLAN 20,
  update its 6 port forwards. First real server move, lowest stakes.
- Phase 5 (brief blip, quiet hour): make Proxmox's bridge VLAN-aware and confirm
  the R630's switch port carries the tagged VLANs. A network reload on the host
  blips all VMs for a few seconds.
- Phase 6: move VMs to Servers one at a time, ~1-2 min each (VLAN tag in
  Proxmox + DHCP renew/new reservation, update anything that referenced the old
  IP): Recipies (107) -> Solar-Calc (100) -> Fiance (111) -> file server (112)
  -> Autopilot (110, stopped) -> Home Assistant (103, after the HA -> IoT allow
  rule) -> n8n/hub (102) last (after the n8n -> UDM rule; Claude is offline for
  ~1-2 min while it re-addresses).
- Phase 7 (separate window, planned): Proxmox host and iDRAC to Servers
  (VLAN 10), SAN to Storage (VLAN 40). See "Proxmox, iDRAC and Storage" below.
  Until then they stay on Main behind the new rules.

Desktops 101 (Anthony) and 106 (Jessy) stay on Trusted (people use them).

## Status

- Planned window: Wednesday 11 or Thursday 12 November 2026, evening.
- 2026-11-09: Servers widened to a /23: 10.10.10.x infrastructure, 10.10.11.x VMs.
- 2026-11-09: "test" network (VLAN 5) deleted (nothing used it). Networks now:
  Main, Guest (2), IoT (3), Cameras (4), OpenVPN.
- UDM: Network app 10.6.109, still legacy firewall (no zones yet). UDM backup
  taken by the user 2026-11-08. mDNS is shared across all networks (needed for
  HA discovery, casting and Sonos across VLANs; keep it on).
- No Matter devices (integration configured, empty). Zigbee (ZHA) is a USB stick
  on HA: not affected by the network.

## Zone-based firewall definition

UniFi's migration (Settings > Policy Engine / Firewall: "Upgrade to Zone-Based
Firewall") puts Main in **Internal**, Guest in **Hotspot**, OpenVPN in **VPN**,
and recreates today's behavior. It is one-way (undo = restore the UDM backup),
so the user clicks it; right after, run the checks in "Verify after every step".

Zones after the migration plus our edits:

| Zone | Networks |
|---|---|
| Internal (= Trusted) | Main |
| VPN | OpenVPN (treated like Trusted) |
| Servers (custom) | Servers VLAN 10 |
| IoT (custom) | IoT VLAN 3 |
| Cameras (custom) | Cameras VLAN 4 |
| DMZ (built-in) | DMZ VLAN 20 |
| Hotspot | Guest VLAN 2 |
| Gateway / External | the UDM itself / internet |

Policies, top to bottom (allows are created first, blocks last; return traffic
of an allowed connection is always allowed):

| # | From | To | What | Action |
|---|---|---|---|---|
| 1 | Internal, VPN | everything | any | allow |
| 2 | Servers | External | any | allow |
| 3 | Servers | IoT | any (HA controls devices) | allow |
| 4 | Servers | Gateway | DNS 53, DHCP 67, NTP 123 | allow |
| 5 | n8n, HA (by IP) | Gateway | TCP 443 (UDM API, Protect), HA also 7441/7447 (Protect streams) | allow |
| 6 | HA, n8n, Solar-Calc | laptop 10.10.1.20 (Internal) | TCP 11434 (Ollama), 3000 (n8n "OpenAI account" points at http://10.10.1.20:3000) | allow |
| 7 | Servers | Internal, Gateway | rest | block |
| 8 | IoT | Gateway | DNS, DHCP, NTP | allow |
| 9 | RPi5 thermostat | HA | TCP 1883 (MQTT) | allow |
| 10 | Shelly devices | HA | UDP 5683 (CoIoT push) | allow |
| 11 | Sonos | HA | TCP 1400 (event callbacks) | allow |
| 12 | Sonos | Internal | TCP 3400, 3401, 3500 (Sonos app events) | allow |
| 13 | IoT | External | any (tighten per device later) | allow |
| 14 | IoT | Internal, Servers, Gateway, DMZ, Cameras | rest | block |
| 15 | Cameras | Gateway | any (Protect adoption/streams, DHCP, DNS, NTP) | allow |
| 16 | Cameras | everything else incl. External | any | block |
| 17 | DMZ | External | any | allow |
| 18 | DMZ | Gateway | DNS, DHCP, NTP | allow |
| 19 | DMZ | everything else | any | block |
| 20 | Hotspot | External only | (built-in guest isolation) | as is |

Port forwards create their own External -> target allows automatically.
Rules 9-12 use IP or device-group objects (from the reservations below), so the
reservations come first. Every block rule is created with logging on, and the
IoT/Cameras blocks get a 24 h "observe" period (logged, check what hits them)
before anyone relies on them.

## IP plan

| Network | Subnet | Fixed (reservations) | DHCP pool |
|---|---|---|---|
| Main / Trusted | 10.10.1.0/24 | .2-.49 (UDM .1, iDRAC .10, Proxmox .15, laptop .20, SAN .30/.31) | .50-.254 (as now) |
| IoT | 10.10.3.0/24 | .10-.99 (thermostat, Lutron, TVs, Sonos, heat pumps, plugs...) | .100-.254 |
| Cameras | 10.10.4.0/24 | .10-.49 | .50-.254 |
| Servers | 10.10.10.0/23 (10.10.10.0-10.10.11.255, gateway 10.10.10.1) | infrastructure: all of 10.10.10.x (blocks below); VMs: VM n -> 10.10.11.n | 10.10.10.200-.249 |
| DMZ | 10.10.20.0/24 | CPU-Share (VM 104) -> 10.10.20.104 | .200-.249 |
| Storage | 10.10.40.0/24 | SAN .10-.49, host iSCSI ports .100-.199; no gateway | none |

Servers infrastructure blocks, all in 10.10.10.x (room for more hosts and for the unused LAN
ports on the servers and the SAN being brought into use):

| Range | Use |
|---|---|
| .2-.9 | network gear management, if it ever moves off Main |
| .10-.19 | out-of-band consoles: iDRAC of the R630 .10, next server .11, ... |
| .20-.29 | hypervisor hosts, management address: R630 (Proxmox) .20, next host .21, ... |
| .30-.49 | extra host interfaces that need their own address (backup/migration ports, a second management port) |
| .50-.199 | other infrastructure: PBS if it ever runs separately, appliances, NAS, more hosts' extra ports, spare |
| .200-.249 | DHCP pool (temporary / new devices) |

Storage VLAN 40 blocks: SAN group IP .10, SAN member/port IPs .11-.49 (the
EqualLogic has several ports per controller, all can be used); hosts' iSCSI
ports .100-.199, two per host for multipath (R630: .100 and .101).

Ports: the unused server ports are best spent on (1) two dedicated iSCSI ports
on VLAN 40 (multipath: faster, and the file server survives a cable or switch
port failure) and (2) a two-port bond (LACP on the USW Pro 48) for vmbr0, so
the VMs and host management do not depend on one cable. Each is its own small
change with the iDRAC console as the fallback, after Phase 7.

Easy rule for VMs: 10.10.11.<VM ID> (VM 103 -> 10.10.11.103). VM IDs 1-254
fit; 10.10.10.x is never used by a VM. Every server also gets a
local DNS name on the UDM (`n8n`, `homeassistant`, `solar`, `recipes`, `finance`,
`fileserver`, `cpushare`, `proxmox`), and configs switch to the names, so a
future move is one DNS edit. (`fileserver` already exists.)

## Swap runbook (per server)

### Pre-flight (once, before Phase 6)

1. Proxmox vmbr0 is VLAN-aware (Phase 5) and the R630's switch port profile
   carries VLAN 10 and 20 tagged (UniFi port profile "Allow All").
2. Firewall rules 2-7 exist and the checks below pass.
3. For each VM confirm the guest uses DHCP (not a static netplan address) and
   the QEMU guest agent answers: the re-address step runs through the guest
   agent, which works even while the VM's network is changing.
4. Cloudflare dashboard: change the `n8n.stoneyshome.com` route from
   `https://10.10.1.86` to `https://localhost` (No TLS Verify on). Every other
   tunnel route already uses localhost.
5. Before a move, look up the VM's MAC with `qm config <id>`, never from memory.

### Steps for one VM (about 1-2 minutes of downtime)

1. Create the reservation on the UDM: client MAC -> Servers network, fixed IP
   10.10.11.<id>; point the DNS name at the new IP (short TTL, takes effect
   at once on the UDM).
2. Update the server's own config that names its old IP (list below).
3. On Proxmox: `qm set <id> --net0 virtio=<MAC>,bridge=vmbr0,tag=10` (same MAC,
   applied live).
4. Renew the address through the guest agent: `qm guest exec <id> -- networkctl renew <nic>`
   (or `dhclient -r && dhclient`, depending on the guest).
5. Verify: the VM shows the new IP, its agent shows online in the hub, the
   tunnel/app answers, and the checks below pass.
6. Update anything elsewhere that pointed at the old IP (list below).
7. Remove the old Main reservation.

**Rollback:** `qm set <id> --net0 virtio=<MAC>,bridge=vmbr0` (no tag) plus a
renew: the VM returns to Main and its old reservation (kept until step 7).
For n8n (the hub), rollback is done by the user from the Proxmox UI
(https://10.10.1.15:8006) or the iDRAC console, because Claude is offline
while the hub is down.

### Per-server references to update

| VM | Old IP -> new | What refers to it |
|---|---|---|
| 107 Recipies | .94 -> 10.10.11.107 | AI-Recipes `server.ts` and the pm2 dump (bind to 0.0.0.0, not the IP); tunnel uses localhost |
| 100 Solar-Calc | .156 -> 10.10.11.100 | own web `vite.config.js`, `Bills.jsx` (use relative URLs); n8n "Solar Bill Intake" (.156:8080 -> `solar`); the `solar-pg-lan` socat on n8n only accepts `range=10.10.1.156/32` -> change to 10.10.11.100/32 in the same step; n8n "Solar Telegram Sender" and Solar-Calc `billread.js` use the laptop's Ollama (.20), rule 6 |
| 111 Fiance | .136 -> 10.10.11.111 | n8n credential "SSH for Game install" (host .136 -> `finance`); tunnel uses localhost |
| 112 fileserver | .80 -> 10.10.11.112 | Samba `interfaces = lo 10.10.1.0/24` -> the new subnet (keep `hosts allow` for Trusted 10.10.1.0/24); Nextcloud `trusted_domains` (add the new IP, keep names); DNS `fileserver` |
| 110 Autopilot | (stopped) | VLAN tag only |
| 103 Home Assistant | .116 -> 10.10.11.103 | n8n "Homelab Agent" workflow (.116:8123) and the n8n credentials "Home Assistant account", "HA Token", "HA Token 2" (-> `homeassistant`); RPi5 thermostat MQTT broker address; Shelly Gen1 CoIoT peer; HA's own internal URL; Ollama (laptop) rule 6 |
| 102 n8n / hub | .86 -> 10.10.11.102 | Cloudflare n8n route (localhost first, pre-flight 4); Solar-Calc `database.json`/`.env` and `SettingsPanel.jsx` (.86:5432 -> `n8n`); the `solar-pg-lan` container is published on `10.10.1.86:5432` -> rebind to the new IP (or 0.0.0.0, the source `range=` still limits it); n8n credentials "SSH Password account" and "SSH n8n" (host .86 -> `n8n` or 127.0.0.1); the `unifi` helper keeps using 10.10.1.1 (the UDM answers on every network's gateway) |
| 104 CPU-Share | .133 -> 10.10.20.104 (DMZ, Phase 4) | 6 Akash port forwards; Proxmox VM firewall `/etc/pve/firewall/104.fw` (10.10.1.133, 10.10.1.0/24); k3s config/service, ufw and Akash inventory on the VM |
| Proxmox host | .15 -> 10.10.10.20 (Phase 7) | see "Proxmox, iDRAC and Storage" |

### Devices before Home Assistant moves

HA talks to some devices that are still on Main today. Before HA leaves Main,
move them to IoT with reservations (or HA would hit block rule 7): Lutron
Smart Bridge (.95), the LG and Android TVs, the Sonos, the RPi5 thermostat
(.128), the printer. The 7 unplugged Kasa plugs keep their IoT addresses
(10.10.3.14, .78, .88, .117, .148, .237, .247) as reservations so they come
back when plugged in. HA updates the IPs of discovered devices itself (Kasa,
Shelly, Sonos, ESPHome, Android TV); Lutron and the LG TV may need their host
edited in HA. The G5 cameras (.139, .144) move to Cameras; Protect re-adopts
them on their own.

n8n itself (scanned 2026-11-09): workflows, credential host/URL fields,
container env/ports, compose dir, /etc, /usr/local and crontabs. The only IPs
are the ones in the table above; n8n's own address is DHCP (no static netplan),
Ollama/n8n/Postgres containers bind 127.0.0.1, traefik binds all addresses.

### Verify after every step

From n8n (`net-check`, a read-only script): UDM API, HA :8123, Solar :8080,
fileserver :445/:80, Proxmox :8006, the solar Postgres, DNS through the UDM,
internet, Cloudflare. Plus: hub shows every agent online; HA has no new
unavailable integrations; one Telegram test message from n8n.

## Proxmox, iDRAC and Storage (Phase 7)

Decision 2026-11-09: no separate management VLAN. The Proxmox host and the
iDRAC join the Servers VLAN in the infrastructure range (Proxmox
10.10.10.20, iDRAC 10.10.10.10). The SAN gets a separate
non-routed storage VLAN (40, 10.10.40.0/24), so iSCSI never crosses the UDM.

Because traffic inside one VLAN never passes the UDM, the VMs (several of them
internet-facing through Cloudflare) could reach the Proxmox and iDRAC logins
directly. So both get their own allow-lists, set up BEFORE they move:

- Proxmox firewall (Datacenter > Firewall, host level only; VM firewalls are
  untouched): input policy DROP, allow TCP 8006 (UI) and 22 (SSH) from Trusted
  10.10.1.0/24 and VPN 192.168.3.0/24, allow ICMP. The host's own outgoing
  connections (hub agent, updates, PBS off-site, backup reports) are not
  affected. Enable it from the iDRAC console session, so a mistake cannot lock
  everyone out; rollback is `pve-firewall stop`.
- iDRAC (iDRAC Settings > Network > IP Blocking): IP range filter = Trusted
  10.10.1.0/24. (It takes one range: from the VPN, use a Trusted PC or the
  Proxmox UI instead.) Rollback: the server's F2 setup screen or `racadm`
  from the Proxmox host.

Order (one window, iDRAC console open as the fallback):
1. Create VLAN 40 (no DHCP, no gateway, not routed); confirm the R630 and SAN
   switch ports carry the tags. Turn on the two allow-lists above while
   everything is still on Main, and test them from a PC.
2. iDRAC: set its VLAN 10 / 10.10.10.10 in iDRAC settings (independent of the
   host). Verify https://10.10.10.10 from a PC.
3. SAN to Storage: add a Proxmox interface on VLAN 40 (10.10.40.100, no
   gateway), re-address the EqualLogic (group 10.10.40.10, ports .11+;
   serial console as fallback), point `storage.cfg`/iSCSI at
   10.10.40.10, re-login the session. File server (VM 112) stopped during this step: its data disk is on the SAN.
4. Proxmox host: move its address from vmbr0 to a VLAN 10 interface on the
   VLAN-aware bridge (10.10.10.20, gateway 10.10.10.1); update `/etc/hosts`,
   `/etc/issue`, the DNS name `proxmox` and the net-check targets. VMs are not
   affected (their VLAN tags do not change). Rollback: iDRAC console, restore
   `/etc/network/interfaces` from the copy taken first, `ifreload -a`.
5. Verify: hub shows the Proxmox agent online, PBS jobs and the off-site sync
   run, backup reports reach n8n, Proxmox UI and iDRAC open from a PC and do
   NOT open from a VM (e.g. `curl -k https://10.10.10.20:8006` from n8n fails).
