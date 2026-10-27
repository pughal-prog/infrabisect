#!/bin/sh
# Static health check for the demo: every readiness probe must target a port
# the container actually listens on. Exit 0 = good, 1 = bad.
set -eu

deploy="$INFRABISECT_STATE_DIR/app/deployment.yaml"
[ -f "$deploy" ] || { echo "no deployment rendered"; exit 1; }

container_port=$(grep 'containerPort:' "$deploy" | awk '{print $NF}')
probe_port=$(grep -A4 'readinessProbe:' "$deploy" | grep 'port:' | awk '{print $NF}' || true)

if [ -n "$probe_port" ] && [ "$probe_port" != "$container_port" ]; then
  echo "readinessProbe targets port $probe_port but the container listens on $container_port"
  exit 1
fi
echo "ok: probe ${probe_port:-none}, container $container_port"
