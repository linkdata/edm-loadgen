// Package verify scrapes EDM /metrics, parses the Prometheus exposition
// format, and reconciles the result against the load-gen's send-side
// counters.
package verify

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Snapshot is a typed view of the EDM metrics we care about. Zero-valued
// fields mean the metric was missing from the scrape.
type Snapshot struct {
	At                time.Time
	Processed         int64
	NewQname          int64
	IgnoredTotal      int64
	CryptopanHits     int64
	CryptopanEvict    int64
	SeenQnameEvict    int64
}

// Scraper polls a URL and writes parsed snapshots into a State.
//
// The scraper keeps a private baseline snapshot taken on its first successful
// scrape. All values written into State.Observed are reported relative to that
// baseline, so EDM having processed N frames before the load-gen attached
// shows up as 0 (not N) on first paint and drift starts honest.
//
// EDM restarts are detected as any counter going backwards; the baseline
// resets to the new snapshot so we count from the new EDM lifetime forward.
type Scraper struct {
	url    string
	client *http.Client

	mu          sync.Mutex
	baselineSet bool
	baseline    Snapshot
}

// NewScraper returns a Scraper that GETs url with a sane timeout.
func NewScraper(url string) *Scraper {
	return &Scraper{
		url:    url,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Once performs a single scrape and returns the parsed snapshot.
func (s *Scraper) Once(ctx context.Context) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify: build request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify: get %s: %w", s.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("verify: %s returned %s", s.url, resp.Status)
	}

	// Use UTF8 validation explicitly. The zero value of TextParser uses
	// UnsetValidation which panics inside IsValidMetricName.
	p := expfmt.NewTextParser(model.UTF8Validation)
	families, err := p.TextToMetricFamilies(resp.Body)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify: parse metrics: %w", err)
	}

	snap := Snapshot{At: time.Now()}
	get := func(name string) int64 {
		mf, ok := families[name]
		if !ok || len(mf.Metric) == 0 {
			return 0
		}
		v := mf.Metric[0]
		switch {
		case v.Counter != nil:
			return int64(v.Counter.GetValue())
		case v.Gauge != nil:
			return int64(v.Gauge.GetValue())
		}
		return 0
	}
	snap.Processed = get("edm_processed_dnstap_total")
	snap.NewQname = get("edm_new_qname_queued_total")
	snap.CryptopanHits = get("edm_cryptopan_lru_hit_total")
	snap.CryptopanEvict = get("edm_cryptopan_lru_evicted_total")
	snap.SeenQnameEvict = get("edm_seen_qname_lru_evicted_total")
	// Sum the per-reason ignored counters into a single field.
	for _, name := range []string{
		"edm_ignored_client_ip_total",
		"edm_ignored_client_ip_error_total",
		"edm_ignored_dns_parse_error_total",
		"edm_ignored_empty_question_section_total",
		"edm_ignored_invalid_question_name_total",
		"edm_ignored_question_name_total",
	} {
		snap.IgnoredTotal += get(name)
	}
	return snap, nil
}

// Run polls every interval (or state.ReportInterval if interval is zero) and
// writes the parsed snapshot back into st.Observed atomically. Returns when
// ctx is cancelled.
func (s *Scraper) Run(ctx context.Context, st *state.State, interval time.Duration) error {
	if interval <= 0 {
		st.RLock()
		interval = st.ReportInterval
		st.RUnlock()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	// Do an initial scrape immediately so the UI has data on first paint.
	if snap, err := s.Once(ctx); err == nil {
		s.apply(st, snap)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			snap, err := s.Once(ctx)
			if err != nil {
				continue // transient errors are tolerated
			}
			s.apply(st, snap)
		}
	}
}

// apply rebases snap against the scraper's baseline (capturing the baseline
// on the first call, or resetting it if any counter has gone backwards) and
// writes the relative values into st.Observed.
func (s *Scraper) apply(st *state.State, snap Snapshot) {
	s.mu.Lock()
	if !s.baselineSet || snapshotWentBackwards(snap, s.baseline) {
		s.baseline = snap
		s.baselineSet = true
	}
	rel := snap.relativeTo(s.baseline)
	s.mu.Unlock()

	atomic.StoreInt64(&st.Observed.EDMProcessed, rel.Processed)
	atomic.StoreInt64(&st.Observed.EDMNewQname, rel.NewQname)
	atomic.StoreInt64(&st.Observed.EDMIgnoredTotal, rel.IgnoredTotal)
	atomic.StoreInt64(&st.Observed.EDMCryptopanHits, rel.CryptopanHits)
	atomic.StoreInt64(&st.Observed.EDMCryptopanEvict, rel.CryptopanEvict)
	atomic.StoreInt64(&st.Observed.EDMSeenQnameEvict, rel.SeenQnameEvict)
}

// relativeTo returns a snapshot representing snap's counter values relative
// to base (i.e. each field is snap.X - base.X). Negative results are clamped
// to zero, which only happens momentarily during EDM restarts before the
// caller resets the baseline.
func (snap Snapshot) relativeTo(base Snapshot) Snapshot {
	clamp := func(a, b int64) int64 {
		if a <= b {
			return 0
		}
		return a - b
	}
	return Snapshot{
		At:             snap.At,
		Processed:      clamp(snap.Processed, base.Processed),
		NewQname:       clamp(snap.NewQname, base.NewQname),
		IgnoredTotal:   clamp(snap.IgnoredTotal, base.IgnoredTotal),
		CryptopanHits:  clamp(snap.CryptopanHits, base.CryptopanHits),
		CryptopanEvict: clamp(snap.CryptopanEvict, base.CryptopanEvict),
		SeenQnameEvict: clamp(snap.SeenQnameEvict, base.SeenQnameEvict),
	}
}

// snapshotWentBackwards reports whether any counter in snap is strictly less
// than its baseline counterpart, which is the signal that EDM was restarted
// and counters reset to zero.
func snapshotWentBackwards(snap, base Snapshot) bool {
	return snap.Processed < base.Processed ||
		snap.NewQname < base.NewQname ||
		snap.IgnoredTotal < base.IgnoredTotal ||
		snap.CryptopanHits < base.CryptopanHits ||
		snap.CryptopanEvict < base.CryptopanEvict ||
		snap.SeenQnameEvict < base.SeenQnameEvict
}
