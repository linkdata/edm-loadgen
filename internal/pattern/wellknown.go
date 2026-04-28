package pattern

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"strings"
)

// WellKnown wraps a Background generator and, for a configurable fraction of
// queries, mutates the SLD to a fresh random label. That guarantees the qname
// falls outside EDM's well-known DAWG and lands in the new-qname path.
//
// The split lets the verifier check EDM's new_qname_queued / processed ratio
// against what we sent.
type WellKnown struct {
	bg     *Background
	getFrac func() float64
	rng    *rand.Rand
}

// NewWellKnown returns a wrapper around bg. getFrac is called per query so a
// live UI knob change takes effect immediately.
func NewWellKnown(bg *Background, getFrac func() float64) *WellKnown {
	return &WellKnown{
		bg:      bg,
		getFrac: getFrac,
		rng:     rand.New(rand.NewPCG(0, uint64(nowFunc().UnixNano())^0xc0debabe)),
	}
}

// Name returns the pattern identifier.
func (w *WellKnown) Name() string { return "wellknown" }

// Next delegates to Background, then mutates the qname to fall outside the
// DAWG with probability (1 - fraction).
func (w *WellKnown) Next(ctx context.Context) (q Query, err error) {
	q, err = w.bg.Next(ctx)
	if err != nil {
		return
	}
	if w.rng.Float64() < w.getFrac() {
		return // keep as well-known
	}
	q.QName = mutateSLD(q.QName, w.rng)
	return
}

// mutateSLD prepends a random 12-char hex label to qname, producing something
// like "9f3a1c0bd2e7.example.com". EDM's DAWG matches by suffix, so this lands
// outside the DAWG entirely (as long as the load-gen domains list itself
// matches what the DAWG was compiled from — see configs/README.md).
func mutateSLD(qname string, rng *rand.Rand) string {
	// Random 6 bytes -> 12-char hex label.
	var raw [6]byte
	for i := range raw {
		raw[i] = byte(rng.IntN(256))
	}
	prefix := hex.EncodeToString(raw[:])
	if qname == "" {
		return prefix
	}
	// Insert prefix as a new leftmost label.
	return prefix + "." + strings.TrimPrefix(qname, ".")
}
