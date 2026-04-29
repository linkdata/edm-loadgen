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
# Upstream EDM marks --minimiser-workers as a required positive int (the
# fork in this workspace also accepts 0=GOMAXPROCS, but we don't want to
# rely on that here). Pick a sensible default from the host CPU count;
# override with EDM_WORKERS=N if you want something specific.
EDM_WORKERS="${EDM_WORKERS:-$(nproc)}"

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

# drain_pgid <pgid> <label> — wait for every process in <pgid> to exit.
#
# Why this exists: `wait $PID` only tracks direct children of this shell. A
# `go run` spawns the actual binary as a grandchild that lives in the same
# process group (because we used setsid), but is not waitable here. So when
# `go run` dies, `wait` returns immediately while the real binary keeps
# running — for EDM that means it keeps holding the pebble lock and the
# next dev.sh run fails.
#
# After SIGTERM-on-pgid, poll the group until it drains. Escalate to
# SIGKILL if the group is still populated after 10s.
drain_pgid() {
    local pgid="$1" label="$2"
    local deadline=$(( $(date +%s) + 10 ))
    while pgrep -g "$pgid" >/dev/null 2>&1; do
        if (( $(date +%s) >= deadline )); then
            echo "dev.sh: $label did not exit within 10s, sending SIGKILL" >&2
            kill -KILL -- "-$pgid" 2>/dev/null || true
            break
        fi
        sleep 0.2
    done
    while pgrep -g "$pgid" >/dev/null 2>&1; do sleep 0.1; done
}

cleanup() {
    # Loadgen first (in case it survived the trap forward, e.g. if it was
    # killed via SIGKILL).
    if [[ -n "${LOADGEN_PID:-}" ]] && kill -0 "$LOADGEN_PID" 2>/dev/null; then
        kill -TERM -- "-$LOADGEN_PID" 2>/dev/null || true
        wait "$LOADGEN_PID" 2>/dev/null || true
        drain_pgid "$LOADGEN_PID" "loadgen"
    fi
    if [[ -n "$EDM_PID" ]] && kill -0 "$EDM_PID" 2>/dev/null; then
        echo
        echo "dev.sh: stopping EDM (pgid $EDM_PID)..."
        # Negative PID = signal the whole process group. setsid above ensures
        # `go run` and the spawned EDM binary share the group, so this kills
        # both — plain `kill $EDM_PID` would leave the child binary orphaned.
        kill -TERM -- "-$EDM_PID" 2>/dev/null || true
        wait "$EDM_PID" 2>/dev/null || true
        drain_pgid "$EDM_PID" "EDM"
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
        --minimiser-workers '$EDM_WORKERS' \
        --disable-histogram-sender \
        --disable-mqtt-filequeue \
        --mqtt-server 'tls://$MQTT_ENDPOINT' \
        --mqtt-keepalive 30 \
        --mqtt-ca-file '$KEYS_DIR/ca.crt' \
        --mqtt-client-cert-file '$KEYS_DIR/client.crt' \
        --mqtt-client-key-file '$KEYS_DIR/client.key' \
        --mqtt-signing-key-file '$KEYS_DIR/jws.key'
" >"$EDM_LOG" 2>&1 &
EDM_PID=$!

# ----- EDM liveness check --------------------------------------------------
#
# `go run` builds + spawns EDM through a wrapper goroutine; if EDM exits
# right after start (most commonly: pebble.Open fails because an orphaned
# EDM still holds the per-DB flock; or a config file is invalid; or the
# input TCP port is taken) we have to notice here, otherwise dev.sh runs
# happily with load-gen wired to nothing and the user gets a silent
# zero-throughput stall.
#
# Two-phase check:
#   1. Stabilisation: poll `kill -0 $EDM_PID` until either it dies (=>
#      our EDM failed; report) or enough time has elapsed for `go run`
#      to compile the binary, fork-exec it, and have the binary attempt
#      its TCP binds (an orphan-on-:53535 / :2112 produces EADDRINUSE
#      almost immediately; a missing config produces a fast exit; etc).
#   2. Readiness: confirm /metrics on :2112 responds.
#
# We can't shortcut to step 2 alone, because an orphan EDM bound to
# :2112 will happily serve /metrics under our nose while our own EDM
# has died from EADDRINUSE. Step 1 catches that.

EDM_STABILISE_SECONDS="${EDM_STABILISE_SECONDS:-5}"
EDM_READY_TIMEOUT="${EDM_READY_TIMEOUT:-15}"
EDM_METRICS_HOST="$(echo "$EDM_INPUT" | awk -F: '{print $1}')"
EDM_METRICS_URL="http://${EDM_METRICS_HOST}:2112/metrics"

report_edm_dead() {
    echo "dev.sh: EDM exited before becoming ready (PID $EDM_PID)" >&2
    echo "dev.sh: tail of $EDM_LOG:" >&2
    echo "----" >&2
    tail -20 "$EDM_LOG" >&2 2>/dev/null || true
    echo "----" >&2
    echo "dev.sh: common causes:" >&2
    echo "  (a) an orphan EDM still holds the pebble lock — look for a stale" >&2
    echo "      dnstapir-edm process (ps ... grep dnstapir-edm) and the line" >&2
    echo "      'pebble.Open: resource temporarily unavailable' in the log" >&2
    echo "  (b) port ${EDM_INPUT#*:} or :2112 already in use (orphan, or" >&2
    echo "      something else listening — ss -tnlp | grep 53535)" >&2
    echo "  (c) config-file or DAWG path missing/invalid" >&2
    exit 1
}

echo -n "dev.sh: waiting for EDM to stabilise (up to ${EDM_STABILISE_SECONDS}s)"
stab_deadline=$(( $(date +%s) + EDM_STABILISE_SECONDS ))
while (( $(date +%s) < stab_deadline )); do
    if ! kill -0 "$EDM_PID" 2>/dev/null; then
        echo
        report_edm_dead
    fi
    echo -n "."
    sleep 0.5
done
echo " alive"

echo -n "dev.sh: waiting for EDM /metrics on ${EDM_METRICS_URL}"
ready_deadline=$(( $(date +%s) + EDM_READY_TIMEOUT ))
while true; do
    if ! kill -0 "$EDM_PID" 2>/dev/null; then
        echo
        report_edm_dead
    fi
    if curl -fsS --max-time 1 "$EDM_METRICS_URL" >/dev/null 2>&1; then
        echo " ok"
        break
    fi
    if (( $(date +%s) >= ready_deadline )); then
        echo
        echo "dev.sh: EDM did not respond on /metrics within ${EDM_READY_TIMEOUT}s" >&2
        echo "dev.sh: tail of $EDM_LOG:" >&2
        echo "----" >&2
        tail -20 "$EDM_LOG" >&2 2>/dev/null || true
        echo "----" >&2
        exit 1
    fi
    echo -n "."
    sleep 0.5
done

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
