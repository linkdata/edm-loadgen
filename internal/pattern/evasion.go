package pattern

import (
	"context"
	"math/rand/v2"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Evasion is a meta-pattern that picks one of {dga, exfil, exotic} per call,
// weighted by the configured profile. It exists to exercise the full pipeline
// under combined pressure rather than to model any specific real-world tool.
type Evasion struct {
	st      *state.State
	rng     *rand.Rand
	dga     *DGA
	exfil   *Exfil
	exotic  *Exotic
}

// NewEvasion composes the underlying generators. The caller is expected to
// share the same Exotic/DGA/Exfil instances elsewhere — Next does not have
// side effects on counter state, so pointer sharing is safe.
func NewEvasion(st *state.State, dga *DGA, exfil *Exfil, exotic *Exotic) *Evasion {
	rng := rand.New(rand.NewPCG(0xeba510, uint64(nowFunc().UnixNano())))
	return &Evasion{st: st, rng: rng, dga: dga, exfil: exfil, exotic: exotic}
}

// Name returns the pattern identifier.
func (e *Evasion) Name() string { return "evasion" }

// Next picks one of the underlying generators per the active profile.
func (e *Evasion) Next(ctx context.Context) (q Query, err error) {
	e.st.RLock()
	profile := e.st.Evasion.Profile
	e.st.RUnlock()

	dgaW, exfilW, exoticW := evasionWeights(profile)
	total := dgaW + exfilW + exoticW
	if total <= 0 {
		return e.dga.Next(ctx)
	}
	pick := e.rng.IntN(total)
	switch {
	case pick < dgaW:
		return e.dga.Next(ctx)
	case pick < dgaW+exfilW:
		return e.exfil.Next(ctx)
	default:
		return e.exotic.Next(ctx)
	}
}

// evasionWeights maps the named profile to integer weights for {dga, exfil,
// exotic}. "off" disables evasion entirely (caller should drop this pattern's
// mix weight to 0 instead).
func evasionWeights(profile string) (dga, exfil, exotic int) {
	switch profile {
	case "off":
		return 0, 0, 0
	case "light":
		return 3, 1, 1
	case "heavy":
		return 1, 3, 2
	default: // medium
		return 2, 2, 1
	}
}
