# Installation guide

This guide sets up Claude Server Agent from scratch:

| Part | What you do | Where |
|------|-------------|-------|
| 0 | Check prerequisites | — |
| 1 | Create a Cloudflare Tunnel | Cloudflare dashboard |
| 2 | Install the **hub** | One Ubuntu machine (the hub also manages itself) |
| 3 | Connect **Claude** to the hub | claude.ai |
| 4 | Install the **agent** | Every other server |
| 5 | Optional extras: notifications, n8n, allow rules | — |
| 6 | Upgrade, uninstall, troubleshooting | — |

**`install.sh` is the only file you need to download.** It fetches everything
else from GitHub by itself:
- the `csa-hub` / `csa-agent` binaries from GitHub Releases, verified by
  SHA-256 checksum;
- or, if no release is published, the source code plus a temporary Go
  toolchain, which it builds automatically;
- `cloudflared`, when you pass a tunnel token.

It also writes the systemd services and a default policy.

---

## Part 0 — Prerequisites

- Ubuntu 22.04 or 24.04 (Debian works too), x86-64 or ARM64, with systemd.
- `sudo` access on every machine.
- `curl` to download the installer. Ubuntu **Desktop** doesn't include it, so
  install it first with `sudo apt-get install -y curl`. Alternatively, use
  `wget -qO install.sh <url>` (wget is preinstalled) in place of
  `curl -fsSL -o install.sh <url>`. The command printed by
  `csa-hub join-token` installs curl for you.
- Outbound HTTPS from every machine to `github.com` and
  `objects.githubusercontent.com`. Source builds also need `go.dev` and
  `dl.google.com`.
- **Hub only:** a domain managed in Cloudflare (the free plan is fine).
- An authenticator app on your phone, such as Google Authenticator,
  1Password, Authy or Bitwarden.

> **Before the code is merged to `main`:** the commands below download from
> `main`. Until this project's branch is merged, make two substitutions in
> every command:
> - replace `/main/` in the URL with `/ccr-59230fbc-jw6beu/`;
> - add `--ref ccr-59230fbc-jw6beu` to the installer's arguments.

---

## Part 1 — Create the Cloudflare Tunnel (once)

The tunnel gives the hub a public HTTPS address without opening any ports.

1. Log in at <https://one.dash.cloudflare.com> and go to **Networks →
   Tunnels → Create a tunnel**.
2. Choose **Cloudflared**, name it `csa` and click **Save tunnel**.
3. The next page shows an install command containing a long token (the text
   after `--token` or `service install`). **Copy only the token.** You'll
   pass it to the hub installer, which installs cloudflared for you, so skip
   Cloudflare's own install steps.
4. Click **Next** to reach **Public Hostname**, then add:
   - **Subdomain:** `agents` (any name works)
   - **Domain:** your domain, e.g. `yourdomain.com`
   - **Service type:** `HTTP`
   - **URL:** `localhost:7420`
5. Save. Your hub URL is now `https://agents.yourdomain.com`.

> ⚠️ Do **not** put a Cloudflare Access policy on this hostname. Claude can't
> get past an Access login page, and the hub has its own login (password plus
> authenticator code).

### Already have a tunnel? Just add a route

If you already run cloudflared (for example for n8n), you don't need a new tunnel.

1. Go to **Networks → Tunnels → your tunnel → Routes → Add route → Published application**.
2. Set the hostname to e.g. `agents.yourdomain.com`.
3. Set the service type to **HTTP**. Set the URL by where cloudflared runs:
   - **On the hub machine:** `localhost:7420`
   - **On another machine:** `<hub-LAN-IP>:7420`, e.g. `10.10.1.50:7420`
4. When you install the hub in Part 2, leave out `--cloudflare-token`. If
   cloudflared runs on another machine, add `--listen 0.0.0.0:7420`, then
   firewall port 7420 so only the cloudflared machine can reach it:
   ```bash
   sudo ufw allow from <CLOUDFLARED-IP> to any port 7420 proto tcp
   sudo ufw deny 7420/tcp
   ```

---

## Part 2 — Install the hub

Run this on the one machine that will be the hub. It can be one of your
existing servers.

