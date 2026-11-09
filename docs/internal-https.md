# Internal HTTPS plan (draft 2026-11-09)

Goal: trusted certificates (no browser warnings) for every internal web UI,
under internal names `<name>.lan.stoneyshome.com`. Done AFTER the network
overhaul (docs/network-overhaul.md), so each piece is set up once on the final
addresses. Nothing is opened to the internet.

## How it works

- Names: the UDM answers `*.lan.stoneyshome.com` inside the network (split
  DNS). They are never added to public Cloudflare DNS. Each network's domain
  becomes `lan.stoneyshome.com`, so short names (`\\fileserver`, `proxmox`)
  keep working through the search domain.
- Certificates: Let's Encrypt, DNS-01 challenge through Cloudflare (a scoped
  API token adds a temporary TXT record). One wildcard
  `*.lan.stoneyshome.com`, so the public certificate logs show only the
  wildcard, not a list of internal machines. Renews itself every ~60 days.
- Delivery: Proxmox uses its built-in ACME client; everything else goes
  through one internal reverse proxy that holds the wildcard.

## Decisions

- **Proxy**: a small dedicated LXC on Proxmox in the Servers infrastructure
  range (proposed `proxy`, 10.10.10.50) running Caddy (with the Cloudflare DNS
  plugin) or Traefik (Cloudflare support built in). Not the Traefik on the n8n
  VM: that one serves n8n itself, and the hub VM should not also become the
  front door for everything else. If the proxy is down, every service is
  still reachable by IP as today.
- **Name suffix**: `lan.stoneyshome.com` (user may pick
  `starwars.stoneyshome.com` instead; the plan is the same).
- **Old names**: `<name>.localdomain` records stay until nothing uses them
  (the four n8n credentials use them today).

## Steps

1. **User**: Cloudflare -> My Profile -> API Tokens -> Create token ->
   "Edit zone DNS" template, Zone = stoneyshome.com only. Save it in a
   root-only file on the proxy host (never in chat), e.g.
   `install -m 600 /dev/null /root/cf-dns-token && nano /root/cf-dns-token`.
2. **Proxmox** (Datacenter -> ACME): account with Let's Encrypt (staging
   first), DNS plugin "cloudflare" with the token, certificate for
   `proxmox.lan.stoneyshome.com` on the node, order, verify, then switch to
   production. Auto-renews. Rollback: the self-signed cert stays in
   /etc/pve/local (pve-ssl.pem) and can be re-selected.
3. **Proxy LXC**: create (Debian, 1 vCPU, 512 MB, Servers VLAN,
   reservation 10.10.10.50, DNS `proxy`), install Caddy/Traefik, wildcard
   certificate via DNS-01 (staging first), agent installed for the hub.
4. **Routes**, one per service, each verified before the next; DNS record
   `<name>.lan.stoneyshome.com` -> 10.10.10.50:
   | Name | Upstream |
   |---|---|
   | homeassistant | http://homeassistant:8123 (HA: add the proxy to `trusted_proxies`, `use_x_forwarded_for`) |
   | nextcloud (LAN) | http://fileserver (add the name to `trusted_domains`, proxy to `trusted_proxies`) |
   | n8n | https://n8n (keep the public n8n.stoneyshome.com tunnel as is) |
   | solar | http://solar:8080 (add the name to `SOLAR_WRITE_HOSTS` only if the LAN-only pages must work through it) |
   | recipes, finance | their app ports |
   | unifi | https://10.10.1.1 (UDM UI; needs a proxy -> Gateway 443 allow rule) |
   | idrac | https://idrac (self-signed upstream; console/websocket check) |
   | pbs | https://localhost:8007 on Proxmox (or Proxmox ACME for 8007 too) |
5. **Network domain**: set Main, Servers, IoT, Cameras, DMZ domain to
   `lan.stoneyshome.com` (clients pick it up at their next DHCP renewal);
   check `\\fileserver` and short names still work.
6. **Firewall**: Trusted/VPN -> proxy 443 is already allowed (rule 1); add
   proxy -> Gateway 443 (UDM UI) and keep everything else default.
7. **Alerts**: certificate expiry check in the morning backup/dead-man run
   (warn when any served cert has < 20 days left).
8. **Later**: switch bookmarks and the n8n credentials to the new names;
   remove the `.localdomain` records.

## Caveats

- Devices that bypass the UDM's DNS (Android Private DNS, Firefox secure DNS,
  iCloud Private Relay, hard-coded 8.8.8.8) will not resolve the internal
  names; fix per device.
- VPN clients need the UDM as their DNS server (check the OpenVPN server's
  DNS setting).
- Let's Encrypt rate limits: always test with the staging server first.
