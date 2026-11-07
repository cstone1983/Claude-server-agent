# Claude Server Agent — Project Plan

A small, self-contained agent runs on each Ubuntu server. Each agent dials
**out** to one **hub**. The hub is the only thing that's publicly reachable.
It exposes a single **MCP connector** for Claude (claude.ai web/mobile) and a
**REST API** for n8n, covering every server. Every action is checked on the
server that runs it, by a local **policy engine**: an allowlist, approval
tiers and an audit log.

---

## 1. Goals

- **One connector for the whole fleet.** You add one URL to claude.ai and log
  in once. New servers then show up automatically.
- **No inbound ports and no tunnel on the servers.** Agents only make an
  outbound connection to the hub.
- **One install command per server.** It fetches a single binary, sets up a
  systemd service and joins the hub with a join token.
- **Usable from claude.ai and the mobile app.** That means the hub must be a
  remote MCP endpoint over public HTTPS with OAuth.
- **Usable from n8n.** Plain HTTP+JSON with a bearer token.
- **Tiered control.**
  - Read-only actions run freely.
  - Anything on the allowlist runs freely.
  - Everything else, including arbitrary root commands, is *possible* but
    needs your approval.
  - "Approve and always allow" adds a rule to the allowlist, so you only
    approve the same thing once.
- **Everything is audited.**

Non-goals for v1: multi-user RBAC, Windows/macOS support, high-availability
hub.

---

## 2. Language and packaging: Go

Go is the most reliable choice for "one install script, many servers":

- It builds to a **single static binary**, with no Python or Node runtime to
  install or break on upgrade.
- It cross-compiles for `amd64` and `arm64` (for a Pi or ARM VPS) from one CI job.
- It has a small memory footprint (about 15–30 MB RSS), suited to a
  long-running systemd service.
