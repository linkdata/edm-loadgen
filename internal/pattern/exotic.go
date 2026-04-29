package pattern

import (
	"context"
	"math/rand/v2"
	"net/netip"
	"strings"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Exotic emits queries that look like normal background traffic but with
// answers carried in TXT, CNAME, or NULL records of configurable size. EDM
// only inspects the question section, so this primarily verifies it does not
// choke on non-A responses.
type Exotic struct {
	st         *state.State
	rng        *rand.Rand
	bg         *Background
	clientPool []netip.Addr
}

// NewExotic borrows the background generator's domain pool to keep qnames
// realistic (only the answer payload is unusual).
func NewExotic(st *state.State, bg *Background) *Exotic {
	rng := rand.New(rand.NewPCG(0xe10c, uint64(nowFunc().UnixNano())))
	return &Exotic{
		st:         st,
		rng:        rng,
		bg:         bg,
		clientPool: makeClientPool(rng, 32),
	}
}

// Name returns the pattern identifier.
func (e *Exotic) Name() string { return "exotic" }

// Next picks a domain from the background list and an exotic record type.
func (e *Exotic) Next(ctx context.Context) (q Query, err error) {
	domains := e.bg.Domains()
	qname := domains[e.rng.IntN(len(domains))]

	e.st.RLock()
	chosen := "TXT"
	if len(e.st.Exotic.RecordTypes) > 0 {
		chosen = e.st.Exotic.RecordTypes[e.rng.IntN(len(e.st.Exotic.RecordTypes))]
	}
	lo, hi := e.st.Exotic.PayloadBytesMin, e.st.Exotic.PayloadBytesMax
	e.st.RUnlock()
	if hi <= lo {
		hi = lo + 1
	}
	size := lo + e.rng.IntN(hi-lo)

	chosen = strings.ToUpper(chosen)
	var qt uint16
	var ans []mdns.RR
	hdr := mdns.RR_Header{Name: mdns.Fqdn(qname), Class: mdns.ClassINET, Ttl: 60}
	switch chosen {
	case "CNAME":
		qt = mdns.TypeCNAME
		hdr.Rrtype = qt
		ans = []mdns.RR{&mdns.CNAME{Hdr: hdr, Target: mdns.Fqdn(randLabel(e.rng, size%63+1) + "." + qname)}}
	case "NULL":
		qt = mdns.TypeNULL
		hdr.Rrtype = qt
		raw := make([]byte, size)
		for i := range raw {
			raw[i] = byte(e.rng.IntN(256))
		}
		ans = []mdns.RR{&mdns.NULL{Hdr: hdr, Data: string(raw)}}
	default:
		qt = mdns.TypeTXT
		hdr.Rrtype = qt
		ans = []mdns.RR{&mdns.TXT{Hdr: hdr, Txt: []string{randLabel(e.rng, size)}}}
	}

	q = Query{
		QName:   qname,
		QType:   qt,
		SrcIP:   e.clientPool[e.rng.IntN(len(e.clientPool))],
		DstIP:   resolverIP,
		Answers: ans,
		At:      nowFunc(),
	}
	return
}

func randLabel(rng *rand.Rand, n int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	if n <= 0 {
		n = 1
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = alpha[rng.IntN(len(alpha))]
	}
	return string(b)
}
