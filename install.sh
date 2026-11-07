#!/usr/bin/env bash
# Claude Server Agent installer.
#
#   Hub (once, on the machine that runs the Cloudflare Tunnel; also installs an agent for itself):
#     curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
#       | sudo bash -s -- hub --public-url https://agents.example.com --cloudflare-token <tunnel-token>
#
#   Each other server:
#     curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh \
#       | sudo bash -s -- agent --hub https://agents.example.com --join <token> [--name web01] [--tags web,prod]
#
#   Upgrade: re-run the same command (a join token is only needed the first time).
#   Update:  sudo csa-update [--check]   (or ask Claude: it sees "update available" in list_servers)
#   Remove:  sudo bash install.sh uninstall [--purge]
set -euo pipefail

REPO="cstone1983/Claude-server-agent"
BIN_DIR="/usr/local/bin"
GO_VERSION="1.26.0"

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,/^[^#]/p' "$0" 2>/dev/null | grep '^#' | sed 's/^# \{0,1\}//' || true
  cat <<'EOF'
Options:
  hub    --public-url URL [--cloudflare-token TOKEN] [--listen ADDR:PORT] [--name NAME] [--tags a,b] [--no-local-agent]
         --listen 0.0.0.0:7420 when cloudflared runs on a different machine (default 127.0.0.1:7420)
  agent  --hub URL [--join TOKEN] [--name NAME] [--tags a,b] [--replace]
         Without --name, the installer asks for a friendly name and tags.
  common [--version vX.Y.Z] [--from-source] [--ref BRANCH]
  update [--check] [--force]      install the latest GitHub release if it is newer (what csa-update runs)
  Updates never run on their own: run sudo csa-update, or let Claude run update_server when you approve.
  uninstall [--purge]
EOF
}

MODE="${1:-}"; shift || true
PUBLIC_URL="" LISTEN="" CF_TOKEN="" HUB_URL="" JOIN="" NAME="" TAGS="" VERSION="latest" FROM_SOURCE=0 REF="main" LOCAL_AGENT=1 PURGE=0 REPLACE=0 CHECK_ONLY=0 FORCE=0 NO_SOURCE_FALLBACK=0 QUIET=0
while [ $# -gt 0 ]; do
  case "$1" in
    --public-url) PUBLIC_URL="${2:?}"; shift 2 ;;
    --cloudflare-token) CF_TOKEN="${2:?}"; shift 2 ;;
    --listen) LISTEN="${2:?}"; shift 2 ;;
    --hub) HUB_URL="${2:?}"; shift 2 ;;
    --join) JOIN="${2:?}"; shift 2 ;;
    --name) NAME="${2:?}"; shift 2 ;;
    --tags) TAGS="${2:?}"; shift 2 ;;
    --version) VERSION="${2:?}"; shift 2 ;;
    --from-source) FROM_SOURCE=1; shift ;;
    --ref) REF="${2:?}"; shift 2 ;;
    --no-local-agent) LOCAL_AGENT=0; shift ;;
    --purge) PURGE=1; shift ;;
    --replace) REPLACE=1; shift ;;
    --check) CHECK_ONLY=1; shift ;;
    --force) FORCE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

need_root() { [ "$(id -u)" -eq 0 ] || die "run as root (sudo)"; }

check_os() {
  [ "$(uname -s)" = "Linux" ] || die "Linux only"
  if [ -r /etc/os-release ]; then
    . /etc/os-release
    case "${ID:-}${ID_LIKE:-}" in
      *ubuntu*|*debian*) ;;
      *) warn "untested distribution ${PRETTY_NAME:-unknown}; continuing" ;;
    esac
  fi
  command -v systemctl >/dev/null || die "systemd is required"
  command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl ca-certificates; }
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported CPU architecture $(uname -m)" ;;
  esac
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

