# edm-loadgen

A synthetic DNS-traffic load generator for [dnstapir/edm](https://github.com/dnstapir/edm).

`edm-loadgen` connects directly to EDM's `--input-tcp` / `--input-unix`
Frame Streams socket — no resolver, no docker stack — and emits
`CLIENT_RESPONSE` dnstap envelopes at a controllable rate, drawing from a
weighted mix of eight traffic patterns (background, well-known, DGA, beacon,
fast-flux, dyn-DNS, exfil, exotic, evasion). A separate verifier scrapes
EDM's `/metrics` and reconciles what was sent against what EDM observed.

Two front-ends:

- **`run`** — headless, prints a recurring text status line. Suitable for CI
  and long-running tests.
- **`serve`** — same producer + verifier, but with a JaWS-driven web UI
  (server-rendered HTML over WebSocket; no client-side JS state) where every
  knob, mix weight, and live counter is bound to a Bootstrap widget.

Single static binary, ~12 MiB. No external runtime deps.

## Build

```bash
go build -o edm-loadgen ./cmd/edm-loadgen
```

Go 1.24+ is required. The dnstap and Frame Streams libs are pinned to the
versions EDM uses (`github.com/dnstap/golang-dnstap@v0.4.0`,
`github.com/farsightsec/golang-framestream@v0.3.0`) so envelopes are
byte-compatible.

## Running a native EDM alongside the load-gen

The load-gen ships frames over Frame Streams to a running EDM process. The
upstream EDM container image is private (`ghcr.io/dnstapir/edm:latest`
returns `DENIED` for anonymous pulls), so the easiest path is a native
build from source. Both binaries on the same host means the load-gen's
default `--target tcp://127.0.0.1:53535` and `--metrics-url
http://127.0.0.1:2112/metrics` work as-is.

Three build steps. From a fresh shell:

```bash
# 1. Clone and build dnstapir-edm.
git clone https://github.com/dnstapir/edm.git /tmp/dnstapir-edm
cd /tmp/dnstapir-edm
go build -o /tmp/dnstapir-edm-bin ./cmd/dnstapir-edm

# 2. Clone and build dnstapir-cli (only used to compile the DAWG file).
git clone https://github.com/dnstapir/cli.git /tmp/dnstapir-cli
make -C /tmp/dnstapir-cli build

# 3. Build a tiny DAWG of "well-known" domains and an EDM config.
mkdir -p /tmp/edm-config /tmp/edm-data
cat > /tmp/tiny-domains.csv <<'EOF'
1,example.com
2,example.org
3,example.net
4,iana.org
5,wikipedia.org
6,cloudflare.com
7,google.com
8,github.com
EOF
/tmp/dnstapir-cli/out/dnstapir-cli --standalone dawg compile \
    --format csv \
    --src /tmp/tiny-domains.csv \
    --dawg /tmp/edm-config/well-known-domains.dawg
echo "cryptopan-key = \"$(openssl rand -base64 15)\"" \
    > /tmp/edm-config/edm.toml
```

Then start EDM. The flags below disable MQTT and histogram upload (so EDM
runs with no upstream connectivity) and point it at TCP `:53535` for input
and the default `:2112` for `/metrics`:

```bash
/tmp/dnstapir-edm-bin run \
    --input-tcp 127.0.0.1:53535 \
    --data-dir /tmp/edm-data \
    --config-file /tmp/edm-config/edm.toml \
    --well-known-domains-file /tmp/edm-config/well-known-domains.dawg \
    --disable-mqtt \
    --disable-histogram-sender
```

EDM logs JSON to stdout. A successful start ends with lines like
`creating plaintext dnstap TCP socket` (input is up) and a goroutine
spinning up minimiser workers. To confirm `/metrics` is live:

```bash
curl -s localhost:2112/metrics | grep '^edm_processed_dnstap_total'
# edm_processed_dnstap_total 0
```

The load-gen can now run against this instance with no further setup. To
make EDM and the load-gen share the *same* well-known set (so the
wellknown-mix knob is honest), pass the same DAWG to both:

```bash
./edm-loadgen run \
    --well-known-source /tmp/tiny-domains.csv \
    --qps 200
```

Tools wrappers for the build-dawg step live in
[`tools/build-dawg.sh`](tools/build-dawg.sh) and a Tranco top-1M fetcher
in [`tools/fetch-tranco.sh`](tools/fetch-tranco.sh).

> **Note**: the upstream EDM hard-codes its `/metrics` listener to
> `127.0.0.1:2112` and pprof to `127.0.0.1:6060`. That's fine when both
> EDM and the load-gen run on the same host (the default). To scrape EDM
> from another host or container, patch
> `pkg/runner/runner.go` and replace `"127.0.0.1:2112"` and
> `"127.0.0.1:6060"` with `"0.0.0.0:..."` before `go build`.

### Optional: enable MQTT publishing

The recipe above runs EDM with `--disable-mqtt`. That keeps the data path
working but suppresses one counter — `edm_new_qname_queued_total` — because
EDM increments it inside the MQTT-publish branch
([`pkg/runner/runner.go:2014`](https://github.com/dnstapir/edm/blob/main/pkg/runner/runner.go)).
To exercise the full publish path, the load-gen embeds a minimal MQTT broker
(`internal/mqtt`) that EDM can connect to. The broker accepts any client cert
on a self-signed TLS listener and counts received Publishes per topic.

Start the load-gen with `--mqtt-listen` and it will:

1. Generate (or reuse) a CA + server cert + client cert + JWS key under
   `--mqtt-keys-dir` (default `./keys/`).
2. Open the broker on the configured TLS port.
3. Print the exact EDM flags to feed it.

```bash
./edm-loadgen serve --listen :8090 --mqtt-listen :8883
# edm-loadgen MQTT broker listening on tls://:8883
# Run EDM with these MQTT flags (drop --disable-mqtt):
#   --mqtt-server=tls://127.0.0.1:8883
#   --mqtt-topic=events/up/edm-loadgen-1/edm
#   ...
```

Then restart EDM with those flags (drop `--disable-mqtt`):

```bash
/tmp/dnstapir-edm-bin run \
    --input-tcp 127.0.0.1:53535 \
    --data-dir /tmp/edm-data \
    --config-file /tmp/edm-config/edm.toml \
    --well-known-domains-file /tmp/edm-config/well-known-domains.dawg \
    --disable-histogram-sender \
    --mqtt-server=tls://127.0.0.1:8883 \
    --mqtt-topic=events/up/edm-loadgen-1/edm \
    --mqtt-client-id=edm-loadgen-1-edm-pub \
    --mqtt-signing-key-id=edm-loadgen-1 \
    --mqtt-ca-file=./keys/ca.crt \
    --mqtt-client-cert-file=./keys/client.crt \
    --mqtt-client-key-file=./keys/client.key \
    --mqtt-signing-key-file=./keys/jws.key
```

In the UI, **EDM new qname**, **MQTT received**, and **MQTT (EDM topic)**
gauges should all start ticking on novel traffic. The broker is dev-only —
no JWS-signature validation, no ACLs, accepts whatever client cert EDM
presents — and only ever bound to localhost by default.

## Quick start

Assuming a running native EDM at `127.0.0.1:53535` (with `/metrics` on
`:2112`):

```bash
# Headless, 200 qps, prints status every 5s, runs until SIGINT.
./edm-loadgen run --qps 200

# Same but with the JaWS UI.
./edm-loadgen serve --listen :8090
xdg-open http://localhost:8090

# Shipping-rate sanity check — send 100 frames and exit.
./edm-loadgen smoke --count 100

# One-shot snapshot of EDM's /metrics, parsed into JSON.
./edm-loadgen verify
```

## Subcommands

| Command  | Purpose                                         |
|----------|-------------------------------------------------|
| `smoke`  | Send N hand-crafted frames and exit             |
| `run`    | Producer + verifier with a recurring text line  |
| `serve`  | Producer + verifier + JaWS web UI               |
| `verify` | One-shot snapshot of EDM `/metrics`             |

Each subcommand has its own `-h` help. Common flags (`--target`, `--qps`,
`--metrics-url`, `--config`, `--report-interval`, …) appear on both `run`
and `serve`.

## Configuration

Defaults are in `internal/state.New`. Override via flags or a JSON config file:

```bash
./edm-loadgen run --config configs/realistic.json
./edm-loadgen serve --config configs/chaos.json --listen :8090
```

Precedence: **flag defaults < config file < explicit flags**. See
[`configs/README.md`](configs/README.md) for the full knob reference and
the three preset profiles.

## Verifier

The verifier scrapes EDM `/metrics` every `--report-interval` and tracks:

| EDM metric                         | What it tells you                              |
|------------------------------------|------------------------------------------------|
| `edm_processed_dnstap_total`       | total frames EDM accepted                      |
| `edm_new_qname_queued_total`       | unique unknown qnames published to MQTT (gated by `--disable-mqtt`) |
| `edm_ignored_*_total`              | parse / validation failures (should stay at 0) |
| `edm_cryptopan_lru_hit_total`      | IP pseudonymisation cache hits                 |
| `edm_seen_qname_lru_evicted_total` | new-qname cache pressure                       |

Drift = `sent − edm_processed`. A small positive drift is normal (transit
+ EDM's input channel). Persistent drift means EDM is dropping or backed up.

> **Note on `edm_new_qname_queued_total`:** EDM only increments this counter
> inside the MQTT-publish branch (`pkg/runner/runner.go:2014`). When EDM is
> running with `--disable-mqtt`, the counter stays at 0 even if the
> load-gen sends fresh qnames. To exercise the new-qname path end-to-end
> you need either a local mock MQTT broker or a non-MQTT verification
> signal (parquet output volume, `seen_qname_lru_evicted_total`).

## Pattern catalog

| Pattern    | Generator strategy                                              |
|------------|-----------------------------------------------------------------|
| background | Zipfian sample of a top-domains list (DomCop / Tranco)          |
| wellknown  | Background, with a fraction mutated to fall outside EDM's DAWG  |
| dga        | Random labels modelled on Conficker / Suppobox / Necurs / Pykspa |
| beacon     | One fixed C2 domain at `interval ± jitter`                      |
| fastflux   | Single domain, response answer rotates through an IP pool       |
| dyndns     | Random subdomains under no-ip / duckdns / freedns               |
| exfil      | Encoded-subdomain exfil (dnscat2 / iodine / raw-b32)            |
| exotic     | Background qnames with TXT/CNAME/NULL response payloads         |
| evasion    | Meta-pattern composing dga + exfil + exotic per profile         |

The reserved `*.test.invalid` zone is used for synthetic C2 / exfil targets
so generated names never collide with real-world domains and never
accidentally hit EDM's well-known DAWG.

## Tools

```bash
tools/fetch-tranco.sh                   # → ./tranco-top1m.csv
tools/build-dawg.sh in.csv out.dawg     # wraps dnstapir-cli dawg compile
```

The DAWG built by `build-dawg.sh` is consumed by both EDM
(`--well-known-domains-file`) and the load-gen
(`--well-known-source` flag, or `background.domains_path` in JSON config).
For the wellknown↔unknown ratio knob to be accurate, the load-gen must see
the same domain set EDM compiled into its DAWG.

## Development

```bash
go test -race ./...
```

Tests cover: dnstap envelope shape (golden-byte comparison), DNS payload
round-trip via miekg/dns, in-process Frame Streams round-trip via
`dnstap.NewFrameStreamSockInput` (the same library EDM uses), pattern
generators (each Generator emits N queries that satisfy its predicate —
e.g. DGA labels meet a Shannon-entropy floor), `linkdata/rate` pacing
within tolerance, and the Prometheus expfmt scraper against fixture
metrics.

### Layout

```
cmd/edm-loadgen/      stdlib-flag CLI dispatcher (no cobra/viper)
internal/state/        authoritative state struct + JSON config overlay
internal/dns/          miekg/dns wire-format packer
internal/dnstap/       *dnstap.Dnstap envelope builder
internal/sink/         Frame Streams sender (wraps dnstap.NewSocketWriter)
internal/pattern/      eight Generator implementations
internal/mix/          weighted picker over pattern slots
internal/rate/         linkdata/rate ticker wrapper (atomic *int32 rate)
internal/verify/       /metrics scraper + sent-vs-observed reconciler
internal/producer/     producer goroutine + beacon pump
internal/web/          JaWS UI (page model, atomic int32 binder, server)
configs/               minimal / realistic / chaos JSON presets
tools/                 fetch-tranco.sh, build-dawg.sh
```

## Known limitations

- `edm_new_qname_queued_total` stays at 0 when EDM is run with
  `--disable-mqtt` (see Verifier section above).
- `--listen` defaults to `:8090` and binds all interfaces; the UI has no
  authentication. Intended for local dev only.
- Fast-flux's `churn_per_min` and dyn-DNS's `update_interval` are advisory;
  the current implementation rotates IPs / providers per query.
- The JaWS UI does not (yet) expose pattern-specific knobs (DGA family,
  exfil tool, …) — only mix weights, QPS, and target. JSON config covers
  the full set.

## License

BSD-2-Clause, matching the rest of the dnstapir org.
