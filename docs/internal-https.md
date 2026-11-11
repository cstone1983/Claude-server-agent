# Internal HTTPS plan (draft 2026-11-09)

Goal: trusted certificates (no browser warnings) for every internal web UI,
under internal names `<name>.lan.stoneyshome.com`. Done AFTER the network
overhaul (docs/network-overhaul.md), so each piece is set up once on the final
addresses. Nothing is opened to the internet.

## Status

- 2026-11-11 machine names `<host>.lan.stoneyshome.com` on the UDM (proxmox,
  idrac, n8n, homeassistant, solar, recipes, finance, fileserver, autopilot,
  cpushare -> each server's own IP). The UDM needs ~10 s before a new record
  answers. (`proxmox.localdomain`/`idrac.localdomain` never resolved; the
  .lan names do.)
- 2026-11-11 Cloudflare token "lan certificates" (DNS Edit, stoneyshome.com
  only, zone id 24f93d39a2b743de4e883fdf812d9030) in /root/cf-dns-token on
  Proxmox and n8n. NOTE: the first ~30 characters were printed once by
  `pvenode acme plugin list` (it shows plugin data) - roll the token and
  update both files + the Proxmox plugin (`pvenode acme plugin set
  cloudflare --data <file>`); never run `plugin list` with data again.
- 2026-11-11 **Proxmox**: ACME accounts `lan-staging` and `lan` (production,
  contact stonec@gmail.com - user OK'd), plugin `cloudflare` (dns, cf,
  CF_Token + CF_Zone_ID, validation delay 30 s), node domain
  proxmox.lan.stoneyshome.com. Certificate issued by Let's Encrypt, valid to
  2027-02-09, renewed by Proxmox's daily job. Verified trusted from Main.
- 2026-11-11 **Traefik on n8n** (compose backup docker-compose.yml.bak-*,
  acme.json backup in /root/net-overhaul): resolver `cfdns` (DNS-01,
  Cloudflare, resolvers 1.1.1.1, storage /letsencrypt/acme-lan.json, token
  via CF_DNS_API_TOKEN_FILE=/run/secrets/cf_dns_token mounted read-only from
  /root/cf-dns-token), file provider ./traefik-dynamic (lan.yml), insecure
  dashboard off. Wildcard lan.stoneyshome.com + *.lan.stoneyshome.com issued
  (production, to 2027-02-09; staging store kept as acme-lan-staging.json.old).
  Route 1: unifi.lan.stoneyshome.com -> https://10.10.1.1 (skip verify);
  DNS unifi.lan -> 10.10.11.102. Verified trusted from Main.
- 2026-11-11 routes added: pbs.lan -> https://10.10.10.15:8007 (Proxmox
  host.fw: IN ACCEPT -source 10.10.11.102 -dport 8007), idrac.lan ->
  https://10.10.10.10 (passHostHeader false; the iDRAC still redirects to
  https://10.10.10.10/start.html - fix: user sets iDRAC "DNS iDRAC Name"
  idrac + static domain lan.stoneyshome.com, then passHostHeader back to
  true), ha.lan -> http://10.10.11.103:8123. DNS idrac/pbs/ha/unifi.lan ->
  10.10.11.102 (the proxy); for SSH to the iDRAC use 10.10.10.10.
- **HA 2026.10 keeps its HTTP settings in .storage/http, not in
  configuration.yaml** ("yaml_migration_done": true; the YAML http: block
  that Homeway added is ignored). Change them in Settings -> System ->
  Network -> HTTP server (saves, restarts, then must be CONFIRMED within ~5
  min or HA reverts). Read them with ws command `http/config`. Trusted
  proxies now: 172.30.32.0/23, 127.0.0.1/32, ::1/128, 10.10.11.102/32.
  ha.lan verified (200, trusted cert) and the user uses it.
- 2026-11-11 **https://proxmox.lan.stoneyshome.com without :8006**: on the
  Proxmox host, systemd unit `pve-https-redirect.service` adds
  `iptables -t nat -A PREROUTING -d 10.10.10.15/32 -p tcp --dport 443 -j
  REDIRECT --to-ports 8006` at boot (stop = rule removed). The firewall
  allow-list (8006 from Main + VPN) still applies; VMs on Servers stay
  blocked. Works from the user's PC. Does NOT work from VMs on this same
  host (e.g. Anthony's desktop on Main: the reply crosses the same bridge
  twice) - they use :8006.
- Not proxied (already trusted HTTPS via their public Cloudflare names):
  Nextcloud (overwritehost nextcloud.stoneyshome.com - a lan name would
  just redirect), n8n, Recipes, Finance.
- Clients that use their own encrypted DNS (Chrome "Use secure DNS" with a
  fixed provider, Android Private DNS, iCloud Private Relay) get NXDOMAIN for
  *.lan names: set them to "OS default"/automatic. The user's PC was on
  Chrome -> Cloudflare 1.1.1.1; fixed.
- Found: the old `mytlschallenge` resolver keeps failing to renew
  n8n.stoneyshome.com (TLS-ALPN can't work behind Cloudflare). Harmless (the
  public site uses the tunnel), but noisy: move the n8n router to `cfdns`
  when the n8n container may be restarted.

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

- **Two kinds of names** (2026-11-11, the user wants these names for SSH
  too): machine names `<host>.lan.stoneyshome.com` (proxmox, n8n,
  homeassistant, fileserver, finance, recipes, solar, cpushare, idrac)
  point at each server's own IP - for SSH/PuTTY, SMB and anything not
  proxied. Web names (e.g. `ha.`, `files.`, `n8n-ui.` ... `.lan.stoneyshome.com`)
  point at the Traefik proxy and carry the wildcard certificate. Proxmox's
  own ACME cert uses its machine name directly (no proxy). A browser on a
  machine name of a proxied service would hit the service's own (untrusted)
  cert - use the web name in browsers.

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
