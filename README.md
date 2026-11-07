# Claude Server Agent

Let Claude (claude.ai on web, desktop and mobile) run commands on and manage
your Ubuntu servers through **one connector**. It has safety rails: an
allowlist, approvals on your phone, and an audit log on every server.

```
 Claude ──HTTPS──> Cloudflare ──tunnel──> csa-hub (one machine) <──outbound WSS── csa-agent (each server)
 n8n ────HTTPS──┘                          │                                      policy + audit
 you (approve) ─┘                          └── csa-agent (the hub machine is managed too)
```

- **csa-hub** is the only public endpoint. It runs on one machine behind a
  single Cloudflare Tunnel, and provides:
  - the MCP connector for Claude;
  - the OAuth login (password plus authenticator code);
  - the approval pages;
  - a REST API for n8n and other automations.
- **csa-agent** runs on every server, including the hub machine. It dials
  **out** to the hub, so servers need no open ports, tunnel or public IP. Each
  agent enforces its own policy and keeps its own audit log.

## How actions are handled

| Tier | What | Behaviour |
|------|------|-----------|
| read | Built-in read-only commands (`df`, `ps`, `systemctl status`, `journalctl`, `docker ps`, …), reading non-sensitive files | Runs immediately |
| allowed | Matches an allow rule in `/etc/csa/policy.yaml`, or an "always allow" rule from an approval | Runs immediately |
| elevated | Everything else, including root shell scripts, file writes, installs and restarts | **Needs your approval** |
| denied | Deny rules and built-ins (`rm -rf /`, stopping the agent, reading the hub's secrets) | Never runs |

When an action needs approval, Claude shows you a link. You open it, enter
your authenticator code and choose one of:
- **Approve**
- **Approve and always allow**, for this server, a tag or all servers. Next
  time that exact action runs without asking.
- **Deny**

Optionally, the hub also pushes approval requests to your phone through
[ntfy](https://ntfy.sh) or a webhook (n8n, Home Assistant).

Commands run through `run_command` without a shell, so
`systemctl restart nginx; rm -rf /` is just odd arguments to `systemctl`. It
can't sneak in a second command.

---

## Install

> **Full step-by-step guide: [INSTALL.md](INSTALL.md)**. It covers downloading the installer,
> the Cloudflare setup, saving credentials, verifying each step, upgrades and troubleshooting.

### 1. Create the Cloudflare Tunnel (once)

1. In the Cloudflare dashboard, go to **Zero Trust → Networks → Tunnels →
   Create a tunnel**. Choose **Cloudflared**, name it (e.g. `csa`) and copy
   the **token** from the install command it shows.
2. Under **Public Hostname**, add one:
   - **Subdomain/domain:** e.g. `agents.yourdomain.com`
   - **Service:** `HTTP` → `localhost:7420`
3. Do **not** put Cloudflare Access in front of this hostname. Claude
   couldn't get past an Access login screen, and the hub has its own login.

### 2. Install the hub (on the machine that will run the tunnel)

```bash
curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
  | sudo bash -s -- hub --public-url https://agents.yourdomain.com --cloudflare-token <TUNNEL_TOKEN>
```

This:
- installs `csa-hub` and `csa-agent`;
- prints your **admin password** and an **authenticator QR code**. Scan it
  with Google Authenticator, 1Password, Authy or similar;
- starts the hub and installs `cloudflared` with your tunnel;
- enrolls the hub machine as a server tagged `hub`.

### 3. Connect Claude

1. In Claude, go to **Settings → Connectors → Add custom connector**.
2. Enter the URL `https://agents.yourdomain.com/mcp`.
3. Log in with the admin password and an authenticator code. You only do this once.

The connector works on web, desktop and mobile.

### 4. Add your other servers

On the hub:
```bash
sudo csa-hub join-token            # add --uses 5 to enroll several servers with one token
```
It prints a ready-made command. Run it on each server, for example:
```bash
curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
  | sudo bash -s -- agent --hub https://agents.yourdomain.com --join <TOKEN> --name web01 --tags web,prod
```
The server appears in Claude right away (`list_servers`).

**Updates run only when you say so.** Every merge to `main` publishes a
release. The hub flags servers that are behind as **UPDATE AVAILABLE** in
`list_servers`. To update, tell Claude ("update all servers") or run
`sudo csa-update` on the machine. **Remove** with
`sudo bash install.sh uninstall [--purge]`.

> The installer downloads release binaries from GitHub Releases. If no
> release exists yet, it builds from source automatically: it fetches a
> temporary Go toolchain and needs about 1 minute. To publish a release, push
> a tag such as `v0.1.0`.

---

## Using it from Claude

Ask in plain language, for example:
- "What's the disk usage on all my servers?"
- "Why is nginx failing on web01? Check the logs."
- "Upgrade packages on tag:prod."

Tools Claude gets:

| Tool | What it does |
|------|--------------|
| `list_servers` | Servers, online status, tags, OS |
| `system_info` | OS, uptime, load, memory, disks, IPs, updates, failed units |
| `run_command` | Run an argv command (optionally as another user) |
| `run_shell` | Run a bash script; always needs approval unless the exact script is allowlisted |
| `read_file` / `list_dir` | Read files and directories (sensitive files need approval) |
| `write_file` / `edit_file` | Change files; each change saves a `.csa-bak` backup |
| `service` | systemctl status/start/stop/restart/reload/enable/disable |
| `logs` | journalctl for the system or one unit, or tail a log file |
| `start_job` / `job_status` / `cancel_job` | Long-running commands in the background |
| `check_approval` | Finish an action once you've approved it |
| `update_server` | Check for, or (when you say so) install, a newer release |
| `policy_view` / `audit_log` | See a server's rules and history |

Every tool takes `server`: a name, `tag:<tag>` or `all`.

## Policy (`/etc/csa/policy.yaml` on each server)

```yaml
allow:
  - id: restart-web
    argv: ["systemctl", "restart|reload", "nginx|php8.3-fpm"]
  - id: app-compose
    argv: ["docker", "compose", "-f", "/srv/app/compose.yml", "**"]
  - id: deploy-as-user
    argv: ["git", "-C", "/srv/app", "pull"]
    as: deploy
  - id: app-config
    write_paths: ["/srv/app/config/**"]
deny:
  - argv: ["reboot", "**"]
sensitive_paths: ["/srv/app/secrets/**"]
```

Pattern syntax for each argv element:

| Pattern | Matches |
|---------|---------|
| `a\|b` | Any of the alternatives |
| `*` | Any single argument (glob) |
| `=literal` | Exactly that text |
| `**` | Any remaining arguments (last element only) |

Useful commands on each server:
- `sudo csa-agent check -- <command>`: shows which tier a command falls into.
- `systemctl reload csa-agent`: applies your policy edits.
- `sudo csa-agent audit`: shows the server's audit log.

The hub can **add** allow rules ("always allow"), but it can never change
your local `policy.yaml` or override its deny rules. To ignore hub rules on a
server, set `accept_hub_rules: false` in that server's `policy.yaml`.

## n8n and automations

```bash
sudo csa-hub token create n8n --scope read                  # read-only
sudo csa-hub token create deploy-bot --scope full --servers tag:web
```

From n8n, use an **HTTP Request** node:
- `POST https://agents.yourdomain.com/api/v1/tools/run_command`
- Header: `Authorization: Bearer <token>`
- Body: `{"server": "web01", "argv": ["systemctl", "is-active", "nginx"]}`

Other endpoints: `GET /api/v1/servers`, `GET /api/v1/tools` (schemas) and
`GET /api/v1/approvals/<id>`.

For approval notifications, add one of these to `/etc/csa-hub/hub.yaml`, then
run `systemctl restart csa-hub`:
```yaml
notify:
  ntfy_url: https://ntfy.sh/<a-long-random-topic>
  webhook_url: https://n8n.yourdomain.com/webhook/csa-approvals
```

## Hub admin CLI (run as root on the hub)

```
csa-hub servers | rename <server> <new-name> | tag <server> <tags> | remove-server <server>
csa-hub join-token [--ttl 60] [--uses 1] [--tags a,b]
csa-hub approvals | approve <id> [--always --scope server|tag:x|all] | deny <id>
csa-hub rules | rule-delete <id>
csa-hub token create|list|revoke
csa-hub sessions | revoke-sessions          # log out every Claude connector
csa-hub set-password | reset-totp
csa-hub call <tool> '<json>'                # e.g. csa-hub call list_servers
```

## Security notes

- **Exposure:** only the hub is reachable from the internet, and only through
  the tunnel. It listens on `127.0.0.1:7420`.
- **Claude's login:** OAuth 2.1 with PKCE. The login needs the admin password
  *and* an authenticator code, and failed attempts are rate-limited.
- **Connector registration:** only redirect URLs on `claude.ai`/`claude.com`
  (and localhost, for Claude Code) can register. Change this with
  `allowed_redirect_hosts` in `hub.yaml`.
- **Secrets on the hub:** tokens are stored only as hashes. The hub service
  runs as an unprivileged, sandboxed `csa-hub` user.
- **Agent authentication:** each agent has an Ed25519 key, created when it
  enrolls. Join tokens are single-use and expire.
- **Agents run as root.** Anything you approve can do anything on that
  server, so read approval requests before approving. Approving a root shell
  on the **hub machine** gives full control of the hub too.
- **Audit logs:** `/var/log/csa/audit.jsonl` on every server, and
  `/var/log/csa-hub/audit.jsonl` on the hub.

## Development

```bash
go test -race ./...                          # includes an end-to-end test: OAuth → MCP → approval → rule
go build ./cmd/csa-hub ./cmd/csa-agent
```

The design notes are in [PLAN.md](PLAN.md).
