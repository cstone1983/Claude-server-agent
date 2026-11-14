# Claude Server Agent (Home Assistant add-on)

Connects this Home Assistant to your Claude Server Agent hub as one more
server, with the same approvals, allowlists and hands-off mode.

## Setup
1. On the hub: `sudo csa-hub join-token` (valid 60 minutes, one use).
2. Paste the token into this add-on's **join_token** option, keep
   `hub_url` and `name` (default `home-assistant`), save, then **Start**.
3. The log says "Enrolled"; the server appears in the hub. The token is now
   used up and can be cleared.

## What it can do
Commands run inside this add-on's container. It manages Home Assistant
through the `ha` CLI (Supervisor API: updates, backups, add-ons, Core, OS)
and can read and edit files in `/config`. Read-only `ha ... info/list`
commands run freely; everything else needs approval. Shutting the host down
is blocked. Policy: `/data/etc/policy.yaml` inside the add-on.
