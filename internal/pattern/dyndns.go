package pattern

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"net/netip"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// DynDNS emits queries to random subdomains under a pool of dynamic-DNS
// providers. Each query has a fresh random label so it lands in EDM's
// new-qname path.
type DynDNS struct {
	st  *state.State
	rng *rand.Rand
	src netip.Addr
}

// NewDynDNS returns a generator. Provider list is read live from state.
func NewDynDNS(st *state.State) *DynDNS {
	rng := rand.New(rand.NewPCG(0xdd, uint64(nowFunc().UnixNano())))
	return &DynDNS{
		st:  st,
		rng: rng,
		src: netip.AddrFrom4([4]byte{198, 51, 100, 77}),
	}
}

// Name returns the pattern identifier.
func (d *DynDNS) Name() string { return "dyndns" }

// Next picks a provider and prepends a random hex label.
func (d *DynDNS) Next(ctx context.Context) (q Query, err error) {
	d.st.RLock()
	provider := ""
	if len(d.st.DynDNS.Providers) > 0 {
		provider = d.st.DynDNS.Providers[d.rng.IntN(len(d.st.DynDNS.Providers))]
	}
	d.st.RUnlock()
	if provider == "" {
		provider = "no-ip.com"
	}

	var raw [4]byte
	for i := range raw {
		raw[i] = byte(d.rng.IntN(256))
	}
	qname := hex.EncodeToString(raw[:]) + "." + provider

	q = Query{
		QName:   qname,
		QType:   mdns.TypeA,
		SrcIP:   d.src,
		DstIP:   resolverIP,
		Answers: synthAnswer(qname, mdns.TypeA, d.rng),
		At:      nowFunc(),
	}
	return
}
