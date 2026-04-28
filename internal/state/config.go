package state

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Config is the on-disk shape of a load-gen profile. It mirrors a subset of
// State's knobs in JSON-friendly types (durations as Go duration strings).
//
// Apply overlays a Config onto a State: only fields that are present in the
// JSON file overwrite the corresponding State fields, so partial configs work
// (a config that only sets QPS leaves all pattern knobs at their defaults).
type Config struct {
	Target         *string `json:"target,omitempty"`
	MetricsURL     *string `json:"metrics_url,omitempty"`
	QPS            *int32  `json:"qps,omitempty"`
	ReportInterval *string `json:"report_interval,omitempty"`

	Mix *MixConfig `json:"mix,omitempty"`

	Background *BackgroundConfig `json:"background,omitempty"`
	WellKnown  *WellKnownConfig  `json:"wellknown,omitempty"`
	DGA        *DGAConfig        `json:"dga,omitempty"`
	Beacon     *BeaconConfig     `json:"beacon,omitempty"`
	FastFlux   *FastFluxConfig   `json:"fastflux,omitempty"`
	DynDNS     *DynDNSConfig     `json:"dyndns,omitempty"`
	Exfil      *ExfilConfig      `json:"exfil,omitempty"`
	Exotic     *ExoticConfig     `json:"exotic,omitempty"`
	Evasion    *EvasionConfig    `json:"evasion,omitempty"`
}

// MixConfig is the JSON-side mirror of Mix.
type MixConfig struct {
	Background *int32 `json:"background,omitempty"`
	WellKnown  *int32 `json:"wellknown,omitempty"`
	DGA        *int32 `json:"dga,omitempty"`
	Beacon     *int32 `json:"beacon,omitempty"`
	FastFlux   *int32 `json:"fastflux,omitempty"`
	DynDNS     *int32 `json:"dyndns,omitempty"`
	Exfil      *int32 `json:"exfil,omitempty"`
	Exotic     *int32 `json:"exotic,omitempty"`
	Evasion    *int32 `json:"evasion,omitempty"`
}

// BackgroundConfig mirrors BackgroundKnobs. QtypeDist takes string keys for
// JSON friendliness ("A", "AAAA", ...).
type BackgroundConfig struct {
	ZipfAlpha   *float64       `json:"zipf_alpha,omitempty"`
	QTypeDist   map[string]int `json:"qtype_dist,omitempty"`
	DomainsPath *string        `json:"domains_path,omitempty"`
}

// WellKnownConfig mirrors WellKnownKnobs.
type WellKnownConfig struct {
	Fraction *float64 `json:"fraction,omitempty"`
}

// DGAConfig mirrors DGAKnobs.
type DGAConfig struct {
	Family    *string  `json:"family,omitempty"`
	LengthMin *int     `json:"length_min,omitempty"`
	LengthMax *int     `json:"length_max,omitempty"`
	TLDs      []string `json:"tlds,omitempty"`
}

// BeaconConfig mirrors BeaconKnobs.
type BeaconConfig struct {
	Domain    *string  `json:"domain,omitempty"`
	Interval  *string  `json:"interval,omitempty"` // duration string
	JitterPct *float64 `json:"jitter_pct,omitempty"`
}

// FastFluxConfig mirrors FastFluxKnobs.
type FastFluxConfig struct {
	Domain      *string `json:"domain,omitempty"`
	IPPoolCIDR  *string `json:"ip_pool_cidr,omitempty"`
	TTLSecs     *int    `json:"ttl_secs,omitempty"`
	ChurnPerMin *int    `json:"churn_per_min,omitempty"`
}

// DynDNSConfig mirrors DynDNSKnobs.
type DynDNSConfig struct {
	Providers      []string `json:"providers,omitempty"`
	UpdateInterval *string  `json:"update_interval,omitempty"`
}

