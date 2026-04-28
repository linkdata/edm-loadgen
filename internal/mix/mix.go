// Package mix picks the next pattern to fire based on weights from
// internal/state.State. Per-call weight reads make UI knob changes apply on
// the very next emission.
package mix

import (
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/linkdata/edm-loadgen/internal/pattern"
	"github.com/linkdata/edm-loadgen/internal/state"
)

// Slot pairs a generator with the int32 weight that controls how often it is
// picked. The weight is read with atomic.LoadInt32 so live UI knob changes
// take effect on the next call to Pick.
//
// Beacon is a special case: it has its own time-driven cadence and is not in
// the mix at all (it gets a separate pump in the producer).
type Slot struct {
	Gen    pattern.Generator
	Weight *int32
}

// Mixer is a per-call weighted picker.
type Mixer struct {
	slots []Slot
	rng   *rand.Rand
}

// New returns a Mixer over the given slots. Slots with zero or negative weight
// at pick time are skipped automatically.
func New(slots []Slot) *Mixer {
	return &Mixer{
		slots: slots,
		rng:   rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xfeed)),
	}
}

// Pick returns the next generator. When all weights are zero, returns nil.
func (m *Mixer) Pick() pattern.Generator {
	var total int32
	for _, s := range m.slots {
		w := atomic.LoadInt32(s.Weight)
		if w > 0 {
			total += w
		}
	}
	if total == 0 {
		return nil
	}
	r := int32(m.rng.IntN(int(total)))
	for _, s := range m.slots {
		w := atomic.LoadInt32(s.Weight)
		if w <= 0 {
			continue
		}
		if r < w {
			return s.Gen
		}
		r -= w
	}
	return m.slots[len(m.slots)-1].Gen // unreachable except on race
}

// Slots returns the configured slots. Useful for the producer to enumerate
// per-pattern counter pointers.
func (m *Mixer) Slots() []Slot { return m.slots }

// FromState builds a Mixer wired to the standard set of patterns. Beacon is
// returned separately because it pumps its own cadence; the caller is
// expected to spin a dedicated goroutine for it.
//
// domainsPath is forwarded to the Background generator.
func FromState(st *state.State, domainsPath string) (*Mixer, *pattern.Beacon, error) {
	bg, err := pattern.NewBackground(st, domainsPath)
	if err != nil {
		return nil, nil, err
	}
	wk := pattern.NewWellKnown(bg, func() float64 {
		st.RLock()
		defer st.RUnlock()
		return st.WellKnown.Fraction
	})
	dga := pattern.NewDGA(st)
	ff, err := pattern.NewFastFlux(st)
	if err != nil {
		return nil, nil, err
	}
	dd := pattern.NewDynDNS(st)
	exfil := pattern.NewExfil(st)
	exotic := pattern.NewExotic(st, bg)
	ev := pattern.NewEvasion(st, dga, exfil, exotic)
	beacon := pattern.NewBeacon(st)

	slots := []Slot{
		{Gen: bg, Weight: &st.Mix.Background},
		{Gen: wk, Weight: &st.Mix.WellKnown},
		{Gen: dga, Weight: &st.Mix.DGA},
		{Gen: ff, Weight: &st.Mix.FastFlux},
		{Gen: dd, Weight: &st.Mix.DynDNS},
		{Gen: exfil, Weight: &st.Mix.Exfil},
		{Gen: exotic, Weight: &st.Mix.Exotic},
		{Gen: ev, Weight: &st.Mix.Evasion},
	}
	return New(slots), beacon, nil
}
