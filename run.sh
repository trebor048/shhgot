#!/usr/bin/env bash
#
# Run shhgit as a web dashboard, logging to a file.
#
#   ./run.sh                  # http://127.0.0.1:8080
#   PORT=9000 ./run.sh        # different port
#   THREADS=16 ./run.sh       # more scan concurrency
#   HOST=0.0.0.0 ./run.sh     # reachable from other machines
#
# The dashboard has NO authentication. The default binds loopback only; if you
# set HOST=0.0.0.0, put it behind a firewall, a reverse proxy with auth, or a
# Cloudflare Tunnel with Access in front of it. See the README.
#
set -euo pipefail
cd "$(dirname "$0")"

HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-8080}"
THREADS="${THREADS:-8}"
LOG="${LOG:-run.log}"

if [ ! -x ./shhgit ]; then
    echo "error: ./shhgit not found. Run ./install.sh or 'make build' first." >&2
    exit 1
fi

echo "starting shhgit dashboard on http://${HOST}:${PORT} (logs: ${LOG})"
exec ./shhgit --web --web-host "$HOST" --web-port "$PORT" \
    -threads "$THREADS" --config-path "$(pwd)" >>"$LOG" 2>&1
