# Configs

Three preset profiles for `edm-loadgen run --config <file>` and
`edm-loadgen serve --config <file>`. JSON files are intentionally flat-data
and partial — any field not present falls through to the in-binary defaults
(`internal/state.New`). Per-knob notes live here because JSON has no
inline comments.

## minimal.json

Background traffic only, 200 qps. Smallest sanity check that EDM is
ingesting frames through the load-gen wire path. Equivalent to
`./edm-loadgen run --qps 200 --mix.dga 0 --mix.beacon 0 ...`.

## realistic.json

500 qps with a low-tier malicious-traffic mix on top of dominant background.
Models a "lightly compromised" resolver: occasional DGA, periodic beaconing,
some exfil bursts, with rare fast-flux and dyn-DNS abuse. Default knobs for
each pattern match the in-binary defaults.

## chaos.json

2000 qps with every pattern turned up. Designed to exercise every EDM
counter:

- High `mix.fastflux` + small TTL + wide IP pool → many distinct response
  IPs → `edm_cryptopan_lru_evicted_total` rises.
- `exfil.payload_bytes` 1 MiB at 500 burst-qps → dense flood of unique
  qnames → `edm_seen_qname_lru_evicted_total` rises.
- `dga` 15 % share → high cardinality of new qnames.
- `evasion.profile = heavy` → emphasis on exfil + exotic.

`ignored_*` counters should stay at 0 in any profile — those firing means
the load-gen emitted malformed DNS, not that EDM is dropping legitimate
traffic.

## Knob reference

### Top-level

| Field             | Type                | Notes                                       |
|-------------------|---------------------|---------------------------------------------|
| `target`          | string              | tcp://host:port or unix:///path             |
| `metrics_url`     | string              | EDM /metrics URL                            |
| `qps`             | integer             | global rate cap (linkdata/rate ticker)      |
| `report_interval` | duration string     | verifier scrape cadence ("5s")              |

### `mix` — relative weights, normalised by the picker

`background`, `wellknown`, `dga`, `beacon`, `fastflux`, `dyndns`, `exfil`,
`exotic`, `evasion`. Each is an integer; ratios are what matter.

`beacon` has its own time-driven cadence (own goroutine, not the rate
ticker), so its weight controls only whether beacons fire at all (zero =
silent, anything else = on).

### `background`

| Field          | Notes                                                    |
|----------------|----------------------------------------------------------|
| `zipf_alpha`   | Zipfian skew over the domain list (1.0–1.5)              |
| `qtype_dist`   | weights keyed by qtype name (A, AAAA, HTTPS, …)          |
| `domains_path` | path to a CSV/text top-domains list (see tools/)         |

### `wellknown`

| Field      | Notes                                                              |
|------------|--------------------------------------------------------------------|
| `fraction` | `[0,1]` — share of background queries that hit EDM's DAWG          |

### `dga`

| Field         | Notes                                                |
|---------------|------------------------------------------------------|
| `family`      | `conficker` / `suppobox` / `necurs` / `pykspa` / `mixed` |
| `length_min`  | label length lower bound                             |
| `length_max`  | label length upper bound                             |
| `tlds`        | list of TLDs to round-robin                          |

### `beacon`

| Field         | Notes                                                     |
|---------------|-----------------------------------------------------------|
| `domain`      | fixed C2 domain — use `*.test.invalid`                    |
| `interval`    | duration string ("60s", "5m")                             |
| `jitter_pct`  | `[0,1]`, ± fraction of interval                           |

### `fastflux`

| Field           | Notes                                                |
|-----------------|------------------------------------------------------|
| `domain`        | fixed flux domain                                    |
| `ip_pool_cidr`  | CIDR (≤ 4096 hosts; wider prefixes are truncated)    |
| `ttl_secs`      | answer TTL                                           |
| `churn_per_min` | advisory; current implementation rotates every query |

### `dyndns`

| Field             | Notes                                          |
|-------------------|------------------------------------------------|
| `providers`       | list of provider zones                         |
| `update_interval` | duration string (advisory; not yet implemented as a separate cadence) |

### `exfil`

| Field              | Notes                                              |
|--------------------|----------------------------------------------------|
| `tool`             | `dnscat2` (hex) / `iodine` (gzip+base32) / `raw-b32` |
| `domain`           | exfiltration zone                                  |
| `payload_bytes`    | total bytes per session (capped at 1 MiB)          |
| `burst_qps`        | advisory; honoured by the producer's main rate cap |
| `session_interval` | gap between sessions (advisory)                    |

### `exotic`

| Field                | Notes                                            |
|----------------------|--------------------------------------------------|
| `record_types`       | subset of `TXT` / `CNAME` / `NULL`                |
| `payload_bytes_min`  | answer payload lower bound                        |
| `payload_bytes_max`  | answer payload upper bound                        |

### `evasion`

| Field      | Notes                                                |
|------------|------------------------------------------------------|
| `profile`  | `off` / `light` / `medium` / `heavy` — preset weights |
