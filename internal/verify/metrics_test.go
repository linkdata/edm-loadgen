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

func TestApplySnapshot(t *testing.T) {
	st := state.New()
	applySnapshot(st, Snapshot{
		Processed:    100,
		NewQname:     20,
		IgnoredTotal: 5,
	})
	r := Reconcile(st)
	if r.EDMProcessed != 100 {
		t.Errorf("EDMProcessed = %d", r.EDMProcessed)
	}
	if r.Drift != -100 {
		t.Errorf("Drift = %d, want -100 (sent=0)", r.Drift)
	}
}
