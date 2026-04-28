package pattern

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/netip"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// FastFlux emits queries to a single domain whose answer A record rotates
// through a pool of IPs. EDM pseudonymises every distinct response IP via
// Crypto-PAn, so this pattern primarily exercises that cache.
type FastFlux struct {
	st   *state.State
	rng  *rand.Rand
	pool []netip.Addr
	src  netip.Addr
}

// NewFastFlux precomputes the IP pool from the configured CIDR.
func NewFastFlux(st *state.State) (*FastFlux, error) {
	st.RLock()
	cidr := st.FastFlux.IPPoolCIDR
	st.RUnlock()
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("fastflux: parse cidr %q: %w", cidr, err)
	}
	pool := expandIPv4Prefix(pfx)
	if len(pool) == 0 {
		return nil, fmt.Errorf("fastflux: empty pool for %q", cidr)
	}
	rng := rand.New(rand.NewPCG(0xf10c, uint64(nowFunc().UnixNano())))
	return &FastFlux{
		st:   st,
		rng:  rng,
		pool: pool,
		src:  netip.AddrFrom4([4]byte{198, 51, 100, 99}),
	}, nil
}

// Name returns the pattern identifier.
func (f *FastFlux) Name() string { return "fastflux" }

// Next emits a query whose answer A points to a random pool IP.
func (f *FastFlux) Next(ctx context.Context) (q Query, err error) {
	f.st.RLock()
	domain := f.st.FastFlux.Domain
	ttl := uint32(f.st.FastFlux.TTLSecs)
	f.st.RUnlock()

	ans := f.pool[f.rng.IntN(len(f.pool))]
	rr := &mdns.A{
		Hdr: mdns.RR_Header{Name: mdns.Fqdn(domain), Class: mdns.ClassINET, Ttl: ttl, Rrtype: mdns.TypeA},
		A:   ans.AsSlice(),
	}
	q = Query{
		QName:   domain,
		QType:   mdns.TypeA,
		SrcIP:   f.src,
		DstIP:   resolverIP,
		Answers: []mdns.RR{rr},
		At:      nowFunc(),
	}
	return
}

// expandIPv4Prefix returns every host address in pfx, capped at 4096 to keep
// memory bounded for /20 and wider prefixes.
func expandIPv4Prefix(pfx netip.Prefix) []netip.Addr {
	if !pfx.Addr().Is4() {
		return nil
	}
	const cap = 4096
	out := make([]netip.Addr, 0, 256)
	for a := pfx.Masked().Addr(); pfx.Contains(a) && len(out) < cap; a = a.Next() {
		out = append(out, a)
	}
	return out
}