download_release() {
  local base
  if [ "$VERSION" = "latest" ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi
  local tarball="csa_linux_${ARCH}.tar.gz"
  say "Downloading $tarball ($VERSION)"
  curl -fsSL -o "$TMP/$tarball" "$base/$tarball" || return 1
  curl -fsSL -o "$TMP/checksums.txt" "$base/checksums.txt" || return 1
  (cd "$TMP" && grep " $tarball\$" checksums.txt | sha256sum -c --quiet -) || die "checksum mismatch for $tarball"
  tar -xzf "$TMP/$tarball" -C "$TMP"
}

build_from_source() {
  say "Building from source ($REF)"
  command -v git >/dev/null || { apt-get update -qq && apt-get install -y -qq git; }
  local gobin
  if command -v go >/dev/null && go version | grep -Eq 'go1\.(2[6-9]|[3-9][0-9])'; then
    gobin="$(command -v go)"
  else
    say "Fetching Go $GO_VERSION toolchain (temporary)"
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -xz -C "$TMP"
    gobin="$TMP/go/bin/go"
  fi
  git clone -q --depth 1 --branch "$REF" "https://github.com/$REPO.git" "$TMP/src" \
    || die "could not download the source (branch '$REF') from github.com/$REPO; pass --ref <branch> if main does not exist yet"
  local ver; ver="$(cd "$TMP/src" && git describe --tags --always 2>/dev/null || echo source)"
  (cd "$TMP/src" && GOFLAGS=-trimpath CGO_ENABLED=0 GOCACHE="$TMP/gocache" GOPATH="$TMP/gopath" "$gobin" build \
     -ldflags "-s -w -X github.com/cstone1983/Claude-server-agent/internal/version.Version=$ver" -o "$TMP" ./cmd/csa-hub ./cmd/csa-agent)
}

fetch_binaries() {
  detect_arch
  if [ "$FROM_SOURCE" -eq 1 ]; then
    build_from_source
  elif ! download_release; then
    [ "$NO_SOURCE_FALLBACK" -eq 0 ] || die "could not download release $VERSION"
    warn "no release download available; building from source instead"
    build_from_source
  fi
  [ -x "$TMP/csa-agent" ] || die "csa-agent binary missing"
}

install_bin() {
  install -m 0755 "$TMP/$1" "$BIN_DIR/$1.new" && mv -f "$BIN_DIR/$1.new" "$BIN_DIR/$1"
  say "Installed $BIN_DIR/$1 ($("$BIN_DIR/$1" version))"
}

write_default_policy() {
  [ -f /etc/csa/policy.yaml ] && return 0
  mkdir -p /etc/csa && chmod 0755 /etc/csa
  cat > /etc/csa/policy.yaml <<'EOF'
# Claude Server Agent — local policy for this server.
#
# Tiers:
#   read      built-in read-only commands (df, ps, systemctl status, journalctl, ...) run freely
#   allowed   anything matching an "allow" rule below runs freely
#   elevated  everything else needs your approval (link + authenticator code)
#   denied    "deny" rules can never run, even with approval
#
# Rules match the command's argv element by element:
#   "restart|reload"  any of the alternatives      "*"   any single argument (glob)
#   "=lit*ral"        exact literal                "**"  (last element) any remaining arguments
# "as" is the user the command runs as ("" = root, "*" = any).
#
# Check a command:  csa-agent check -- systemctl restart nginx
# After editing:    systemctl reload csa-agent
#
# "Approve + always allow" rules from the hub are kept separately in
# /var/lib/csa/hub-rules.yaml. Set accept_hub_rules: false to ignore them.

allow: []
#  - id: restart-web
#    argv: ["systemctl", "restart|reload", "nginx|php8.3-fpm"]
#  - id: apt-refresh
#    argv: ["apt-get", "update"]
#  - id: app-compose
#    argv: ["docker", "compose", "-f", "/srv/app/compose.yml", "**"]
#  - id: app-config
#    write_paths: ["/srv/app/config/**"]
#  - id: nightly-script
#    shell: "/usr/local/bin/backup.sh && echo done"

deny: []
#  - id: no-reboot
#    argv: ["reboot", "**"]
#  - id: no-db-files
#    write_paths: ["/var/lib/postgresql/**"]

# Extra paths whose reads need approval (keys, .env, /etc/shadow, ... are built in).
sensitive_paths: []

# accept_hub_rules: true
EOF
  chmod 0644 /etc/csa/policy.yaml
}

write_agent_unit() {
  cat > /etc/systemd/system/csa-agent.service <<'EOF'
[Unit]
Description=Claude Server Agent (connects this server to the csa hub)
Documentation=https://github.com/cstone1983/Claude-server-agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/csa-agent run
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5
# Background jobs (e.g. upgrades) keep running if the agent itself restarts.
KillMode=process
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
  mkdir -p /var/lib/csa /var/log/csa
  chmod 0700 /var/lib/csa
  chmod 0750 /var/log/csa
}

write_hub_unit() {
  id csa-hub >/dev/null 2>&1 || useradd --system --home-dir /var/lib/csa-hub --shell /usr/sbin/nologin csa-hub
  cat > /etc/systemd/system/csa-hub.service <<'EOF'
[Unit]
Description=Claude Server Agent hub (MCP connector endpoint)
Documentation=https://github.com/cstone1983/Claude-server-agent
After=network-online.target
Wants=network-online.target

[Service]
User=csa-hub
Group=csa-hub
ExecStart=/usr/local/bin/csa-hub serve
Restart=always
RestartSec=5
RuntimeDirectory=csa-hub
RuntimeDirectoryMode=0700
StateDirectory=csa-hub
StateDirectoryMode=0700
LogsDirectory=csa-hub
LogsDirectoryMode=0750
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF
}

wait_for_hub() {
  for _ in $(seq 1 30); do
    curl -fsS -o /dev/null http://127.0.0.1:7420/healthz 2>/dev/null && return 0
    sleep 1
  done
  journalctl -u csa-hub -n 30 --no-pager >&2 || true
  die "hub did not start"
}

install_cloudflared() {
  if ! command -v cloudflared >/dev/null; then
    say "Installing cloudflared"
    curl -fsSL -o "$TMP/cloudflared.deb" "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${ARCH}.deb"
    dpkg -i "$TMP/cloudflared.deb" >/dev/null
  fi
  if systemctl list-unit-files cloudflared.service >/dev/null 2>&1 && systemctl is-enabled cloudflared >/dev/null 2>&1; then
    say "cloudflared service already installed; leaving it as is"
  else
    say "Registering the Cloudflare Tunnel"
    cloudflared service install "$CF_TOKEN"
  fi
}

# can_ask: true when a person is at a terminal (also works with curl | sudo bash,
# where stdin is the script, by reading from /dev/tty).
can_ask() { { : </dev/tty >/dev/tty; } 2>/dev/null; }

# ask "question" "default" -> prints the answer (or the default).
# drain_tty discards anything already typed or pasted (e.g. the extra line
# break from pasting a multi-line command), so it can't answer a prompt.
drain_tty() {
  while IFS= read -r -t 0.3 -n 4096 _ </dev/tty 2>/dev/null; do :; done
}

ask() {
  local q="$1" def="$2" ans=""
  if can_ask; then
    drain_tty
    if [ -n "$def" ]; then printf '%s [%s]: ' "$q" "$def" >/dev/tty; else printf '%s: ' "$q" >/dev/tty; fi
    IFS= read -r ans </dev/tty || ans=""
  fi
  printf '%s' "${ans:-$def}"
}

valid_name() { printf '%s' "$1" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$'; }

current_agent_name() { { sed -n 's/^name: *//p' /etc/csa/agent.yaml 2>/dev/null || true; } | tr -d '"'; }

enroll_agent() {
  local hub="$1" name="$NAME" tags="$TAGS" out
  if [ -n "$name" ]; then
    say "Using server name '$name' (from --name; leave --name out to be asked)"
  elif ! can_ask; then
    say "No terminal to ask on; using the hostname as the server name (rename later with: sudo csa-hub rename)"
  fi
  if [ -z "$name" ]; then
    local def; def="$(current_agent_name)"; [ -n "$def" ] || def="$(hostname -s)"
    if can_ask; then
      printf '\n\033[1;32m===== Last step: name this server =====\033[0m\n' >/dev/tty
      printf 'This is how you and Claude will refer to it. Press Enter to use "%s".\n' "$def" >/dev/tty
    fi
    name="$(ask "Friendly name" "$def")"
    if [ -z "$TAGS" ]; then
      tags="$(ask "Tags, comma-separated (optional, e.g. web,prod; Enter to skip)" "")"
    fi
    say "Server name: $name${tags:+ (tags: $tags)}"
  fi
  while :; do
    until valid_name "$name"; do
      can_ask || die "invalid server name '$name' (letters, digits, . _ -; max 63)"
      warn "'$name' is not a valid name (letters, digits, . _ -; max 63)"
      name="$(ask "Friendly name for this server" "")"
    done
    local args=(--hub "$hub" --join "$JOIN" --name "$name")
    [ -n "$tags" ] && args+=(--tags "$tags")
    # Re-enrolling this same machine under its current name replaces its old entry.
    if [ "$REPLACE" -eq 1 ] || { agent_enrolled && [ "$(current_agent_name)" = "$name" ]; }; then
      args+=(--replace)
    fi
    if out="$("$BIN_DIR/csa-agent" enroll "${args[@]}" 2>&1)"; then
      echo "$out"
      return 0
    fi
    if printf '%s' "$out" | grep -q "already exists" && can_ask; then
      warn "a server named '$name' is already on the hub"
      name="$(ask "Pick a different name (or re-run with --replace to replace it)" "")"
      continue
    fi
    die "$out"
  done
}

agent_enrolled() { [ -f /etc/csa/agent.yaml ] && grep -q '^agent_id: .\+' /etc/csa/agent.yaml; }

start_agent() {
  systemctl daemon-reload
  systemctl enable --quiet csa-agent
  systemctl restart csa-agent
  sleep 2
  if systemctl is-active --quiet csa-agent; then
    say "csa-agent is running"
  else
    journalctl -u csa-agent -n 30 --no-pager >&2 || true
    die "csa-agent failed to start"
  fi
}

do_hub() {
  case "${LISTEN:-}" in
    ""|127.0.0.1:7420|0.0.0.0:7420) ;;
    *) die "--listen must be 127.0.0.1:7420 (cloudflared on this machine) or 0.0.0.0:7420 (cloudflared elsewhere)" ;;
  esac
  [ -n "$PUBLIC_URL" ] || [ -f /etc/csa-hub/hub.yaml ] || die "--public-url is required (e.g. https://agents.example.com)"
  fetch_binaries
  [ -x "$TMP/csa-hub" ] || die "csa-hub binary missing"
  install_bin csa-hub
  install_bin csa-agent
  write_hub_unit

  local first=0
  if [ ! -f /etc/csa-hub/hub.yaml ]; then
    first=1
    mkdir -p /etc/csa-hub
    echo
    local pw="" pw2=""
    if can_ask; then
      while :; do
        printf 'Choose an admin password (at least 10 characters; Enter to generate one): ' >/dev/tty
        IFS= read -rs pw </dev/tty || pw=""; echo >/dev/tty
        [ -z "$pw" ] && break
        if [ "${#pw}" -lt 10 ]; then warn "too short"; continue; fi
        printf 'Repeat it: ' >/dev/tty
        IFS= read -rs pw2 </dev/tty || pw2=""; echo >/dev/tty
        [ "$pw" = "$pw2" ] && break
        warn "they don't match"
      done
    fi
    CSA_ADMIN_PASSWORD="$pw" "$BIN_DIR/csa-hub" init --public-url "$PUBLIC_URL" --listen "${LISTEN:-127.0.0.1:7420}"
    echo
  elif [ -n "$LISTEN" ]; then
    sed -i "s/^listen: .*/listen: $LISTEN/" /etc/csa-hub/hub.yaml
  fi
  chown -R csa-hub:csa-hub /etc/csa-hub
  chmod 0700 /etc/csa-hub
  chmod 0600 /etc/csa-hub/hub.yaml
  PUBLIC_URL="$(sed -n 's/^public_url: *//p' /etc/csa-hub/hub.yaml | tr -d '"')"

  systemctl daemon-reload
  systemctl enable --quiet csa-hub
  systemctl restart csa-hub
  wait_for_hub
  say "csa-hub is running on 127.0.0.1:7420"

  if [ -n "$CF_TOKEN" ]; then
    install_cloudflared
  fi
  if grep -q '^listen: 0\.0\.0\.0' /etc/csa-hub/hub.yaml; then
    warn "the hub accepts connections on all interfaces (port 7420). Allow only your cloudflared machine, e.g.:"
    warn "  sudo ufw allow from <CLOUDFLARED-IP> to any port 7420 proto tcp && sudo ufw deny 7420/tcp"
  fi

  if [ "$LOCAL_AGENT" -eq 1 ]; then
    write_default_policy
    write_agent_unit
    if ! agent_enrolled; then
      say "Enrolling this machine as a server on its own hub"
      JOIN="$("$BIN_DIR/csa-hub" join-token --quiet --ttl 5 --tags hub)"
      enroll_agent "http://127.0.0.1:7420"
    fi
    start_agent
  fi
  write_update_units

  [ "$QUIET" -eq 1 ] && return 0
  cat <<EOF

$(say "Hub ready")
  Connector URL:  ${PUBLIC_URL}/mcp

  1. Cloudflare Zero Trust → Networks → Tunnels → your tunnel → Public Hostname:
       $(echo "$PUBLIC_URL" | sed 's#https\?://##')  →  http://localhost:7420
$( [ -z "$CF_TOKEN" ] && ! systemctl is-active --quiet cloudflared && echo "     (cloudflared is not running here: install it with 'cloudflared service install <token>', or point a route on your existing tunnel at this machine)" )
  2. Claude → Settings → Connectors → Add custom connector → paste the connector URL.
     Log in with the admin password and an authenticator code$( [ $first -eq 1 ] && echo " shown above" ).
  3. Add servers: run 'sudo csa-hub join-token' here and paste the command it prints on each server.

  Approvals: 'sudo csa-hub approvals' — or open the link Claude shows you.
  Optional push notifications: set notify.ntfy_url or notify.webhook_url in /etc/csa-hub/hub.yaml,
  then 'systemctl restart csa-hub'.
EOF
}

