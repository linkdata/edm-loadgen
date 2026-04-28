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

EDM_SRC="${EDM_SRC:-../edm}"
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

# ----- start load-gen in background first ----------------------------------
#
# Starting loadgen first lets it generate the TLS material EDM needs. EDM
# fails fast on a missing client cert, so we then poll for the keys to exist
# before starting EDM. Both children get their own process groups via setsid
# so cleanup can signal each tree independently.

echo "dev.sh: starting load-gen (UI on http://localhost${LOADGEN_LISTEN}/ )..."
echo "dev.sh: edm-loadgen will generate keys in $KEYS_DIR on first run"

setsid bash -c "
    exec go run ./cmd/edm-loadgen serve \
        --listen '$LOADGEN_LISTEN' \
        --mqtt-listen '$MQTT_LISTEN' \
        --mqtt-keys-dir '$KEYS_DIR' \
        --mqtt-node-name '$NODE_NAME' \
        $(printf "%q " "$@")
" &
LOADGEN_PID=$!

# ----- wait for keys before starting EDM -----------------------------------

echo -n "dev.sh: waiting for $KEYS_DIR/client.crt"
deadline=$(( $(date +%s) + 30 ))
while [[ ! -f "$KEYS_DIR/client.crt" ]]; do
    if (( $(date +%s) >= deadline )); then
        echo
        echo "dev.sh: timed out waiting for keys; loadgen may have failed" >&2
        exit 1
    fi
    if ! kill -0 "$LOADGEN_PID" 2>/dev/null; then
        echo
        echo "dev.sh: loadgen exited before generating keys" >&2
        exit 1
    fi
    echo -n "."
    sleep 0.5
done
echo " ok"

# ----- start EDM in background ---------------------------------------------

echo "dev.sh: starting EDM (logs -> $EDM_LOG) ..."
setsid bash -c "
    cd '$EDM_SRC' && \
    exec go run ./cmd/dnstapir-edm run \
        --input-tcp '$EDM_INPUT' \
        --data-dir '$EDM_DATA' \
        --config-file '$EDM_CONFIG/edm.toml' \
        --well-known-domains-file '$EDM_CONFIG/well-known-domains.dawg' \
        --minimiser-workers 0 \
        --disable-histogram-sender \
        --mqtt-server 'tls://$MQTT_ENDPOINT' \
        --mqtt-keepalive 30 \
        --mqtt-ca-file '$KEYS_DIR/ca.crt' \
        --mqtt-client-cert-file '$KEYS_DIR/client.crt' \
        --mqtt-client-key-file '$KEYS_DIR/client.key' \
        --mqtt-signing-key-file '$KEYS_DIR/jws.key'
" >"$EDM_LOG" 2>&1 &
EDM_PID=$!

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
