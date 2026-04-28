#!/usr/bin/env bash
# Compile a CSV/text domain list into a well-known-domains.dawg suitable for
# both EDM (--well-known-domains-file) and edm-loadgen (--well-known-source).
#
# Usage:
#     tools/build-dawg.sh <input.csv> <output.dawg>
#
# Requires dnstapir-cli on PATH or at /tmp/dnstapir-cli/out/dnstapir-cli.

set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <input.csv> <output.dawg>" >&2
    exit 2
fi

IN="$1"
OUT="$2"

CLI="${DNSTAPIR_CLI:-}"
if [ -z "$CLI" ]; then
    if command -v dnstapir-cli >/dev/null 2>&1; then
        CLI=$(command -v dnstapir-cli)
    elif [ -x /tmp/dnstapir-cli/out/dnstapir-cli ]; then
        CLI=/tmp/dnstapir-cli/out/dnstapir-cli
    else
        echo "dnstapir-cli not found; set DNSTAPIR_CLI or build it from https://github.com/dnstapir/cli" >&2
        exit 1
    fi
fi

echo "compiling $IN -> $OUT (using $CLI)"
"$CLI" --standalone dawg compile --format csv --src "$IN" --dawg "$OUT"
echo "done"