do_agent() {
  if ! agent_enrolled; then
    [ -n "$HUB_URL" ] && [ -n "$JOIN" ] || die "--hub and --join are required for a new server (get a token with 'csa-hub join-token' on the hub)"
  fi
  fetch_binaries
  install_bin csa-agent
  write_default_policy
  write_agent_unit
  write_update_units
  # Questions come last, after all downloads and setup.
  if [ -n "$JOIN" ]; then
    [ -n "$HUB_URL" ] || HUB_URL="$(sed -n 's/^hub_url: *//p' /etc/csa/agent.yaml)"
    enroll_agent "$HUB_URL"
    say "Enrolled with $HUB_URL"
  fi
  start_agent

  [ "$QUIET" -eq 1 ] && return 0
  cat <<EOF

$(say "Server connected")
  It now appears in Claude via list_servers. Local policy: /etc/csa/policy.yaml
  Test a command's tier:  sudo csa-agent check -- systemctl restart nginx
  Audit log:              sudo csa-agent audit
EOF
}

write_update_units() {
  # csa-update: fetch the installer from the latest GitHub release and run it in
  # update mode. Run by hand or by the agent's update_server tool; nothing runs it
  # automatically.
  cat > /usr/local/sbin/csa-update <<EOF
#!/bin/sh
# Claude Server Agent updater. Usage: sudo csa-update [--check] [--force]
set -eu
tmp="\$(mktemp)"
trap 'rm -f "\$tmp"' EXIT
if ! curl -fsSL -o "\$tmp" "https://github.com/$REPO/releases/latest/download/install.sh"; then
  echo "csa-update: no release published yet (or GitHub unreachable); nothing to do"
  exit 0
fi
bash "\$tmp" update "\$@"
EOF
  chmod 0755 /usr/local/sbin/csa-update
}

