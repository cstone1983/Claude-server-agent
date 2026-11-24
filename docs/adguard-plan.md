# AdGuard Home with per-group rules - PLAN (2026-11-24, nothing built yet)

Goal: one DNS filter for the house with different rules per group of devices:
- **Parents' own devices:** full access, minimal blocking (ads, trackers, malware only).
- **Kids' devices:** adult and other sensitive content blocked, safe search forced,
  chosen apps/services blocked or limited by schedule, bypass routes closed.
- **IoT devices:** block as much as possible while each device keeps working.
- **Servers:** unchanged (keep using the router's DNS), so backups, updates and the hub never
  depend on the filter.

Names, devices and MAC addresses stay out of this repo (it is public); they live only in
AdGuard's own config on the server.

## 1. Where it runs

- Proxmox LXC container **116 "adguard"** (Debian 13): 1 core, 512 MB RAM, 4 GB disk, on the
  Servers VLAN at **10.10.11.116**, name `adguard.lan.stoneyshome.com`. Web UI through Traefik
  at `https://adguard.lan.stoneyshome.com` (Main + VPN only). Hub agent installed.
- Upstream DNS: encrypted (DNS-over-HTTPS) to Quad9 and Cloudflare, DNSSEC on.
- **Optimistic cache** on: if the internet drops (the modem restarts), devices keep getting the
  last known answers instead of errors.
- Questions for `*.lan.stoneyshome.com` and `*.localdomain` go to the router, so local names
  keep working.
- Admin password in a root-only file on the container; Claude uses the AdGuard API from there
  through the hub and never sees the password in chat.
- Backups: nightly PBS like the other VMs, plus a copy of `AdGuardHome.yaml` after every change.

### Failover (important for kids and IoT)
Phones and PCs use a second DNS server whenever they like, not only when the first is down. So:
- **Main (parents):** AdGuard first, router second. If AdGuard is down, nothing breaks.
- **Kids + IoT:** AdGuard **only**; giving them the router as a second server would be an
  open door around every rule. To avoid a single point of failure, a **second AdGuard** with the
  same config (adguardhome-sync), ideally on hardware other than Proxmox (a small Pi), so a
  Proxmox reboot doesn't take DNS away from those networks. Until then: during a Proxmox reboot
  those devices lose name lookups for a few minutes.

## 2. Telling the groups apart

AdGuard sees each device's IP address (the router does DHCP), so each device needs a stable
address and a group:
- **IoT:** already its own VLAN (10.10.3.0/24): the whole subnet is the IoT group, with
  per-device exceptions where needed.
- **Kids:** recommended: a new **Kids network (VLAN 50, own Wi-Fi name)** - every device that
  joins it gets the kids' rules automatically, including new ones, and the router firewall can add
  rules for it. Alternative: fixed IPs on Main for each kid device (works, but a new device or a
  MAC-randomising phone silently gets parent rules).
- **Parents:** Main network (10.10.1.0/24) by default, so any unknown device on Main gets the
  light rules.
- **Guest:** light rules (ads + malware), nothing else.

Phones randomise their Wi-Fi MAC per network; on the Kids Wi-Fi that doesn't matter (the network
decides), on Main it would break fixed IPs.

## 3. Rules per group

| | Parents | Kids | IoT | Guest |
|---|---|---|---|---|
| Ads + trackers | light list (AdGuard DNS filter) | HaGeZi Pro | HaGeZi Pro++ / Ultimate + device-maker telemetry lists | light list |
| Malware, phishing, scams | yes | yes | yes | yes |
| Adult content | - | yes (AdGuard parental control + NSFW list) | n/a | - |
| Gambling, dating, piracy, drugs | - | yes | n/a | - |
| Safe search (Google, Bing, DuckDuckGo) + YouTube restricted | - | forced | n/a | - |
| VPNs, proxies, Tor, encrypted-DNS bypass | - | blocked (HaGeZi DoH/VPN/TOR list) | blocked | - |
| Newly registered domains | - | blocked | blocked | - |
| Chosen services (e.g. TikTok, Discord, Roblox, Snapchat) | - | blocked or on a schedule | n/a | - |
| Bedtime schedule | - | games/social off e.g. 21:00-07:00 school nights | - | - |
| Query log | 7 days | 30 days | 30 days | 1 day |

- **Kids' requests:** "this site/app doesn't work" -> Claude looks up what was blocked for that
  device and you decide; a one-off allow can be for one device only.
- **IoT method:**
  1. One week **log only** (nothing blocked) to learn what each device really uses.
  2. Then block everything on the strong lists, and allow back exactly what each device needs
     (checked against Home Assistant: every integration still updating).
  3. For simple devices (plugs, bulbs that only talk to Home Assistant locally): **allowlist
     mode** - block everything except named domains (often none).
  4. Watch for a week; anything new or odd goes into the weekly report.

## 4. Closing the ways around it (needed for kids and IoT)

DNS filtering only works if devices actually use it:
- **Router firewall (Kids + IoT zones):** allow DNS (53 and DNS-over-TLS 853) only to AdGuard;
  block it to everything else, so devices with Google DNS hard-coded can't go around.
  Encrypted DNS over HTTPS can't be blocked by port; the bypass list above blocks the known
  providers by name.
- **On the kids' phones/tablets:** Android "Private DNS" off (locked via Family Link), iPhone/iPad
  via Screen Time; Chrome/Edge "use secure DNS" off.
- **Limits, honestly:** on mobile data (away from home Wi-Fi) none of this applies; a hotspot or a
  determined VPN gets around it; DNS can't filter inside an allowed site (a YouTube video, a
  Discord server). Pair it with Family Link / Screen Time on the devices themselves.
  Optional later: AdGuard's encrypted DNS with a per-device ID on the kids' phones, so the rules
  follow them on mobile data (needs a public address through a Cloudflare tunnel).

## 5. Rollout (each step undone by switching that network's DNS back to the router)

1. Build the container, config, Traefik route, backups; test from one PC by hand.
2. **IoT network:** log-only week, then blocking with per-device allowlists; check Home
   Assistant after each change.
3. **Kids:** create the Kids VLAN + Wi-Fi, move their devices, turn on the kids' rules and the
   firewall bypass blocks; check their usual apps/schoolwork sites with you.
4. **Main (parents)** with router fallback. Guest last.
5. Second AdGuard + sync (when there's hardware for it).

## 6. Running it

- Claude manages it through the hub: unblock/block, per-device exceptions, list changes, reports.
  Changes need your approval at first; routine ones ("allow this domain for this device") can be
  always-allowed later.
- **Weekly Telegram report:** top blocks per group, busiest devices, IoT devices contacting
  new domains, anything blocked for the kids that looks like a mistake.
- **Alerts:** AdGuard not answering, filter lists not updating, a device on Kids/IoT trying other
  DNS servers.
- Privacy: kids' query logs are kept 30 days for troubleshooting, then deleted automatically.

## 7. Needed from you before building

1. The kids' devices (type + rough age per kid, which sets how strict) - in AdGuard only.
2. Which services to block outright and which only at bedtime, and the bedtime hours.
3. Your own devices (anything on Main that should NOT be treated as a parent device?).
4. Kids network (VLAN 50 + Wi-Fi): yes or fixed IPs on Main?
5. Second AdGuard on separate hardware: buy a small Pi, use an existing device, or skip for now?