### 2.1 Download the installer

```bash
cd ~
curl -fsSL -o install.sh https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh
# or, without curl:
# wget -O install.sh https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh
chmod +x install.sh
less install.sh        # optional: read what it will do
```

### 2.2 Run it in hub mode

```bash
sudo ./install.sh hub \
  --public-url https://agents.yourdomain.com \
  --cloudflare-token <TUNNEL_TOKEN_FROM_PART_1>
```

Optional flags:

| Flag | Meaning |
|------|---------|
| `--name NAME` | Name of the hub machine as a server (default: its hostname) |
| `--tags a,b` | Extra tags for the hub machine (it always gets `hub`) |
| `--listen 0.0.0.0:7420` | Accept connections from the LAN (cloudflared on another machine) |
| `--no-local-agent` | Don't manage the hub machine itself |
| `--version v0.1.0` | Install a specific release instead of the latest |
| `--from-source` | Always build from source |
| `--ref BRANCH` | Branch to build from, when building from source |

One-liner alternative (download and run in one step):
```bash
curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
  | sudo bash -s -- hub --public-url https://agents.yourdomain.com --cloudflare-token <TOKEN>
```

### 2.3 Save your credentials (important)

On the first run, the installer asks you to **choose an admin password**
(at least 10 characters; type it twice). Press Enter instead to have one
generated and printed. Then it shows:

1. **Admin password.** The one you chose, or the generated one, shown only
   once. Save it in your password manager.
2. **QR code.** Scan it with your authenticator app; the manual secret is
   printed below the code. The app then shows a 6-digit code, which you need
   to log in and to approve actions.

**Lost them, or want your own password?** On the hub:
```bash
sudo csa-hub set-password   # asks for a new password twice and restarts the hub
sudo csa-hub reset-totp     # new authenticator QR code (re-scan it in your app)
```
Changing the password doesn't disconnect Claude; it's only needed for new
logins and the approvals dashboard.

### 2.4 What the installer did

- Installed `/usr/local/bin/csa-hub` and `/usr/local/bin/csa-agent`.
- Wrote `/etc/csa-hub/hub.yaml`, the hub config. It holds the password hash
  and authenticator secret, and its mode is `0600`.
- Started the `csa-hub` service as the unprivileged user `csa-hub`. The
  service listens on `127.0.0.1:7420`.
- Installed `cloudflared` and connected it to your tunnel.
- Enrolled the hub machine as a server tagged `hub`. It got the `csa-agent`
  service and the policy file `/etc/csa/policy.yaml`.

### 2.5 Check it

```bash
systemctl status csa-hub csa-agent cloudflared --no-pager
curl -s http://127.0.0.1:7420/healthz                                   # → ok
curl -s https://agents.yourdomain.com/.well-known/oauth-authorization-server | head -5
sudo csa-hub servers                                                    # the hub machine, "online": true
```

If the `https://` check fails but `healthz` works, the tunnel's public
hostname isn't set up correctly. Recheck Part 1, step 4.

---

## Part 3 — Connect Claude

1. Open Claude (web or desktop) and go to **Settings → Connectors → Add
   custom connector**.
2. **Name:** `Servers` (anything you like). **URL:**
   `https://agents.yourdomain.com/mcp`
3. Click **Add**, then **Connect**. A hub page opens. Enter the **admin
   password** and the current **authenticator code**, then click **Allow
   access**.
4. Done. The connector also appears in the mobile app. Try asking:
   *"List my servers and show disk usage on each."*

To log out every Claude session later, run `sudo csa-hub revoke-sessions` on
the hub.

---

## Part 4 — Install the agent on each server

### 4.1 Create a join token (on the hub)

```bash
sudo csa-hub join-token                     # one server, valid for 60 minutes
sudo csa-hub join-token --uses 10 --ttl 240 # several servers with one token
sudo csa-hub join-token --tags prod         # every server that joins gets tag "prod"
```

It prints the token and a ready-to-paste install command.

### 4.2 Download and run the installer (on the server)

```bash
cd ~
curl -fsSL -o install.sh https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh
chmod +x install.sh
sudo ./install.sh agent \
  --hub https://agents.yourdomain.com \
  --join <JOIN_TOKEN> \
  --name web01 \
  --tags web,prod
```

