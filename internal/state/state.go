// Package state holds the authoritative runtime configuration and live
// counters for edm-loadgen. The JaWS UI binds to fields of [State] via
// pointer tags; the headless run mode reads/writes the same struct under
// the same mutex.
//
// QPS and Mix.* are plain int32 fields accessed via sync/atomic — exposing
// their address lets linkdata/rate.NewTicker observe live updates without
// going through the mutex. All other fields require holding mu.
package state

import (
	"sync"
	"sync/atomic"
	"time"
)

// State is the single source of truth for the running load generator.
type State struct {
	mu sync.RWMutex

	// Connection (set at startup, treated read-only after Start).
	Target     string
	MetricsURL string

	// Atomic knobs — accessed via sync/atomic, no mutex needed.
	QPS     int32 // total queries per second
	Running int32 // 0 or 1

	// Mutex-guarded knobs.
	Mix        Mix
	Background BackgroundKnobs
	WellKnown  WellKnownKnobs
	DGA        DGAKnobs
	Beacon     BeaconKnobs
	FastFlux   FastFluxKnobs
	DynDNS     DynDNSKnobs
	Exfil      ExfilKnobs
	Exotic     ExoticKnobs
	Evasion    EvasionKnobs

	// Live counters. Sent is written by the producer; Observed by the
	// verifier; Received by the embedded MQTT broker.
	Sent     Counters
	Observed Counters
	Received ReceivedCounters

	// MQTT holds the embedded-broker config. Empty Listen disables it.
	MQTT MQTTKnobs

	// Verifier scrape cadence.
	ReportInterval time.Duration
}

// MQTTKnobs configures the embedded MQTT broker. Listen="" disables it.
type MQTTKnobs struct {
	Listen   string // e.g. ":8883" or "127.0.0.1:8883"
	KeysDir  string // dir where pki.Ensure reads/writes cert material
	NodeName string // EDM node id; topic prefix and client-id stem
}

// ReceivedCounters tracks messages the embedded broker has received.
// All fields are int64 accessed via sync/atomic so the broker's OnPublish
// hook can write them directly via pointers and UI getters can read them
// without a mutex.
type ReceivedCounters struct {
	Total       int64
	EDMTopic    int64
	Connections int64
}

// Mix holds the relative weights for each pattern. Stored as plain int32 so
// the mixer can read them with atomic.LoadInt32 without taking the state
// mutex.
type Mix struct {
	Background int32
	WellKnown  int32
	DGA        int32
	Beacon     int32
	FastFlux   int32
	DynDNS     int32
	Exfil      int32
	Exotic     int32
	Evasion    int32
}

// BackgroundKnobs configures the realistic background-traffic generator.
type BackgroundKnobs struct {
	ZipfAlpha   float64        // Zipfian skew; 1.0=uniform-ish, 1.4=heavy
	QTypeDist   map[uint16]int // miekg/dns qtype -> relative weight
	DomainsPath string         // path to a CSV/text top-domains list
}

// WellKnownKnobs configures the wellknown↔unknown coin flip.
type WellKnownKnobs struct {
	Fraction float64
}

// DGAKnobs picks a DGA family and length range.
type DGAKnobs struct {
	Family    string
	LengthMin int
	LengthMax int
	TLDs      []string
}

// BeaconKnobs schedules fixed-domain queries with jittered intervals.
type BeaconKnobs struct {
	Domain    string
	Interval  time.Duration
	JitterPct float64
}

// FastFluxKnobs rotates answer IPs for a single domain.
type FastFluxKnobs struct {
	Domain      string
	IPPoolCIDR  string
	TTLSecs     int
	ChurnPerMin int
}

// DynDNSKnobs queries random subdomains under dynamic-DNS providers.
type DynDNSKnobs struct {
	Providers      []string
	UpdateInterval time.Duration
}

// ExfilKnobs drives the encoded-subdomain exfiltration generator.
type ExfilKnobs struct {
	Tool            string
	Domain          string
	PayloadBytes    int
	BurstQPS        int
	SessionInterval time.Duration
}

// ExoticKnobs requests TXT/CNAME/NULL response payloads on otherwise-normal
// background queries.
type ExoticKnobs struct {
	RecordTypes     []string
	PayloadBytesMin int
	PayloadBytesMax int
}