installed_version() {
  local bin
  for bin in "$BIN_DIR/csa-agent" "$BIN_DIR/csa-hub"; do
    if [ -x "$bin" ]; then "$bin" version | awk '{print $2}'; return; fi
  done
}

latest_release() {
  # The /releases/latest page redirects to /releases/tag/<tag>; this avoids API rate limits.
  curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null | sed -n 's#.*/releases/tag/##p'
}

do_update() {
  [ -x "$BIN_DIR/csa-agent" ] || [ -x "$BIN_DIR/csa-hub" ] || die "Claude Server Agent is not installed here"
  local current latest
  current="$(installed_version)"
  latest="$(latest_release)"
  [ -n "$latest" ] || { say "No release published on GitHub yet; nothing to do"; return 0; }
  if [ "$current" = "$latest" ] && [ "$FORCE" -eq 0 ]; then
    say "Up to date ($current)"
    return 0
  fi
  if [ "$CHECK_ONLY" -eq 1 ]; then
    say "Update available: $current -> $latest (run: sudo csa-update)"
    return 0
  fi
  say "Updating $current -> $latest"
  VERSION="$latest" NO_SOURCE_FALLBACK=1 QUIET=1
  if [ -f /etc/csa-hub/hub.yaml ]; then
    agent_enrolled && LOCAL_AGENT=1 || LOCAL_AGENT=0
    do_hub
  else
    agent_enrolled || die "agent is not enrolled; re-run the installer with --hub and --join"
    do_agent
  fi
  say "Updated to $latest"
}

