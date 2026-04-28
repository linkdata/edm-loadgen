// Package pattern produces synthetic DNS query metadata that the producer
// turns into dnstap.Dnstap envelopes. Each pattern category implements
// [Generator] and reads its knobs live from internal/state.State, so a UI
// knob change takes effect on the next call to Next.
package pattern

import (
	"context"
	"net/netip"
	"time"

	mdns "github.com/miekg/dns"
)

// Query is the minimum information needed to build a CLIENT_RESPONSE
// dnstap envelope plus its embedded DNS payload.
type Query struct {
	QName   string
	QType   uint16
	SrcIP   netip.Addr
	DstIP   netip.Addr
	Answers []mdns.RR
	At      time.Time
}

// Generator emits synthetic queries one at a time.
type Generator interface {
	// Name returns the pattern's stable identifier used in metrics and
	// status output ("background", "dga", ...).
	Name() string
	// Next returns the next synthetic query. ctx is honoured for
	// cancellation; patterns with internal sleeps (e.g. beacon waiting for
	// its interval) must observe ctx.Done().
	Next(ctx context.Context) (Query, error)
}

// resolverIP is the synthetic dst address for non-fastflux patterns. It looks
// like a real public resolver (Cloudflare's 1.1.1.1) — using a documentation
// CIDR here would also work, but a recognisable address is easier to spot in
// EDM's pseudonymised parquet output.
var resolverIP = netip.MustParseAddr("1.1.1.1")

// nowFunc is overridable in tests; production calls time.Now.
var nowFunc = func() time.Time { return time.Now() }