// EvasionKnobs picks a preset profile that combines other patterns.
type EvasionKnobs struct {
	Profile string
}

// Counters tracks per-pattern sent counts plus the EDM-side metric snapshot.
//
// All fields are int64 accessed via sync/atomic so the producer/verifier can
// update them without the state mutex.
type Counters struct {
	Background int64
	WellKnown  int64
	DGA        int64
	Beacon     int64
	FastFlux   int64
	DynDNS     int64
	Exfil      int64
	Exotic     int64
	Evasion    int64
	Total      int64

	// Mirror of EDM /metrics counters. Populated by the verifier.
	EDMProcessed      int64
	EDMNewQname       int64
	EDMIgnoredTotal   int64
	EDMCryptopanHits  int64
	EDMCryptopanEvict int64
	EDMSeenQnameEvict int64
}

// IsRunning returns true if the producer is enabled.
func (s *State) IsRunning() bool { return atomic.LoadInt32(&s.Running) != 0 }

// SetRunning atomically toggles the producer.
func (s *State) SetRunning(on bool) {
	var v int32
	if on {
		v = 1
	}
	atomic.StoreInt32(&s.Running, v)
}

// Locker returns the underlying RWMutex.
func (s *State) Locker() *sync.RWMutex { return &s.mu }

// RLock and friends satisfy the RWLocker shape JaWS bind.New looks for.
func (s *State) RLock()   { s.mu.RLock() }
func (s *State) RUnlock() { s.mu.RUnlock() }
func (s *State) Lock()    { s.mu.Lock() }
func (s *State) Unlock()  { s.mu.Unlock() }

// New returns a State pre-populated with defaults that match the flag
// defaults in cmd/edm-loadgen.
func New() *State {
	s := &State{
		Target:         "tcp://127.0.0.1:53535",
		MetricsURL:     "http://127.0.0.1:2112/metrics",
		ReportInterval: 5 * time.Second,
		Background: BackgroundKnobs{
			ZipfAlpha: 1.2,
			QTypeDist: map[uint16]int{
				1:  54,
				28: 19,
				65: 8,
				15: 1,
				12: 4,
				16: 1,
				2:  1,
				33: 1,
				5:  1,
				64: 10,
			},
		},
		WellKnown: WellKnownKnobs{Fraction: 0.85},
		DGA: DGAKnobs{
			Family:    "mixed",
			LengthMin: 8,
			LengthMax: 24,
			TLDs:      []string{"com", "net", "org", "xyz", "info"},
		},
		Beacon: BeaconKnobs{
			Domain:    "c2.test.invalid",
			Interval:  60 * time.Second,
			JitterPct: 0.10,
		},
		FastFlux: FastFluxKnobs{
			Domain:      "flux.test.invalid",
			IPPoolCIDR:  "203.0.113.0/24",
			TTLSecs:     120,
			ChurnPerMin: 6,
		},
		DynDNS: DynDNSKnobs{
			Providers:      []string{"no-ip.com", "duckdns.org", "freedns.afraid.org"},
			UpdateInterval: 30 * time.Minute,
		},
		Exfil: ExfilKnobs{
			Tool:            "dnscat2",
			Domain:          "ex.test.invalid",
			PayloadBytes:    524288,
			BurstQPS:        200,
			SessionInterval: 10 * time.Minute,
		},
		Exotic: ExoticKnobs{
			RecordTypes:     []string{"TXT", "CNAME", "NULL"},
			PayloadBytesMin: 32,
			PayloadBytesMax: 480,
		},
		Evasion: EvasionKnobs{Profile: "medium"},
	}
	atomic.StoreInt32(&s.QPS, 100)
	atomic.StoreInt32(&s.Mix.Background, 80)
	atomic.StoreInt32(&s.Mix.WellKnown, 0)
	atomic.StoreInt32(&s.Mix.DGA, 5)
	atomic.StoreInt32(&s.Mix.Beacon, 2)
	atomic.StoreInt32(&s.Mix.FastFlux, 1)
	atomic.StoreInt32(&s.Mix.DynDNS, 2)
	atomic.StoreInt32(&s.Mix.Exfil, 5)
	atomic.StoreInt32(&s.Mix.Exotic, 3)
	atomic.StoreInt32(&s.Mix.Evasion, 2)
	return s
}
