package pattern

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

func newTestState(t *testing.T) *state.State {
	t.Helper()
	return state.New()
}

func mustNext(t *testing.T, g Generator) Query {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	q, err := g.Next(ctx)
	if err != nil {
		t.Fatalf("%s.Next: %v", g.Name(), err)
	}
	if q.QName == "" {
		t.Fatalf("%s.Next returned empty qname", g.Name())
	}
	if !q.SrcIP.IsValid() || !q.DstIP.IsValid() {
		t.Fatalf("%s.Next bad IPs: src=%v dst=%v", g.Name(), q.SrcIP, q.DstIP)
	}
	if !mdns.IsFqdn(mdns.Fqdn(q.QName)) {
		t.Fatalf("%s.Next bad qname: %q", g.Name(), q.QName)
	}
	return q
}

func TestBackgroundEmits(t *testing.T) {
	st := newTestState(t)
	bg, err := NewBackground(st, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		_ = mustNext(t, bg)
	}
}

func TestWellKnownFractionRoughlyHonoured(t *testing.T) {
	st := newTestState(t)
	bg, err := NewBackground(st, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wk := NewWellKnown(bg, func() float64 { return 0.5 })
	wellKnown := 0
	const n = 1000
	for i := 0; i < n; i++ {
		q := mustNext(t, wk)
		// Mutated qnames are exactly "<12 hex chars>.<original>".
		first, _, _ := strings.Cut(q.QName, ".")
		isMutated := len(first) == 12 && allHex(first)
		if !isMutated {
			wellKnown++
		}
	}
	frac := float64(wellKnown) / n
	if frac < 0.4 || frac > 0.6 {
		t.Errorf("wellknown fraction = %.3f, want 0.5 ± 0.1", frac)
	}
}

func TestDGAEntropyHigh(t *testing.T) {
	st := newTestState(t)
	st.DGA.Family = "conficker"
	dga := NewDGA(st)
	for i := 0; i < 50; i++ {
		q := mustNext(t, dga)
		first, _, _ := strings.Cut(q.QName, ".")
		// Short uniformly-random alphanumeric labels typically give 2.5–3.5
		// bits/char by Shannon, but 8-char samples with a duplicated character
		// can dip below 2.3. We pick 2.0 as a floor that still rules out
		// natural-language dictionary words (~1.5 bits/char).
		if e := shannonBitsPerChar(first); e < 2.0 {
			t.Errorf("conficker label %q entropy = %.2f, want >= 2.0", first, e)
		}
	}
}

func TestBeaconUsesConfiguredDomain(t *testing.T) {
	st := newTestState(t)
	st.Beacon.Domain = "c2.example.invalid"
	st.Beacon.Interval = 100 * time.Millisecond
	st.Beacon.JitterPct = 0
	b := NewBeacon(st)
	q1 := mustNext(t, b) // first emission is immediate
	if q1.QName != "c2.example.invalid" {
		t.Errorf("Beacon qname = %q, want c2.example.invalid", q1.QName)
	}
	start := time.Now()
	_ = mustNext(t, b) // second waits for the interval
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Errorf("Beacon second emit too fast: %v", elapsed)
	}
}

func TestFastFluxRotates(t *testing.T) {
	st := newTestState(t)
	st.FastFlux.IPPoolCIDR = "203.0.113.0/29" // 8 addrs
	ff, err := NewFastFlux(st)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		q := mustNext(t, ff)
		if a, ok := q.Answers[0].(*mdns.A); ok {
			seen[a.A.String()] = struct{}{}
		}
	}
	if len(seen) < 3 {
		t.Errorf("fastflux only saw %d distinct IPs in 50 queries: %v", len(seen), seen)
	}
}

func TestDynDNSUsesProvider(t *testing.T) {
	st := newTestState(t)
	st.DynDNS.Providers = []string{"duckdns.org"}
	dd := NewDynDNS(st)
	for i := 0; i < 20; i++ {
		q := mustNext(t, dd)
		if !strings.HasSuffix(q.QName, ".duckdns.org") {
			t.Errorf("dyndns qname %q does not end in .duckdns.org", q.QName)
		}
	}
}

func TestExfilLabelsUnique(t *testing.T) {
	st := newTestState(t)
	st.Exfil.Tool = "dnscat2"
	st.Exfil.Domain = "ex.test.invalid"
	st.Exfil.PayloadBytes = 4096
	ex := NewExfil(st)
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		q := mustNext(t, ex)
		if !strings.HasSuffix(q.QName, ".ex.test.invalid") {
			t.Errorf("exfil qname %q wrong suffix", q.QName)
		}
		seen[q.QName] = struct{}{}
	}
	if len(seen) < 40 {
		t.Errorf("exfil distinct qnames = %d, want >= 40 in 50 emissions", len(seen))
	}
}

func TestExoticPicksConfiguredTypes(t *testing.T) {
	st := newTestState(t)
	st.Exotic.RecordTypes = []string{"TXT", "CNAME", "NULL"}
	bg, _ := NewBackground(st, nil, 0)
	ex := NewExotic(st, bg)
	types := map[uint16]int{}
	for i := 0; i < 200; i++ {
		q := mustNext(t, ex)
		types[q.QType]++
	}
	for _, want := range []uint16{mdns.TypeTXT, mdns.TypeCNAME, mdns.TypeNULL} {
		if types[want] == 0 {
			t.Errorf("exotic never emitted qtype %d (counts=%v)", want, types)
		}
	}
}

func TestEvasionDelegates(t *testing.T) {
	st := newTestState(t)
	bg, _ := NewBackground(st, nil, 0)
	dga := NewDGA(st)
	exfil := NewExfil(st)
	exotic := NewExotic(st, bg)
	ev := NewEvasion(st, dga, exfil, exotic)
	for i := 0; i < 30; i++ {
		_ = mustNext(t, ev)
	}
}

func allHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func shannonBitsPerChar(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]int{}
	for _, r := range s {
		freq[r]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		p := float64(c) / n
		h -= p * (math.Log2(p))
	}
	return h
}
