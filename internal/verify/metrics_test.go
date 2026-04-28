package verify

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linkdata/edm-loadgen/internal/state"
)

const fixtureMetrics = `# HELP edm_processed_dnstap_total The total number of processed dnstap packets
# TYPE edm_processed_dnstap_total counter
edm_processed_dnstap_total 1234
# HELP edm_new_qname_queued_total The total number of queued new_qname events
# TYPE edm_new_qname_queued_total counter
edm_new_qname_queued_total 42
# HELP edm_ignored_dns_parse_error_total parse errors
# TYPE edm_ignored_dns_parse_error_total counter
edm_ignored_dns_parse_error_total 3
# HELP edm_ignored_invalid_question_name_total bad names
# TYPE edm_ignored_invalid_question_name_total counter
edm_ignored_invalid_question_name_total 7
# HELP edm_cryptopan_lru_hit_total hits
# TYPE edm_cryptopan_lru_hit_total counter
edm_cryptopan_lru_hit_total 99
`

func TestScraperParsesEDMMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(fixtureMetrics))
	}))
	defer srv.Close()

	sc := NewScraper(srv.URL)
	snap, err := sc.Once(t.Context())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if snap.Processed != 1234 {
		t.Errorf("Processed = %d, want 1234", snap.Processed)
	}
	if snap.NewQname != 42 {
		t.Errorf("NewQname = %d, want 42", snap.NewQname)
	}
	if snap.IgnoredTotal != 10 {
		t.Errorf("IgnoredTotal = %d, want 10 (3+7)", snap.IgnoredTotal)
	}
	if snap.CryptopanHits != 99 {
		t.Errorf("CryptopanHits = %d, want 99", snap.CryptopanHits)
	}
}

// TestScraperBaseline confirms that the first scrape establishes a baseline
// and subsequent scrapes report values relative to it. EDM having processed
// frames before the load-gen attached must not show up as phantom drift.
func TestScraperBaseline(t *testing.T) {
	st := state.New()
	sc := NewScraper("http://unused")

	// First scrape: EDM had been running and accumulated counters.
	sc.apply(st, Snapshot{Processed: 1_000_000, NewQname: 200, CryptopanHits: 5_000})
	r := Reconcile(st)
	if r.EDMProcessed != 0 {
		t.Errorf("after baseline EDMProcessed = %d, want 0", r.EDMProcessed)
	}
	if r.EDMNewQname != 0 {
		t.Errorf("after baseline EDMNewQname = %d, want 0", r.EDMNewQname)
	}
	if r.Drift != 0 {
		t.Errorf("after baseline Drift = %d, want 0", r.Drift)
	}

	// Second scrape: EDM processed 350 more frames and saw 12 new qnames.
	sc.apply(st, Snapshot{Processed: 1_000_350, NewQname: 212, CryptopanHits: 5_700})
	r = Reconcile(st)
	if r.EDMProcessed != 350 {
		t.Errorf("delta EDMProcessed = %d, want 350", r.EDMProcessed)
	}
	if r.EDMNewQname != 12 {
		t.Errorf("delta EDMNewQname = %d, want 12", r.EDMNewQname)
	}
}

// TestScraperRebaselinesOnRestart confirms that an apparent counter
// regression (which only happens when EDM is restarted and counters reset
// to zero) re-anchors the baseline so post-restart progress is still
// visible.
func TestScraperRebaselinesOnRestart(t *testing.T) {
	st := state.New()
	sc := NewScraper("http://unused")

	sc.apply(st, Snapshot{Processed: 1000, NewQname: 50}) // baseline
	sc.apply(st, Snapshot{Processed: 1500, NewQname: 70}) // +500 / +20
	r := Reconcile(st)
	if r.EDMProcessed != 500 {
		t.Fatalf("pre-restart EDMProcessed = %d, want 500", r.EDMProcessed)
	}

	// EDM restarts: counters reset to 0 then accumulate 42 frames.
	sc.apply(st, Snapshot{Processed: 0})  // restart detected, rebaseline
	sc.apply(st, Snapshot{Processed: 42}) // 42 - 0
	r = Reconcile(st)
	if r.EDMProcessed != 42 {
		t.Errorf("post-restart EDMProcessed = %d, want 42", r.EDMProcessed)
	}
}