do_uninstall() {
  rm -f /usr/local/sbin/csa-update
  for svc in csa-agent csa-hub; do
    if systemctl list-unit-files "$svc.service" >/dev/null 2>&1; then
      systemctl disable --now "$svc" 2>/dev/null || true
      rm -f "/etc/systemd/system/$svc.service"
    fi
  done
  systemctl daemon-reload
  rm -f "$BIN_DIR/csa-agent" "$BIN_DIR/csa-hub"
  if [ "$PURGE" -eq 1 ]; then
    rm -rf /etc/csa /var/lib/csa /var/log/csa /etc/csa-hub /var/lib/csa-hub /var/log/csa-hub
    id csa-hub >/dev/null 2>&1 && userdel csa-hub || true
    say "Removed Claude Server Agent and all its data"
  else
    say "Removed services and binaries (config and logs kept; use --purge to delete them)"
  fi
  say "cloudflared was left installed; remove it with: cloudflared service uninstall && apt-get remove cloudflared"
}

main() {
  case "$MODE" in
    hub) need_root; check_os; do_hub ;;
    agent) need_root; check_os; do_agent ;;
    update) need_root; check_os; do_update ;;
    uninstall) need_root; do_uninstall ;;
    -h|--help|help|"") usage ;;
    *) die "unknown mode '$MODE' (hub, agent, update or uninstall)" ;;
  esac
}

main