- Without `--name`, the installer **asks** for a friendly name (default: the
  hostname) and optional tags just before it connects to the hub. If the name
  is already used by another server, it asks again. To take over that name
  deliberately, add `--replace`.
- `--name` / `--tags` on the command line skip the questions. Tags let you
  target groups of servers, e.g. `server: "tag:web"`.

One-liner alternative. This is what `csa-hub join-token` prints; paste it as
a single line. It installs curl first if it's missing:
```bash
S=$(command -v sudo); command -v curl >/dev/null || { $S apt-get update -qq; $S apt-get install -y -qq curl; }; curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh | $S bash -s -- agent --hub https://agents.yourdomain.com --join <JOIN_TOKEN>
```

### 4.3 What the installer did

- Installed `/usr/local/bin/csa-agent` and the `csa-agent` systemd service.
  The service runs as root, so it can manage the server.
- Created an Ed25519 identity key in `/etc/csa/agent.key` and registered it
  with the hub. The join token is now used up.
- Wrote `/etc/csa/agent.yaml`, the connection settings.
- Wrote `/etc/csa/policy.yaml`, this server's rules, only if the file didn't
  already exist.

### 4.4 Check it

```bash
systemctl status csa-agent --no-pager
sudo journalctl -u csa-agent -n 20 --no-pager     # look for "connected to hub"
```
Then run `sudo csa-hub servers` on the hub, or ask Claude to list servers.

Repeat Part 4 for each server.

### 4.5 Proxmox VE hosts

Proxmox has no `sudo`, so run the join command in a **root** shell: the web
UI's node **>_ Shell**, or `su -`. The hub's join command uses `sudo` only
when it exists.

The installer detects Proxmox and writes a **careful-mode** policy:
- Read-only commands (`qm list`, `qm config`, `pvesm status`, `pvesh get`, …)
  run freely.
- **Every change needs your approval, every time.**
- "Always allow" rules from the hub are ignored on this host.
- Nothing is blocked outright. To block something for good, add a `deny` rule
  to `/etc/csa/policy.yaml` and run `systemctl reload csa-agent`.

---

## Part 5 — Optional setup

### 5.1 Approval notifications on your phone

When Claude needs approval, it shows you the link in chat. To also get a
push notification, edit `/etc/csa-hub/hub.yaml` on the hub:

```yaml
notify:
  ntfy_url: https://ntfy.sh/<long-random-topic-name>   # subscribe to this topic in the ntfy app
  # ntfy_token: tk_...                                 # if your ntfy server requires auth
  webhook_url: https://n8n.yourdomain.com/webhook/csa  # JSON POST for n8n / Home Assistant
```
Then run `sudo systemctl restart csa-hub`.

You can also approve from a terminal on the hub:
```bash
sudo csa-hub approvals
sudo csa-hub approve <id> [--always --scope server|tag:web|all]
sudo csa-hub deny <id>
```

### 5.2 Approvals dashboard

Open `https://agents.yourdomain.com/approvals` and log in once with the
password and an authenticator code. You stay logged in for 24 hours, even across
hub restarts and updates.
- Approve or deny everything pending in one go: identical commands on
  several servers are grouped.
- Choose **Approve + always allow** to whitelist the command on all
  servers.
- Manage whitelisted commands at `/rules`.
- Claude can also propose rules (`request_rule`). They appear on the same
  page for you to approve.

### 5.3 Let common actions run without approval

On a server, edit `/etc/csa/policy.yaml`, for example:
```yaml
allow:
  - id: restart-web
    argv: ["systemctl", "restart|reload", "nginx"]
  - id: apt-refresh
    argv: ["apt-get", "update"]
```
Then apply and check it:
```bash
sudo systemctl reload csa-agent
sudo csa-agent check -- systemctl restart nginx  # → tier: allowed
```
Or click **"Always allow"** when approving. That adds the rule from the hub,
and `sudo csa-hub rules` lists those rules.

### 5.4 n8n / automations

