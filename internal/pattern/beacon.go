package pattern

import (
	"context"
	"math/rand/v2"
	"net/netip"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Beacon emits a query to a single fixed C2-like domain at the configured
// interval (with jitter). The pattern's Next blocks until the next scheduled
// emission, observing ctx for cancellation.
//
// Because Beacon is naturally sub-1Hz, it is excluded from the linkdata/rate
// ticker and its emissions are pulled into the producer queue directly. The
// per-second QPS knob has no effect on this pattern.
type Beacon struct {
	st        *state.State
	rng       *rand.Rand
	src       netip.Addr
	firstSent bool
}

// NewBeacon returns a beacon generator. The synthetic client IP is fixed —
// real beacons come from one infected host.
func NewBeacon(st *state.State) *Beacon {
	rng := rand.New(rand.NewPCG(0xbeac0, uint64(nowFunc().UnixNano())))
	return &Beacon{
		st:  st,
		rng: rng,
		// One fixed client in the documentation prefix.
		src: netip.AddrFrom4([4]byte{198, 51, 100, 42}),
	}
}

// Name returns the pattern identifier.
func (b *Beacon) Name() string { return "beacon" }

// Next sleeps for one beacon interval ± jitter, then emits. The first call
// returns a query immediately so we have something to observe right away.
func (b *Beacon) Next(ctx context.Context) (q Query, err error) {
	b.st.RLock()
	domain := b.st.Beacon.Domain
	interval := b.st.Beacon.Interval
	jitter := b.st.Beacon.JitterPct
	b.st.RUnlock()

	// First call: emit immediately so the verifier sees activity within
	// `--report-interval` even on long beacon intervals.
	if !b.firstSent {
		b.firstSent = true
	} else {
		wait := jitterDuration(interval, jitter, b.rng)
		select {
		case <-ctx.Done():
			err = ctx.Err()
			return
		case <-time.After(wait):
		}
	}

	q = Query{
		QName:   domain,
		QType:   mdns.TypeA,
		SrcIP:   b.src,
		DstIP:   resolverIP,
		Answers: synthAnswer(domain, mdns.TypeA, b.rng),
		At:      nowFunc(),
	}
	return
}

// jitterDuration returns d shifted by ±pct (0–1).
func jitterDuration(d time.Duration, pct float64, rng *rand.Rand) time.Duration {
	if pct <= 0 {
		return d
	}
	delta := (rng.Float64()*2 - 1) * pct // [-pct, +pct]
	return time.Duration(float64(d) * (1 + delta))
}
