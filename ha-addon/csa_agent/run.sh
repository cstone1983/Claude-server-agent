#!/usr/bin/with-contenv bashio
# Start the Claude Server Agent inside this add-on. Its config, key, policy,
# hub rules and audit log live in /data (kept across restarts and updates).
set -e
mkdir -p /data/etc /data/lib /data/log
for p in /etc/csa:/data/etc /var/lib/csa:/data/lib /var/log/csa:/data/log; do
  rm -rf "${p%%:*}"; ln -s "${p#*:}" "${p%%:*}"
done
[ -f /data/etc/policy.yaml ] || cp /policy.default.yaml /data/etc/policy.yaml

if [ ! -f /data/etc/agent.yaml ]; then
  HUB="$(bashio::config 'hub_url')"; JOIN="$(bashio::config 'join_token')"
  NAME="$(bashio::config 'name')"; TAGS="$(bashio::config 'tags')"
  if [ -z "$JOIN" ]; then
    bashio::exit.nok "Not enrolled yet: create a join token on the hub (sudo csa-hub join-token), put it in this add-on's join_token option, then start it again."
  fi
  bashio::log.info "Enrolling with ${HUB} as '${NAME}'"
  csa-agent enroll --hub "$HUB" --join "$JOIN" --name "$NAME" ${TAGS:+--tags "$TAGS"} --replace
  bashio::log.info "Enrolled. The join token is used up; you can clear it from the options."
fi

bashio::log.info "Starting csa-agent ($(csa-agent version 2>/dev/null || echo unknown))"
exec csa-agent run
