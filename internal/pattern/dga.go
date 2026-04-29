package pattern

import (
	"context"
	"math/rand/v2"
	"net/netip"
	"strings"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// DGA generates random domain names that resemble several known
// domain-generation-algorithm families. All names are guaranteed to land in
// EDM's new-qname path.
type DGA struct {
	st         *state.State
	rng        *rand.Rand
	clientPool []netip.Addr
}

// NewDGA returns a generator. Family / length / TLDs are read from state on
// each call so the UI can change them live.
func NewDGA(st *state.State) *DGA {
	rng := rand.New(rand.NewPCG(0xd6a, uint64(nowFunc().UnixNano())))
	return &DGA{
		st:         st,
		rng:        rng,
		clientPool: makeClientPool(rng, 64),
	}
}

// Name returns the pattern identifier.
func (d *DGA) Name() string { return "dga" }

// Next builds a random qname per the configured family.
func (d *DGA) Next(ctx context.Context) (q Query, err error) {
	d.st.RLock()
	family := d.st.DGA.Family
	lo, hi := d.st.DGA.LengthMin, d.st.DGA.LengthMax
	tld := "com"
	if len(d.st.DGA.TLDs) > 0 {
		tld = d.st.DGA.TLDs[d.rng.IntN(len(d.st.DGA.TLDs))]
	}
	d.st.RUnlock()
	if hi <= lo {
		hi = lo + 1
	}

	var label string
	switch family {
	case "suppobox":
		label = suppobox(d.rng)
	case "necurs":
		label = necursLike(d.rng, lo, hi)
	case "pykspa":
		label = pykspaLike(d.rng, lo, hi)
	case "conficker":
		label = conficker(d.rng, lo, hi)
	default: // "mixed" or unknown
		switch d.rng.IntN(4) {
		case 0:
			label = suppobox(d.rng)
		case 1:
			label = necursLike(d.rng, lo, hi)
		case 2:
			label = pykspaLike(d.rng, lo, hi)
		default:
			label = conficker(d.rng, lo, hi)
		}
	}
	qname := label + "." + tld

	q = Query{
		QName:   qname,
		QType:   mdns.TypeA,
		SrcIP:   d.clientPool[d.rng.IntN(len(d.clientPool))],
		DstIP:   resolverIP,
		Answers: synthAnswer(qname, mdns.TypeA, d.rng),
		At:      nowFunc(),
	}
	return
}

// conficker emits high-entropy lowercase alphanumeric, typical of early DGAs.
func conficker(rng *rand.Rand, lo, hi int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	n := lo + rng.IntN(hi-lo)
	b := make([]byte, n)
	for i := range b {
		b[i] = alpha[rng.IntN(len(alpha))]
	}
	return string(b)
}

// necursLike: lowercase letters only, often longer (16–24 chars).
func necursLike(rng *rand.Rand, lo, hi int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz"
	if lo < 16 {
		lo = 16
	}
	if hi <= lo {
		hi = lo + 4
	}
	n := lo + rng.IntN(hi-lo)
	b := make([]byte, n)
	for i := range b {
		b[i] = alpha[rng.IntN(len(alpha))]
	}
	return string(b)
}

// pykspaLike: alternating consonant/vowel-ish, looks pronounceable.
func pykspaLike(rng *rand.Rand, lo, hi int) string {
	const cons = "bcdfghjklmnpqrstvwxz"
	const vow = "aeiouy"
	n := lo + rng.IntN(hi-lo)
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			b[i] = cons[rng.IntN(len(cons))]
		} else {
			b[i] = vow[rng.IntN(len(vow))]
		}
	}
	return string(b)
}

// suppoboxWords: a tiny embedded wordlist. The real Suppobox family ships
// with thousands; for load-gen "looks like a 2-word concat" is enough.
var suppoboxWords = []string{
	"better", "people", "during", "morning", "nothing", "another", "though",
	"between", "should", "common", "always", "system", "second", "matter",
	"government", "without", "action", "enough", "mother", "school", "still",
	"never", "around", "little", "great", "world", "place", "house", "every",
	"social", "church", "moment", "country", "though", "answer", "letter",
}

// suppobox concatenates two short words.
func suppobox(rng *rand.Rand) string {
	a := suppoboxWords[rng.IntN(len(suppoboxWords))]
	b := suppoboxWords[rng.IntN(len(suppoboxWords))]
	return strings.ToLower(a + b)
}
