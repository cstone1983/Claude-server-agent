# Scheduled server checks

Two scheduled tasks (Claude "routines"), created by the user in the Claude app
with the connectors **Server Agent Hub** and **Home Assistant** attached
(tasks created by Claude itself cannot carry connectors). Results go to the
Telegram bot "Stoneys servers" through `server-notify` on n8n.

Pre-approved hub rules (2026-11-11), so the runs never wait for approval:
- Proxmox: `/usr/local/sbin/health-report`
- n8n: `/usr/local/sbin/health-report` + any args (`daily` / `weekly`)
- all servers: `/usr/local/sbin/vm-health`
- n8n: `/usr/local/bin/server-notify` + any args

Scripts (all read-only, in `scripts/`): `health-report-proxmox`,
`health-report-n8n`, `vm-health`, `server-notify`, `eql-cli`.

| Task | When | Telegram |
|---|---|---|
| Daily server check | Sun-Fri 4:41 (America/New_York) | only on a major problem |
| Weekly server deep dive | Saturday 7:36 (America/New_York) | always a report |

## Prompt: Daily server check

```
Daily server health check for Chris's home lab (quick, READ-ONLY: change nothing, fix nothing).

Use the "Server Agent Hub" connector (servers: Proxmox, n8n, and the VMs) and the Home Assistant connector. These exact commands are pre-approved and run without approval:
1. list_servers — note any server that is offline.
2. run_command server="Proxmox" argv=["/usr/local/sbin/health-report"]   (takes 1-3 minutes; timeout_sec 300)
3. run_command server="n8n" argv=["/usr/local/sbin/health-report","daily"]   (timeout_sec 120)
4. run_command server="all" argv=["/usr/local/sbin/vm-health"]
5. Home Assistant: ha_get_overview(fields=["system_info"]) — state must be RUNNING.
Each report line starts with OK / WARN / ALERT / INFO.

Decide whether there is a MAJOR problem. Major = any ALERT line; a server/agent offline; a UniFi device not connected; 2 or more internet drops in 24 h; a backup failed or is older than 26 h; SAN critical, a SAN disk failed/not online (other than spare/foreign) or no iSCSI session; any disk/storage at 95% or more; Home Assistant not RUNNING; any NEW hardware log entry (machine check, memory, power, fan, temperature).
NOT major (ignore): package updates, reboot pending, certificates with more than 7 days left, INFO lines, the SAN "Power supply failure" (power supply 1 is left out on purpose), SAN cache battery charging, old exited containers on vm-fiance, VMs 110 and 9103 stopped, the Nov 8 2026 CPU machine-check entries, cabling warnings on switch ports 42 and 48 (known bad cables).

If there is a major problem: send ONE Telegram message with
run_command server="n8n" argv=["/usr/local/bin/server-notify","alert","Daily check","<plain text: one short line per problem, max 12 lines, each with the server and a suggested next step>"].
If there is no major problem: send NOTHING.
Finish with a 3-line summary in this session. Do not attempt fixes; do not run any other commands that change anything.
```

## Prompt: Weekly server deep dive

```
Weekly server deep dive for Chris's home lab (READ-ONLY: change nothing, fix nothing). Always send a report.

Use the "Server Agent Hub" connector and the Home Assistant connector. These exact commands are pre-approved and run without approval:
1. list_servers — offline servers, and agents showing UPDATE AVAILABLE.
2. run_command server="Proxmox" argv=["/usr/local/sbin/health-report"]   (1-3 minutes; timeout_sec 300)
3. run_command server="n8n" argv=["/usr/local/sbin/health-report","weekly"]   (timeout_sec 120)
4. run_command server="all" argv=["/usr/local/sbin/vm-health"]
5. Home Assistant (read-only tools): ha_get_overview(fields=["system_info"]); available updates (core, OS, add-ons, HACS) via the updates tool's list action; active repairs via ha_get_system_health(include="repairs"); last backup age if visible.
Each report line starts with OK / WARN / ALERT / INFO.

Known and accepted (mention once in a "known" line, don't list as problems): SAN "Power supply failure" (power supply 1 left out on purpose); VMs 110 and 9103 stopped; the Nov 8 2026 CPU machine-check entries; switch ports 42 and 48 bad cables (until fixed); old exited containers on vm-fiance.

Write ONE plain-text report (max ~40 lines), in this order:
- First line: overall status, e.g. "All good" or "3 items need attention".
- "Needs attention:" numbered items (1, 2, 3...), each: server - what - suggested fix (ALERTs first, then WARNs).
- "Updates available:" one line per server/device with counts (packages, UniFi firmware, HA core/add-ons, hub agents).
- "Fine:" one line listing the areas that are OK (hardware, SAN, backups, storage, certificates, internet, Wi-Fi, HA).
- Last line: "Reply in the Claude app (this task's session) with e.g. 'OK 1, 3' to have those fixed."
Send it with run_command server="n8n" argv=["/usr/local/bin/server-notify","<warn if anything needs attention, else ok>","Weekly server report","<the report>"].
Finish with the same report in this session. Do not attempt fixes.
```

## Creating them in the Claude app

claude.ai -> Code -> scheduled tasks (routines) -> New: name, the prompt
above, schedule (cron with time zone America/New_York), environment = the
same cloud environment as the Claude-server-agent sessions, and attach the
connectors **Server Agent Hub** and **Home Assistant**.
