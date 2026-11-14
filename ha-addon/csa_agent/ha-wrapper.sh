#!/bin/sh
# "ha" for commands run by csa-agent: the agent gives commands a clean
# environment, so load the add-on's Supervisor token here (never printed).
if [ -z "$SUPERVISOR_TOKEN" ] && [ -r /run/s6/container_environment/SUPERVISOR_TOKEN ]; then
  SUPERVISOR_TOKEN="$(cat /run/s6/container_environment/SUPERVISOR_TOKEN)"
  export SUPERVISOR_TOKEN
fi
exec /usr/local/bin/ha.bin "$@"
