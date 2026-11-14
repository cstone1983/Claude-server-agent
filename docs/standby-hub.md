# Standby hub and agent fallback

Goal: if the hub's VM reboots or crashes, Claude, the dashboard and the agents keep working
through a second hub on another server, and everything moves back by itself afterwards.

## How it works

- **Primary** (`replica.role: primary`) works as before. It also listens on its LAN address
  (`listen_also`) and answers `/replica/state` and `/replica/merge` (LAN only, shared secret).
- **Standby** (`replica.role: standby`) copies the primary's whole state every 15 s and, while the
  primary answers, **forwards every request to it**. So both hubs can sit behind the same
  hostname and Cloudflare can send traffic to either: there is still only one hub doing the work.
- Primary silent for `takeover_after_sec` (default 90 s): the standby **serves by itself** from its
  copy: same servers, keys, logins, rules, hands-off mode (and its end time).
- Primary back: the standby first **merges** what happened meanwhile into it (new Claude logins and
  refreshed tokens, new rules, enrolled servers, dashboard devices, hands-off on/off, whichever
  changed last), then drops its agent connections (they reconnect and get forwarded to the primary)
  and forwards again.
- Not carried over: approvals that were pending in memory at the moment of a switch (ask again), and
  deletions made on the standby during an outage (a rule deleted there comes back; delete it again).
- **Agents**: `fallback_hub_urls` in `/etc/csa/agent.yaml` lists more addresses to try, in order,
  when `hub_url` fails (e.g. Cloudflare or the internet is down). On a fallback, an agent checks
  `hub_url` every 5 minutes and moves back.
- **Running commands survive a dropped connection**: the agent keeps running and re-sends the result
  when it reconnects (to whichever hub); the hub waits up to 3 minutes for it.
- On LAN addresses (IP, not the public name) a hub serves only `/agent/connect`, `/healthz` and
  `/replica/*`; the UI, OAuth and MCP are only on the public name.
- `/healthz` says `ok primary`, `ok standby forwarding to primary` or `ok standby serving (...)`.

## Setup (once)

1. **Tunnel.** Today `agents.stoneyshome.com` is a route on n8n's tunnel, which also carries
   `n8n.stoneyshome.com`, so it can't simply be run on a second server. In Cloudflare
   (Zero Trust → Networks → Tunnels): create a tunnel `csa-hub` with one public hostname
   `agents.stoneyshome.com → http://localhost:7420`, and remove that hostname from the n8n tunnel.
   Run a connector for `csa-hub` on **both** n8n and the standby (a second cloudflared service on
   n8n, e.g. `cloudflared-hub.service`). Both stay up all the time.
2. **Secret.** Same random value on both hubs, root-only:
   `openssl rand -hex 32 > /etc/csa-hub/replica.secret; chown csa-hub /etc/csa-hub/replica.secret; chmod 600 ...`
   (copy it to the standby without printing it).
3. **Primary** `/etc/csa-hub/hub.yaml`:
   ```yaml
   listen_also: ["10.10.11.102:7420"]
   replica:
     role: primary
     secret_file: /etc/csa-hub/replica.secret
     trusted_proxies: ["<standby IP>"]
   ```
4. **Standby**: install csa-hub (same version), copy hub.yaml from the primary (it holds the
   dashboard password hash and TOTP secret, so logging in works on both; root-to-root, never in chat),
   then:
   ```yaml
   listen: 127.0.0.1:7420
   listen_also: ["<standby IP>:7420"]
   replica:
     role: standby
     peer_url: http://10.10.11.102:7420
     secret_file: /etc/csa-hub/replica.secret
   ```
5. **Firewall**: allow TCP 7420 to both hubs from the `servers` ipset (agents' fallback, and the
   standby ↔ primary).
6. **Agents** (`/etc/csa/agent.yaml`, then restart csa-agent):
   ```yaml
   fallback_hub_urls:
     - http://10.10.11.102:7420
     - http://<standby IP>:7420
   ```
   The HA add-on agent gets the same list.

## Test

1. `curl http://<standby>:7420/healthz` → `ok standby forwarding to primary`.
2. Stop csa-hub on n8n: after ~90 s the standby says `serving`; list_servers through Claude still works
   (servers reconnect within a minute).
3. Start it again: within ~15 s the standby merges and forwards; the n8n audit log shows `replica_merged`.

## Caveats

- LAN fallback is plain HTTP: commands and output are not encrypted on the LAN while an agent uses
  it (only when the public path is down). Agents still prove who they are with their signed key.
- If only the LAN link between the two hubs breaks while both run, the standby takes over too and
  servers may split between them until it heals. Both are VMs on the same host and bridge, so this
  is unlikely; it ends by itself when the link returns.
