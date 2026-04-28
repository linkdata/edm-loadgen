#!/usr/bin/env bash
# Download the latest Tranco top-1M list and write it to ./tranco-top1m.csv.
#
# Tranco is a research-oriented top-domains ranking that resists day-to-day
# manipulation (https://tranco-list.eu). The CSV column layout is
# "<rank>,<domain>", which the load-gen's domain loader handles directly.
#
# Usage:
#     tools/fetch-tranco.sh [output-path]
#
# Defaults to ./tranco-top1m.csv. The file is ~25 MiB.

set -euo pipefail

OUT="${1:-tranco-top1m.csv}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

URL="https://tranco-list.eu/top-1m.csv.zip"
echo "fetching $URL"
curl -fsSL --output "$TMP/list.zip" "$URL"
echo "unpacking"
unzip -p "$TMP/list.zip" top-1m.csv > "$OUT"
lines=$(wc -l < "$OUT")
echo "wrote $OUT ($lines lines)"