On the hub:
```bash
sudo csa-hub token create n8n --scope read                       # read-only
sudo csa-hub token create deploy --scope full --servers tag:web  # can request approvals, web servers only
```

In n8n, add an **HTTP Request** node:
- **Method:** `POST`
- **URL:** `https://agents.yourdomain.com/api/v1/tools/run_command`
- **Header:** `Authorization: Bearer <token>`
- **Body (JSON):** `{"server": "web01", "argv": ["systemctl", "is-active", "nginx"]}`

---

## Part 6 — Upgrade, uninstall, troubleshooting

### Updates (only when you say so)

Every change merged into `main` on GitHub is tested and published as a new
release (`v0.1.N`). **Nothing updates automatically.** The hub checks GitHub
once an hour. When a server runs an older version, `list_servers` shows
**UPDATE AVAILABLE**, and Claude will mention it to you.

To update, use either of these:
- **Ask Claude**, e.g. *"update web01"* or *"update all servers"*. Claude
  runs `update_server`. It installs only official releases and is told to do
  so only after you say to. The update runs in the background, and the agent
  reconnects within about a minute. Updating the hub machine briefly
  disconnects the connector.
- **On the machine itself:**
  ```bash
  sudo csa-update --check      # is an update available?
  sudo csa-update              # update now (verifies checksums, keeps config, restarts services)
  ```

To stop Claude from triggering updates on a server, add
`disable_remote_update: true` to that server's `/etc/csa/policy.yaml`.
Claude can still check for updates there.

Servers installed before update support existed need the installer re-run
once to get `csa-update`:
```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh
sudo ./install.sh hub      # on the hub (keeps the config)
sudo ./install.sh agent    # on servers (keeps enrollment and policy)
```

### Friendly server names

The name Claude uses for a server defaults to its hostname. To rename it,
run this on the hub:
```bash
sudo csa-hub servers                 # current names
sudo csa-hub rename Ubuntu n8n       # takes effect immediately; no reinstall
```
"Always allow" rules that were scoped to the old name move to the new one.

### Move a server to another hub, or re-enroll it

```bash
sudo ./install.sh agent --hub https://agents.yourdomain.com --join <NEW_TOKEN> --name web01
```

### Uninstall

```bash
sudo ./install.sh uninstall           # removes services and binaries, keeps config/logs
sudo ./install.sh uninstall --purge   # removes everything
```
On the hub, run `sudo csa-hub remove-server <name>` for servers you
uninstalled.

### Troubleshooting

| Symptom | Check |
|---------|-------|
| Installer: "no release download available" | Normal before the first release: it builds from source instead. Before the merge to `main`, add `--ref <branch>`. |
| Hub won't start | `sudo journalctl -u csa-hub -n 50 --no-pager` |
| Claude says it can't connect | Run `curl https://agents.yourdomain.com/.well-known/oauth-protected-resource`. It must return JSON. Check the tunnel hostname and that no Cloudflare Access policy applies. |
| "wrong password or code" | Check your phone's clock (codes are time-based). After 10 failures there's a 15-minute lockout. |
| Agent shows offline | `sudo journalctl -u csa-agent -n 50`. A `401` means re-enroll with a new join token. "stale timestamp" means fix the server clock (`timedatectl`). |
| Command unexpectedly needs approval | `sudo csa-agent check -- <command>` shows the tier and the rule that would allow it. |
| What did Claude do? | `sudo csa-agent audit -n 50` on a server, `/var/log/csa-hub/audit.jsonl` on the hub. |

### Files at a glance

| Location | What |
|----------|------|
| `/etc/csa-hub/hub.yaml` | Hub config: URL, password hash, authenticator secret, notifications |
| `/var/lib/csa-hub/state.json` | Servers, tokens (hashed) and "always allow" rules |
| `/var/log/csa-hub/audit.jsonl` | Hub audit log |
| `/etc/csa/agent.yaml`, `/etc/csa/agent.key` | Agent connection settings and identity key |
| `/etc/csa/policy.yaml` | Your rules for this server |
| `/var/lib/csa/hub-rules.yaml` | Rules pushed from approvals (managed by the hub) |
| `/var/log/csa/audit.jsonl` | Server audit log |
