package verify

import (
	"sync/atomic"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Report is a small struct describing how many frames the load-gen sent vs.
// what EDM observed, plus interpreted deltas.
type Report struct {
	SentTotal       int64
	EDMProcessed    int64
	Drift           int64 // SentTotal - EDMProcessed (positive = lag)
	EDMNewQname     int64
	EDMIgnoredTotal int64
	PerPattern      map[string]int64
}

// Reconcile snapshots the current send-side and observed counters into a
// Report. Caller must hold no locks; all reads are atomic.
func Reconcile(st *state.State) Report {
	r := Report{
		SentTotal:       atomic.LoadInt64(&st.Sent.Total),
		EDMProcessed:    atomic.LoadInt64(&st.Observed.EDMProcessed),
		EDMNewQname:     atomic.LoadInt64(&st.Observed.EDMNewQname),
		EDMIgnoredTotal: atomic.LoadInt64(&st.Observed.EDMIgnoredTotal),
		PerPattern: map[string]int64{
			"background": atomic.LoadInt64(&st.Sent.Background),
			"wellknown":  atomic.LoadInt64(&st.Sent.WellKnown),
			"dga":        atomic.LoadInt64(&st.Sent.DGA),
			"beacon":     atomic.LoadInt64(&st.Sent.Beacon),
			"fastflux":   atomic.LoadInt64(&st.Sent.FastFlux),
			"dyndns":     atomic.LoadInt64(&st.Sent.DynDNS),
			"exfil":      atomic.LoadInt64(&st.Sent.Exfil),
			"exotic":     atomic.LoadInt64(&st.Sent.Exotic),
			"evasion":    atomic.LoadInt64(&st.Sent.Evasion),
		},
	}
	r.Drift = r.SentTotal - r.EDMProcessed
	return r
}