- It has an official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`)
  that supports the Streamable HTTP transport.

Two binaries, both built from one Go module:
- **`csa-hub`** is the hub plus its admin CLI (`csa-hub approve`,
  `csa-hub join-token`, …).
- **`csa-agent`** is the per-server agent plus its local CLI
  (`csa-agent check`, `csa-agent audit`, …).

The hub installer installs both, and the hub machine enrolls itself as a
server.

---

## 3. Architecture: one hub, many agents

```
                         ┌──────────────── HUB (csa-hub) ─────────────────┐
 claude.ai / mobile ─────┤ https://agents.yourdomain/mcp   (one connector) │
   (one connector)       │ https://agents.yourdomain/api   (n8n REST)      │
 n8n ────────────────────┤ /oauth/*  /approve  server registry            │
 you (approve) ──────────┤                                                 │
                         └───────▲──────────────▲──────────────▲───────────┘
                                 │ outbound     │ outbound     │ outbound
                                 │ WSS (Ed25519)│ WSS (Ed25519)│ WSS (Ed25519)
                         ┌───────┴──┐    ┌──────┴───┐    ┌─────┴────┐
                         │ web01    │    │ db01     │    │ nas      │
                         │csa-agent │    │csa-agent │    │csa-agent │
                         │ policy + │    │ policy + │    │ policy + │
                         │ exec +   │    │ exec +   │    │ exec +   │
                         │ audit    │    │ audit    │    │ audit    │
                         └──────────┘    └──────────┘    └──────────┘
                     no open ports, no tunnel, no public IP needed
```

### How connecting to Claude works
1. In claude.ai, go to **Settings → Connectors → Add custom connector** and
   paste `https://agents.yourdomain/mcp`.
2. claude.ai opens the hub's login page. You enter the admin password and an
   authenticator-app code, once.
3. That's it. The same connector works on web, desktop and mobile, and covers
   **every** server that has joined the hub.

Claude sees one set of tools. Each tool takes a `server` argument:

```
list_servers()                          -> web01 (online), db01 (online), nas (offline)
run_command(server="db01", argv=["df","-h"])
service(server="web01", unit="nginx", action="restart")
```

Tools can also target `server="all"` or a tag such as `server="tag:web"`, for
example to run `system_info` across the whole fleet.

### Agents: outbound only
- `csa-agent` opens a persistent **WebSocket over TLS** to the hub. It
  reconnects automatically with backoff and sends heartbeats.
- Servers need no inbound ports, port forwarding, tunnel or public IP. They
  only need outbound HTTPS to the hub, so this also works behind NAT or CGNAT.
- **Enrollment:** run `csa-hub join-token` on the hub, then pass the token
  to the install script on the server. The agent creates its own key pair and
  registers the public key with the hub. From then on it authenticates every
  connection by signing a timestamp.
  - This uses Ed25519 rather than mTLS, because Cloudflare terminates TLS, so
    client certificates wouldn't reach the hub.
  - You can revoke a server with `csa-hub remove-server db01`.

### Where the policy lives
**Each agent enforces its own policy and keeps its own audit log.** The hub
only routes requests and relays approvals, and it can't override a server's
deny list, sensitive-path list or built-in denials. The hub *can* grant
approvals, so the hub is the trust anchor and must be protected:
- its config, which holds the password hash and TOTP secret, is
  unreadable through the agent's file tools, even with approval;
- it runs as a sandboxed, unprivileged user.

### Where the hub runs: exactly one public endpoint
Pick one of these. The installer supports all three:

| Option | Effort | Notes |
|--------|--------|-------|
| **A. One of your existing servers + one Cloudflare Tunnel** | Low | One tunnel total, not one per server. Free. Agents on the LAN connect to it directly. |
| **B. Small VPS ($4–6/mo) with Caddy + Let's Encrypt** | Low | Doesn't depend on your home network. Agents anywhere can reach it. |
| **C. Tailscale Funnel on the hub machine** | Lowest | No domain needed. URL looks like `hub.tailnet-xxxx.ts.net`. |

Something has to be publicly reachable because claude.ai connectors call your
MCP endpoint from Anthropic's cloud. A hub reduces that to one endpoint,
instead of one per server.

### Authentication
- **claude.ai connector:** OAuth 2.1, implemented once in the hub:
  - Endpoints: metadata discovery, dynamic client registration, PKCE, and
    authorize/token/refresh.
  - The login and consent page uses an admin password plus TOTP.
- **n8n:** scoped API tokens on the hub (`csa-hub token create n8n
  --scope read --servers tag:web`).
- **Agent ↔ hub:** an Ed25519 key per agent, registered at enrollment with a single-use join token.

---

## 4. Policy engine (the core)

Every request becomes an **Action**: tool name, argv or shell string, user,
working directory, caller (claude/n8n) and a reason. The engine classifies it
into one of three tiers:

| Tier | Examples | Behaviour |
|------|----------|-----------|
| **read** | `df`, `uptime`, `systemctl status`, `journalctl`, `docker ps`, read a file, list a directory | Runs immediately |
| **allowed** | Matches an allowlist rule, e.g. `systemctl restart nginx`, `apt-get update` | Runs immediately |
| **elevated** | Anything else: arbitrary root shell, file writes outside allowed paths, package install, reboot | **Held for approval** |

A **deny list** always wins over all three tiers. Examples: `rm -rf /`,
`mkfs` on a mounted disk, editing `csa`'s own policy file through Claude. You
can still edit the policy locally.

### Allowlist rules (`/etc/csa/policy.yaml`)
Rules match **argv**, not raw strings, so `systemctl restart nginx; rm -rf /`
can never match a rule:

```yaml
allow:
  - id: restart-web
    argv: ["systemctl", "restart|reload", "nginx|php8.3-fpm"]
    as: root
  - id: apt-refresh
    argv: ["apt-get", "update"]
    as: root
  - id: docker-compose-app
    argv: ["docker", "compose", "-f", "/srv/app/compose.yml", "*"]
  - id: write-app-config
    write_paths: ["/srv/app/config/**"]
deny:
  - argv: ["rm", "-rf", "/"]
  - write_paths: ["/etc/csa/**"]
```

Shell commands (`bash -c '…'`) are always **elevated**, unless you add an
explicit exact-match rule for that command.

### Approval flow
When an action is elevated:
1. The agent creates a pending approval with an ID, the full command, the
   stated reason, the caller and the server. The hub shows it in one shared
   approval queue for all servers.
2. It notifies you through any channels you configured:
   - **ntfy** or **Pushover** push notification with an approve link.
   - **Home Assistant** notification.
   - An **n8n webhook**, so you can route it anywhere.
3. You open the link, which goes to the hub's `/approve` page. You enter a
   TOTP code, which the agent verifies, and choose one of:
   - **Approve once**
   - **Approve and always allow.** This writes a rule; you can tweak its
     wildcards before saving. You also choose whether the rule applies to
     *this server*, *a tag* or *all servers*. The hub pushes rules to the
     agents, and each agent stores them in its local `policy.yaml`.
   - **Deny**
4. The tool call that asked waits about 45 s for your answer. If none comes,
   it returns `pending: <id>`, and Claude can call `check_approval` later.
5. Locally, `csa-hub approve <id>` / `csa-hub deny <id>` works over SSH on the
   hub.

The approval is **out-of-band** on purpose. Claude never approves its own
requests, and a stolen connector token alone cannot run root commands.

### Root
`csa` runs as root under systemd, so approved actions *can* do anything. Each
action declares which user it runs as (`as: root` or `as: deploy`). Without a
declaration it runs as an unprivileged `csa` user. Root is used only when a
rule or approval grants it.

### Audit log
- Format: append-only JSONL with timestamp, caller, token/client ID, action,
  tier, rule matched or approval ID, exit code, duration, and an output hash
  plus a truncated output.
- Also mirrored to journald.
- `csa-agent audit` and an MCP tool `audit_log` let Claude review what it has
  done.

---

## 5. MCP tools (v1)

Every tool except `list_servers` takes `server`: a name, `tag:<tag>` or
`all`. Fan-out calls return one result per server.

| Tool | Purpose | Default tier |
|------|---------|--------------|
| `list_servers` | Joined servers with online status, tags, OS and last-seen time | read |
| `system_info` | OS, kernel, uptime, CPU/mem/disk, IPs, pending updates, reboot-required | read |
| `run_command` | Run an argv command (optional `as`, `cwd`, `timeout`, `stdin`) | by policy |
| `run_shell` | Run a bash script/string | elevated unless exact rule |
| `start_job` / `job_status` / `job_output` / `cancel_job` | Long-running commands (upgrades, builds) without hitting MCP timeouts | by policy |
| `read_file` | Read file (size-capped, with offset/limit) | read (deny secrets like `/etc/shadow` unless approved) |
| `write_file` / `edit_file` | Write or patch a file, with automatic `.csa-bak` backup | allowed paths or elevated |
| `list_dir` | Directory listing | read |
| `service` | systemctl status/start/stop/restart/enable for a unit | status=read, rest by policy |
| `logs` | journalctl by unit/since/priority, tail files | read |
| `packages` | apt list/search/upgradable; install/remove/upgrade | list=read, rest by policy |
| `docker` | ps, logs, inspect, compose up/down/pull | read / by policy |
| `processes` | ps/top snapshot, kill | list=read, kill by policy |
| `network` | listening ports, connections, ping, curl a URL | read |
| `check_approval` | Poll a pending approval | read |
| `policy_view` / `request_rule` | Show rules; *propose* a new allowlist rule (still needs your approval) | read |
| `audit_log` | Recent audit entries | read |

The hub's REST API exposes the same tools: `POST /api/v1/tools/<name>` with
JSON args, including `server`. n8n then uses a plain HTTP Request node.

Output handling: stdout and stderr are captured separately and capped (for
example at 64 KB, with a hint to use `job_output` with an offset). ANSI codes
are stripped. Each result carries the exit code and duration.

---

## 6. Install

**Hub (once):**
```bash
curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
  | sudo bash -s -- hub --public-url https://agents.yourdomain [--cloudflare-token …|--caddy|--tailscale-funnel]
```
This step:
- generates the hub CA, the OAuth signing key, the admin password and a TOTP
  secret, and shows a QR code in the terminal;
- sets up the single public endpoint (option A, B or C from §3);
- prints the **connector URL** to paste into claude.ai, plus a first join
  token.

**Each server:**
```bash
curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
  | sudo bash -s -- agent --hub https://agents.yourdomain --join <token> --name web01 --tags web,prod
```
This step:
1. Checks for Ubuntu 22.04/24.04+ and detects the architecture (`amd64`/`arm64`).
2. Downloads `csa` from **GitHub Releases** and verifies its SHA256 and
   signature (cosign or minisign).
3. Installs the binary to `/usr/local/bin/csa` and creates the `csa` user,
   `/etc/csa/`, `/var/lib/csa/` and `/var/log/csa/`.
4. Writes a default `policy.yaml` with sensible read rules and an empty
   allowlist.
5. Enrolls with the hub: it generates a key pair, exchanges the join token for
   a client certificate, and receives the TOTP verification secret.
6. Installs the systemd unit `csa-agent.service` with restart-on-failure.
7. The server appears in `list_servers` within seconds. There's nothing to
   change in claude.ai.

Join tokens are single-use or time-limited. The hub's admin page can show a
ready-to-paste install command with a fresh token.

The installer is idempotent, so re-running it upgrades (a self-update command that
the hub can trigger fleet-wide is planned), and `install.sh uninstall`
removes everything.

---

## 7. Repository layout

```
cmd/csa/              main: hub, agent + admin subcommands
internal/hub/         agent registry, routing, fan-out, rule distribution
internal/proto/       agent<->hub WebSocket messages and handshake
internal/mcp/         MCP server + tool registrations (hub side)
internal/api/         REST handlers for n8n (hub side)
internal/oauth/       OAuth 2.1 AS (DCR, PKCE, tokens)
internal/policy/      rule parsing, argv matching, tiers, deny list
internal/approval/    pending store, notifiers (ntfy, pushover, HA, webhook), web UI
internal/exec/        runner (argv/shell, user switching, timeouts, output caps), jobs
internal/tools/       system_info, files, services, packages, docker, …
internal/audit/       JSONL writer + journald
install.sh
packaging/            systemd unit, default policy.yaml
.github/workflows/    test + goreleaser release (amd64/arm64, checksums, signing)
```

---

## 8. Milestones

> **Status:** M1–M4 and most of M5–M6 are built. That covers:
> - the hub, agent, policy engine and approvals;
> - OAuth, MCP and REST;
> - `install.sh` and the CI/release workflows.
>
> Still to do:
> - testing against claude.ai with a real tunnel;
> - publishing the first tagged release;
> - docker-specific tools;
> - an example n8n workflow.

1. **M1 – Hub ↔ agent core (LAN):**
   - `csa-hub` and `csa-agent` with the outbound WebSocket link, join
     tokens and Ed25519 auth;
   - the MCP endpoint on the hub with a static bearer token;
   - the tools `list_servers`, `system_info`, `run_command`, `read_file` and
     `list_dir`, at the read tier only;
   - the audit log.

   Testable from Claude Code CLI against two VMs.
2. **M2 – Policy engine:** the YAML allowlist and deny list, argv matching,
   tiers and the `csa` admin CLI. Unit tests for matching edge cases (shell
   metacharacters, path traversal, symlinks).
3. **M3 – Approvals:**
   - the shared approval queue on the hub and TOTP verification on the agent;
   - ntfy and webhook notifiers;
   - "always allow" with rule scope (server, tag or all) and rule push to
     agents;
   - `check_approval`.
4. **M4 – claude.ai connectivity:** the OAuth 2.1 server on the hub, the
   consent page with TOTP, and the single public endpoint (Cloudflare Tunnel,
   Caddy or Tailscale Funnel) in the installer. Verify end to end from
   claude.ai web and mobile.
5. **M5 – Full toolset and n8n:** services, logs, packages, docker, jobs,
   file write/edit, fan-out to `tag:`/`all`, the REST API, scoped tokens, and
   an example n8n workflow.
6. **M6 – Release and install:** goreleaser, signed releases, `install.sh
   hub|agent`, fleet-wide self-update, and docs.

---

## 9. Open questions

- **Where should the hub run?** Options: one existing server with one
  Cloudflare Tunnel, a small VPS, or Tailscale Funnel (§3). Do you have a
  domain on Cloudflare?
- **Notification channel for approvals:** ntfy, Pushover, Home Assistant
  push, or route everything through n8n?
- **Hub availability:** if the hub is down, Claude can't reach any server,
  although the servers themselves are unaffected. Is a single hub fine for
  v1?
- **Secrets on disk:** should `read_file` on things like `.env` files be
  elevated by default? The proposal is yes, through a configurable
  sensitive-path list.