// ExfilConfig mirrors ExfilKnobs.
type ExfilConfig struct {
	Tool            *string `json:"tool,omitempty"`
	Domain          *string `json:"domain,omitempty"`
	PayloadBytes    *int    `json:"payload_bytes,omitempty"`
	BurstQPS        *int    `json:"burst_qps,omitempty"`
	SessionInterval *string `json:"session_interval,omitempty"`
}

// ExoticConfig mirrors ExoticKnobs.
type ExoticConfig struct {
	RecordTypes     []string `json:"record_types,omitempty"`
	PayloadBytesMin *int     `json:"payload_bytes_min,omitempty"`
	PayloadBytesMax *int     `json:"payload_bytes_max,omitempty"`
}

// EvasionConfig mirrors EvasionKnobs.
type EvasionConfig struct {
	Profile *string `json:"profile,omitempty"`
}

// LoadConfigFile reads path and parses it as a Config.
func LoadConfigFile(path string) (cfg Config, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		err = fmt.Errorf("config: read %s: %w", path, err)
		return
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&cfg); err != nil {
		err = fmt.Errorf("config: parse %s: %w", path, err)
	}
	return
}

// Apply overlays cfg onto s. Fields that are nil in cfg leave s unchanged.
//
// The state mutex is taken for non-atomic field updates; atomic int32 fields
// (QPS, Mix.*) are updated via atomic.StoreInt32 outside the mutex, matching
// how the rest of the codebase reads them.
func (cfg Config) Apply(s *State) error {
	s.Lock()
	defer s.Unlock()

	if cfg.Target != nil {
		s.Target = *cfg.Target
	}
	if cfg.MetricsURL != nil {
		s.MetricsURL = *cfg.MetricsURL
	}
	if cfg.ReportInterval != nil {
		d, err := time.ParseDuration(*cfg.ReportInterval)
		if err != nil {
			return fmt.Errorf("config: report_interval: %w", err)
		}
		s.ReportInterval = d
	}
	if cfg.QPS != nil {
		atomic.StoreInt32(&s.QPS, *cfg.QPS)
	}
	if cfg.Mix != nil {
		if cfg.Mix.Background != nil { atomic.StoreInt32(&s.Mix.Background, *cfg.Mix.Background) }
		if cfg.Mix.WellKnown != nil { atomic.StoreInt32(&s.Mix.WellKnown, *cfg.Mix.WellKnown) }
		if cfg.Mix.DGA != nil { atomic.StoreInt32(&s.Mix.DGA, *cfg.Mix.DGA) }
		if cfg.Mix.Beacon != nil { atomic.StoreInt32(&s.Mix.Beacon, *cfg.Mix.Beacon) }
		if cfg.Mix.FastFlux != nil { atomic.StoreInt32(&s.Mix.FastFlux, *cfg.Mix.FastFlux) }
		if cfg.Mix.DynDNS != nil { atomic.StoreInt32(&s.Mix.DynDNS, *cfg.Mix.DynDNS) }
		if cfg.Mix.Exfil != nil { atomic.StoreInt32(&s.Mix.Exfil, *cfg.Mix.Exfil) }
		if cfg.Mix.Exotic != nil { atomic.StoreInt32(&s.Mix.Exotic, *cfg.Mix.Exotic) }
		if cfg.Mix.Evasion != nil { atomic.StoreInt32(&s.Mix.Evasion, *cfg.Mix.Evasion) }
	}
	if c := cfg.Background; c != nil {
		if c.ZipfAlpha != nil {
			s.Background.ZipfAlpha = *c.ZipfAlpha
		}
		if c.DomainsPath != nil {
			s.Background.DomainsPath = *c.DomainsPath
		}
		if len(c.QTypeDist) > 0 {
			dist := make(map[uint16]int, len(c.QTypeDist))
			for k, v := range c.QTypeDist {
				qt, ok := qtypeByName(k)
				if !ok {
					return fmt.Errorf("config: background.qtype_dist: unknown qtype %q", k)
				}
				dist[qt] = v
			}
			s.Background.QTypeDist = dist
		}
	}
	if c := cfg.WellKnown; c != nil && c.Fraction != nil {
		s.WellKnown.Fraction = *c.Fraction
	}
	if c := cfg.DGA; c != nil {
		if c.Family != nil { s.DGA.Family = *c.Family }
		if c.LengthMin != nil { s.DGA.LengthMin = *c.LengthMin }
		if c.LengthMax != nil { s.DGA.LengthMax = *c.LengthMax }
		if c.TLDs != nil { s.DGA.TLDs = append([]string(nil), c.TLDs...) }
	}
	if c := cfg.Beacon; c != nil {
		if c.Domain != nil { s.Beacon.Domain = *c.Domain }
		if c.Interval != nil {
			d, err := time.ParseDuration(*c.Interval)
			if err != nil { return fmt.Errorf("config: beacon.interval: %w", err) }
			s.Beacon.Interval = d
		}
		if c.JitterPct != nil { s.Beacon.JitterPct = *c.JitterPct }
	}
	if c := cfg.FastFlux; c != nil {
		if c.Domain != nil { s.FastFlux.Domain = *c.Domain }
		if c.IPPoolCIDR != nil { s.FastFlux.IPPoolCIDR = *c.IPPoolCIDR }
		if c.TTLSecs != nil { s.FastFlux.TTLSecs = *c.TTLSecs }
		if c.ChurnPerMin != nil { s.FastFlux.ChurnPerMin = *c.ChurnPerMin }
	}
	if c := cfg.DynDNS; c != nil {
		if c.Providers != nil { s.DynDNS.Providers = append([]string(nil), c.Providers...) }
		if c.UpdateInterval != nil {
			d, err := time.ParseDuration(*c.UpdateInterval)
			if err != nil { return fmt.Errorf("config: dyndns.update_interval: %w", err) }
			s.DynDNS.UpdateInterval = d
		}
	}
	if c := cfg.Exfil; c != nil {
		if c.Tool != nil { s.Exfil.Tool = *c.Tool }
		if c.Domain != nil { s.Exfil.Domain = *c.Domain }
		if c.PayloadBytes != nil { s.Exfil.PayloadBytes = *c.PayloadBytes }
		if c.BurstQPS != nil { s.Exfil.BurstQPS = *c.BurstQPS }
		if c.SessionInterval != nil {
			d, err := time.ParseDuration(*c.SessionInterval)
			if err != nil { return fmt.Errorf("config: exfil.session_interval: %w", err) }
			s.Exfil.SessionInterval = d
		}
	}
	if c := cfg.Exotic; c != nil {
		if c.RecordTypes != nil { s.Exotic.RecordTypes = append([]string(nil), c.RecordTypes...) }
		if c.PayloadBytesMin != nil { s.Exotic.PayloadBytesMin = *c.PayloadBytesMin }
		if c.PayloadBytesMax != nil { s.Exotic.PayloadBytesMax = *c.PayloadBytesMax }
	}
	if c := cfg.Evasion; c != nil && c.Profile != nil {
		s.Evasion.Profile = *c.Profile
	}
	return nil
}

// qtypeByName returns the miekg/dns numeric qtype for an uppercase short name
// like "A", "AAAA", "TXT". Returns ok=false for unrecognised names.
func qtypeByName(name string) (uint16, bool) {
	switch strings.ToUpper(name) {
	case "A":
		return 1, true
	case "NS":
		return 2, true
	case "CNAME":
		return 5, true
	case "PTR":
		return 12, true
	case "MX":
		return 15, true
	case "TXT":
		return 16, true
	case "AAAA":
		return 28, true
	case "SRV":
		return 33, true
	case "SVCB":
		return 64, true
	case "HTTPS":
		return 65, true
	case "NULL":
		return 10, true
	default:
		return 0, false
	}
}
