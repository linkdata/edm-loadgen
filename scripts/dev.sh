#!/usr/bin/env bash
# dev.sh — run edm-loadgen in serve mode (UI + embedded MQTT broker) in the
# foreground, with a native EDM running in the background pointing at our
# broker. EDM is shut down automatically when the load-gen exits.
#
# Both processes use `go run` so source edits take effect on the next run
# without manual rebuilds.
#
# Defaults assume the layout from README.md:
#   /tmp/dnstapir-edm       — clone of github.com/dnstapir/edm
#   /tmp/edm-config         — edm.toml + well-known-domains.dawg
#   /tmp/edm-data           — EDM scratch dir
# Override via env vars (EDM_SRC, EDM_CONFIG, EDM_DATA, etc.) if your layout
# differs. Any positional args are passed straight through to edm-loadgen.

set -euo pipefail

EDM_SRC="${EDM_SRC:-/tmp/dnstapir-edm}"
EDM_CONFIG="${EDM_CONFIG:-/tmp/edm-config}"
EDM_DATA="${EDM_DATA:-/tmp/edm-data}"
KEYS_DIR="${KEYS_DIR:-$PWD/keys}"
NODE_NAME="${NODE_NAME:-edm-loadgen-1}"
MQTT_LISTEN="${MQTT_LISTEN:-:8883}"
LOADGEN_LISTEN="${LOADGEN_LISTEN:-:8090}"
EDM_INPUT="${EDM_INPUT:-127.0.0.1:53535}"
EDM_LOG="${EDM_LOG:-/tmp/dev-edm.log}"

# ----- sanity checks -------------------------------------------------------

if [[ ! -d "$EDM_SRC" ]]; then
    echo "dev.sh: EDM_SRC=$EDM_SRC is not a directory" >&2
    echo "        clone https://github.com/dnstapir/edm there or set EDM_SRC" >&2
    exit 1
fi
if [[ ! -f "$EDM_CONFIG/edm.toml" ]]; then
    echo "dev.sh: missing $EDM_CONFIG/edm.toml" >&2
    echo "        see README.md > 'Running a native EDM' for the recipe" >&2
    exit 1
fi
if [[ ! -f "$EDM_CONFIG/well-known-domains.dawg" ]]; then
    echo "dev.sh: missing $EDM_CONFIG/well-known-domains.dawg" >&2
    exit 1
fi
mkdir -p "$EDM_DATA"

# Convert a possibly-bare-port (":8883") to a connectable host:port for EDM.
mqtt_endpoint() {
    case "$1" in
        :*) echo "127.0.0.1$1" ;;
        *)  echo "$1" ;;
    esac
}
MQTT_ENDPOINT="$(mqtt_endpoint "$MQTT_LISTEN")"

# ----- lifecycle -----------------------------------------------------------

EDM_PID=""

cleanup() {
    # Loadgen first (in case it survived the trap forward, e.g. if it was
    # killed via SIGKILL).
    if [[ -n "${LOADGEN_PID:-}" ]] && kill -0 "$LOADGEN_PID" 2>/dev/null; then
        kill -TERM -- "-$LOADGEN_PID" 2>/dev/null || true
        wait "$LOADGEN_PID" 2>/dev/null || true
    fi
    if [[ -n "$EDM_PID" ]] && kill -0 "$EDM_PID" 2>/dev/null; then
        echo
        echo "dev.sh: stopping EDM (pgid $EDM_PID)..."
        # Negative PID = signal the whole process group. setsid above ensures
        # `go run` and the spawned EDM binary share the group, so this kills
        # both — plain `kill $EDM_PID` would leave the child binary orphaned.
        kill -TERM -- "-$EDM_PID" 2>/dev/null || true
        wait "$EDM_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# ----- start EDM in background ---------------------------------------------

echo "dev.sh: starting EDM (logs -> $EDM_LOG) ..."
# setsid puts go run and the EDM binary it execs into a fresh process group
# rooted at the go-run pid, so cleanup can kill the whole group.
setsid bash -c "
    cd '$EDM_SRC' && \
    exec go run ./cmd/dnstapir-edm run \
        --input-tcp '$EDM_INPUT' \
        --data-dir '$EDM_DATA' \
        --config-file '$EDM_CONFIG/edm.toml' \
        --well-known-domains-file '$EDM_CONFIG/well-known-domains.dawg' \
        --disable-histogram-sender \
        --mqtt-server 'tls://$MQTT_ENDPOINT' \
        --mqtt-keepalive 30 \
        --mqtt-ca-file '$KEYS_DIR/ca.crt' \
        --mqtt-client-cert-file '$KEYS_DIR/client.crt' \
        --mqtt-client-key-file '$KEYS_DIR/client.key' \
        --mqtt-signing-key-file '$KEYS_DIR/jws.key'
" >"$EDM_LOG" 2>&1 &
EDM_PID=$!

# EDM dials MQTT very early; if the broker isn't up yet it'll just retry.
# That's fine — we don't need to gate on EDM being "ready" before starting
# the load-gen, since the load-gen owns the broker.

# ----- start load-gen in foreground ----------------------------------------

echo "dev.sh: starting load-gen (UI on http://localhost${LOADGEN_LISTEN}/ )..."
echo "dev.sh: edm-loadgen will generate keys in $KEYS_DIR on first run"

# Run loadgen in its own process group (setsid) so external `kill -INT
# <script-pid>` can be forwarded as a process-group signal that hits both
# `go run` and the binary it execs. `go run` does not reliably propagate
# signals to its child by itself.
setsid bash -c "
    exec go run ./cmd/edm-loadgen serve \
        --listen '$LOADGEN_LISTEN' \
        --mqtt-listen '$MQTT_LISTEN' \
        --mqtt-keys-dir '$KEYS_DIR' \
        --mqtt-node-name '$NODE_NAME' \
        $(printf "%q " "$@")
" &
LOADGEN_PID=$!

# Forward signals to the loadgen process group.
forward() {
    if [[ -n "${LOADGEN_PID:-}" ]] && kill -0 "$LOADGEN_PID" 2>/dev/null; then
        kill -"$1" -- "-$LOADGEN_PID" 2>/dev/null || true
    fi
}
trap 'forward INT'  INT
trap 'forward TERM' TERM
trap cleanup EXIT

wait "$LOADGEN_PID" || true
