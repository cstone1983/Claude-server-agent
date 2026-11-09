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
| Servers (new VLAN 10) | VMs: n8n/hub, file server, HA, apps; later the Proxmox host/iDRAC/SAN | internet; IoT (HA controls devices); UDM API (n8n); not Trusted |
| DMZ (new VLAN 20) | CPU-Share / Akash only | internet only |
| IoT (VLAN 3) | plugs, lamps, TVs... | internet (or less); nothing local except DNS/DHCP/NTP; specific HA ports only if a device must push to HA |
| Cameras (VLAN 4) | cameras | the UDM only (UniFi Protect, DHCP, DNS, NTP); no internet |
| Guest (VLAN 2) | visitors | internet only |
| (later) Storage | SAN iSCSI | Proxmox host only, not routed |

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
- Phase 7 (separate window, optional): Proxmox host, iDRAC, SAN to a management
  /storage VLAN with the iDRAC console as the fallback. Until then they stay on
  Main behind the new rules.

Desktops 101 (Anthony) and 106 (Jessy) stay on Trusted (people use them).
