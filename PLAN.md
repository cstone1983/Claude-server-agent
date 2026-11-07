# Claude Server Agent — Project Plan

A small, self-contained agent that runs on each Ubuntu server. It exposes an
**MCP server** so Claude (claude.ai web/mobile) can run commands and manage
the box, plus a **REST API** for n8n automations. Every action passes through
one **policy engine**: an allowlist, approval tiers and an audit log.

---

## 1. Goals

- **One install command per server.** It fetches a single binary, sets up a
  systemd service and prints the connector URL to paste into claude.ai.
- **Usable from claude.ai and the mobile app.** That means a remote MCP
  endpoint over public HTTPS with OAuth.
- **Usable from n8n.** Plain HTTP+JSON with a bearer token.
- **Tiered control.**
  - Read-only actions run freely.
  - Anything on the allowlist runs freely.
  - Everything else, including arbitrary root commands, is *possible* but
    needs your approval.
  - "Approve and always allow" adds a rule to the allowlist, so you only
    approve the same thing once.
- **Everything is audited.**

Non-goals for v1: a central hub, multi-user RBAC, Windows/macOS support.

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

Binary name: **`csa`** (claude-server-agent). The same binary is the daemon
(`csa serve`) and the admin CLI (`csa approve`, `csa allow`, `csa audit`, …).

---

## 3. Architecture (per server)

```
 claude.ai / mobile ──HTTPS──┐                     ┌──> exec (argv / shell)
                             │   Cloudflare Tunnel │
 n8n ────────────HTTPS───────┼──> (cloudflared) ──>│ csa serve (127.0.0.1:7420)
                             │   no inbound ports  │   ├─ /mcp     MCP Streamable HTTP
 you (approve) ──push/link───┘                     │   ├─ /api/v1  REST for n8n
                                                   │   ├─ /oauth/* OAuth 2.1 for claude.ai
                                                   │   └─ /approve approval pages
                                                   │
                                                   ├─ Policy engine (tiers + allowlist)
                                                   ├─ Job runner (long-running commands)
                                                   └─ Audit log (/var/log/csa/audit.jsonl)
```

### Exposure: Cloudflare Tunnel (recommended)
- `csa` binds to **localhost only**. `cloudflared` makes an outbound
  connection, so the firewall has no open ports.
- Each box gets a hostname such as `web01.agents.yourdomain.com`.
- Alternatives the installer also supports:
  - Tailscale Funnel.
  - Your own reverse proxy (Caddy/nginx) with Let's Encrypt.
  - LAN-only mode for n8n on the same network.

### Authentication
- **claude.ai connector:** this needs OAuth 2.1. `csa` embeds a minimal
  authorization server:
  - Endpoints: metadata discovery, dynamic client registration, PKCE, and
    authorize/token/refresh.
  - The consent screen is protected by an admin password and TOTP, both
    generated at install time.
  - You log in once per server when you add the connector.
- **n8n:** a static API token, created with `csa token create --name n8n`.
  Each token can have its own scope (for example `read` or `allowlist`), so
  n8n can be limited to less power than Claude.

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
1. `csa` creates a pending approval with an ID, the full command, the stated
   reason and the caller.
2. It notifies you through any channels you configured:
   - **ntfy** or **Pushover** push notification with an approve link.
   - **Home Assistant** notification.
   - An **n8n webhook**, so you can route it anywhere.
3. You open the link, which goes to the `/approve` page behind the same
   TOTP/password. You choose one of:
   - **Approve once**
   - **Approve and always allow.** This writes a rule; you can tweak its
     wildcards before saving.
   - **Deny**
4. The tool call that asked waits about 60 s for your answer. If none comes,
   it returns `pending: <id>`, and Claude can call `check_approval` later.
5. Locally, `csa approve <id>` / `csa deny <id>` works over SSH.

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
- `csa audit tail` and an MCP tool `audit_log` let Claude review what it has
  done.

---

## 5. MCP tools (v1)

| Tool | Purpose | Default tier |
|------|---------|--------------|
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

The REST API exposes the same tools: `POST /api/v1/tools/<name>` with JSON
args. n8n then uses a plain HTTP Request node.

Output handling: stdout and stderr are captured separately and capped (for
example at 64 KB, with a hint to use `job_output` with an offset). ANSI codes
are stripped. Each result carries the exit code and duration.

---

## 6. Install script

```bash
curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
  | sudo bash -s -- --name web01 --tunnel-token <cloudflare-tunnel-token>
```

What it does (idempotent, so re-running it upgrades the agent):
1. Checks for Ubuntu 22.04/24.04+ and detects the architecture (`amd64`/`arm64`).
2. Downloads `csa` from **GitHub Releases** and verifies its SHA256 and
   signature (cosign or minisign).
3. Installs the binary to `/usr/local/bin/csa` and creates the `csa` user,
   `/etc/csa/`, `/var/lib/csa/` and `/var/log/csa/`.
4. Writes a default `policy.yaml` with sensible read rules and an empty
   allowlist.
5. Generates the OAuth signing key, the admin password and a TOTP secret. It
   shows a QR code in the terminal.
6. Installs the systemd unit `csa.service` with restart-on-failure.
7. Optionally installs `cloudflared` and registers the tunnel with the given
   token.
8. Prints the **connector URL** (`https://web01.agents.example.com/mcp`) and
   the first n8n token.

Other commands: `csa update` self-updates from Releases. `csa uninstall`
cleanly removes the agent.

---

## 7. Repository layout

```
cmd/csa/              main: serve + admin subcommands
internal/mcp/         MCP server + tool registrations
internal/api/         REST handlers for n8n
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

1. **M1 – Core loop (LAN):** `csa serve` with the MCP Streamable HTTP
   endpoint, a static bearer token, and the tools `system_info`,
   `run_command`, `read_file` and `list_dir`. Read tier only, plus the audit
   log. Testable from Claude Code CLI.
2. **M2 – Policy engine:** the YAML allowlist and deny list, argv matching,
   tiers and the `csa` admin CLI. Unit tests for matching edge cases (shell
   metacharacters, path traversal, symlinks).
3. **M3 – Approvals:** a pending store, ntfy, the webhook notifier, the
   approval web page, "always allow" writing rules, and `check_approval`.
4. **M4 – claude.ai connectivity:** the OAuth 2.1 server, the consent page
   with TOTP, and Cloudflare Tunnel in the installer. Verify end to end from
   claude.ai web and mobile.
5. **M5 – Full toolset and n8n:** services, logs, packages, docker, jobs,
   file write/edit, REST API, scoped tokens, and an example n8n workflow.
6. **M6 – Release and install:** goreleaser, signed releases, `install.sh`,
   `csa update`, and docs.

---

## 9. Open questions

- **Domain:** do you have a domain on Cloudflare for the tunnels? If not,
  Tailscale Funnel is the fallback.
- **Notification channel for approvals:** ntfy, Pushover, Home Assistant
  push, or route everything through n8n?
- **How many servers?** With one connector per server, claude.ai lists each
  as a separate connector. For a larger fleet, a later optional "hub mode"
  could aggregate them behind one connector without changing the agent.
- **Secrets on disk:** should `read_file` on things like `.env` files be
  elevated by default? The proposal is yes, through a configurable
  sensitive-path list.
