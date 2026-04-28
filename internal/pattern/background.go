package pattern

import (
	"bufio"
	"context"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"strings"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Background samples a top-domains list with a Zipfian distribution and emits
// queries with a realistic qtype mix. It is the bulk of normal traffic.
type Background struct {
	st       *state.State
	domains  []string
	zipf     *rand.Zipf
	qtypeBag []uint16
	clientPool []netip.Addr
	rng      *rand.Rand
}

// NewBackground loads a domain list into memory and prepares the samplers.
// If the list is empty, Background falls back to a small built-in list.
func NewBackground(st *state.State, domainsPath string) (*Background, error) {
	domains, err := loadDomainList(domainsPath)
	if err != nil {
		return nil, fmt.Errorf("background: load domains: %w", err)
	}
	if len(domains) == 0 {
		domains = []string{
			"example.com", "example.org", "example.net",
			"iana.org", "wikipedia.org",
			"cloudflare.com", "google.com", "github.com",
		}
	}
	st.RLock()
	alpha := st.Background.ZipfAlpha
	bag := buildQtypeBag(st.Background.QTypeDist)
	st.RUnlock()

	rng := rand.New(rand.NewPCG(0, uint64(nowFunc().UnixNano())))
	zipf := rand.NewZipf(rng, alpha, 1.0, uint64(len(domains)-1))
	return &Background{
		st:         st,
		domains:    domains,
		zipf:       zipf,
		qtypeBag:   bag,
		clientPool: makeClientPool(rng, 256),
		rng:        rng,
	}, nil
}

// Name returns the pattern identifier.
func (b *Background) Name() string { return "background" }

// Next picks a domain and qtype.
func (b *Background) Next(ctx context.Context) (q Query, err error) {
	idx := b.zipf.Uint64()
	qname := b.domains[idx]
	qt := b.qtypeBag[b.rng.IntN(len(b.qtypeBag))]
	q = Query{
		QName:   qname,
		QType:   qt,
		SrcIP:   b.clientPool[b.rng.IntN(len(b.clientPool))],
		DstIP:   resolverIP,
		Answers: synthAnswer(qname, qt, b.rng),
		At:      nowFunc(),
	}
	return
}

// Domains exposes the loaded list to siblings (e.g. wellknown's mutator).
func (b *Background) Domains() []string { return b.domains }

func loadDomainList(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Handle "<rank>,<domain>" CSV form (DomCop) and bare-domain text.
		if i := strings.IndexByte(line, ','); i >= 0 {
			line = line[i+1:]
		}
		out = append(out, strings.Trim(line, "\"'"))
	}
	return out, scan.Err()
}

// buildQtypeBag expands a {qtype: weight} map into a flat slice we can index
// uniformly for a weighted pick. Weights of zero or negative are skipped.
func buildQtypeBag(dist map[uint16]int) []uint16 {
	bag := make([]uint16, 0, 100)
	for qt, w := range dist {
		for i := 0; i < w; i++ {
			bag = append(bag, qt)
		}
	}
	if len(bag) == 0 {
		bag = []uint16{mdns.TypeA}
	}
	return bag
}

// makeClientPool generates n synthetic IPv4 client addresses inside the
// documentation prefix 198.51.100.0/24 so they never collide with real users.
func makeClientPool(rng *rand.Rand, n int) []netip.Addr {
	pool := make([]netip.Addr, 0, n)
	seen := map[netip.Addr]struct{}{}
	for len(pool) < n {
		addr := netip.AddrFrom4([4]byte{198, 51, 100, byte(rng.IntN(255) + 1)})
		if _, ok := seen[addr]; ok {
			if len(pool) > 200 {
				break // tiny range: stop instead of looping
			}
			continue
		}
		seen[addr] = struct{}{}
		pool = append(pool, addr)
	}
	return pool
}

// synthAnswer fabricates a single answer RR matching qtype. EDM only inspects
// the question section, but real responses always have at least one answer
// for non-NXDOMAIN cases — we follow the convention to keep parquet output
// readable.
func synthAnswer(qname string, qt uint16, rng *rand.Rand) []mdns.RR {
	hdr := mdns.RR_Header{Name: mdns.Fqdn(qname), Class: mdns.ClassINET, Ttl: 300, Rrtype: qt}
	switch qt {
	case mdns.TypeA:
		ip := netip.AddrFrom4([4]byte{93, 184, byte(rng.IntN(255)), byte(rng.IntN(255))})
		return []mdns.RR{&mdns.A{Hdr: hdr, A: ip.AsSlice()}}
	case mdns.TypeAAAA:
		var b [16]byte
		b[0], b[1] = 0x20, 0x01
		b[2], b[3] = 0x0d, 0xb8
		for i := 8; i < 16; i++ {
			b[i] = byte(rng.IntN(256))
		}
		return []mdns.RR{&mdns.AAAA{Hdr: hdr, AAAA: b[:]}}
	case mdns.TypeCNAME:
		return []mdns.RR{&mdns.CNAME{Hdr: hdr, Target: "alias." + mdns.Fqdn(qname)}}
	case mdns.TypeMX:
		return []mdns.RR{&mdns.MX{Hdr: hdr, Preference: 10, Mx: "mail." + mdns.Fqdn(qname)}}
	case mdns.TypeNS:
		return []mdns.RR{&mdns.NS{Hdr: hdr, Ns: "ns1." + mdns.Fqdn(qname)}}
	case mdns.TypeTXT:
		return []mdns.RR{&mdns.TXT{Hdr: hdr, Txt: []string{"v=spf1 -all"}}}
	case mdns.TypePTR:
		return []mdns.RR{&mdns.PTR{Hdr: hdr, Ptr: "host." + mdns.Fqdn(qname)}}
	case mdns.TypeSRV:
		return []mdns.RR{&mdns.SRV{Hdr: hdr, Priority: 10, Weight: 5, Port: 443, Target: mdns.Fqdn(qname)}}
	case mdns.TypeHTTPS, mdns.TypeSVCB:
		// Minimal SVCB record: priority 1, target ".", no params.
		rr := &mdns.SVCB{Hdr: hdr, Priority: 1, Target: "."}
		return []mdns.RR{rr}
	default:
		return nil
	}
}
