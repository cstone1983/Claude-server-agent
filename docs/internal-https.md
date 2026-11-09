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

- **Proxy** (user's decision 2026-11-09): the existing **Traefik on the n8n
  VM** (compose in /home/stonec/n8n-compose; today it serves only
  n8n.stoneyshome.com, entrypoints :80/:443, docker provider, a TLS-challenge
  resolver `mytlschallenge`). Every `<name>.lan.stoneyshome.com` record
  points at n8n's IP (10.10.11.102 after the overhaul). If n8n is down,
  internal HTTPS names are down with it; every service stays reachable by IP.
  The hub itself does not go through Traefik (cloudflared -> localhost:7420),
  so this change cannot cut Claude off.
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
3. **Traefik on n8n**: back up docker-compose.yml and the acme storage; add
   a DNS-01 resolver `cfdns` (`--certificatesresolvers.cfdns.acme.dnschallenge.provider=cloudflare`,
   token from a root-only file via `CF_DNS_API_TOKEN_FILE`, staging CA first),
   a file provider (`--providers.file.directory=/etc/traefik/dynamic`,
   mounted read-only) for routes to services that are not containers here,
   and one router that requests `lan.stoneyshome.com` + `*.lan.stoneyshome.com`.
   Recreating the Traefik container blips n8n.stoneyshome.com (public n8n,
   webhooks, the n8n MCP) for a few seconds; the hub is not affected. While
   there: turn off `--api.insecure` (dashboard not needed).
4. **Routes**, one per service, each verified before the next; DNS record
   `<name>.lan.stoneyshome.com` -> n8n's IP:
   | Name | Upstream |
   |---|---|
   | homeassistant | http://homeassistant:8123 (HA: add the proxy to `trusted_proxies`, `use_x_forwarded_for`) |
   | nextcloud (LAN) | http://fileserver (add the name to `trusted_domains`, proxy to `trusted_proxies`) |
   | n8n | the n8n container directly (same Traefik; keep the public n8n.stoneyshome.com router as is) |
   | solar | http://solar:8080 (add the name to `SOLAR_WRITE_HOSTS` only if the LAN-only pages must work through it) |
   | recipes, finance | their app ports |
   | unifi | https://10.10.1.1 (UDM UI; needs a proxy -> Gateway 443 allow rule) |
   | idrac | https://idrac (self-signed upstream; console/websocket check) |
   | pbs | https://localhost:8007 on Proxmox (or Proxmox ACME for 8007 too) |
5. **Network domain**: set Main, Servers, IoT, Cameras, DMZ domain to
   `lan.stoneyshome.com` (clients pick it up at their next DHCP renewal);
   check `\\fileserver` and short names still work.
6. **Firewall**: Trusted/VPN -> n8n 443 is already allowed (rule 1); n8n ->
   Gateway 443 is already rule 5 (UDM UI). n8n -> iDRAC/Proxmox: same Servers
   VLAN after Phase 7, Servers -> Internal before that (needs a narrow allow
   if those routes are wanted earlier).
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
