package web

import (
	"html/template"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/linkdata/jaws"
	"github.com/linkdata/jaws/lib/bind"
)

// AtomicInt32 returns a Binder[int32] backed by atomic.LoadInt32 /
// atomic.StoreInt32 on p. The locker is taken for the binder's mutual
// exclusion (so multiple writes serialise) but the actual value access goes
// through atomic ops, which gives a happens-before relationship for any other
// goroutine reading p with atomic.LoadInt32.
//
// This is the right shape for fields shared with github.com/linkdata/rate's
// Ticker, which observes its rate cell with atomic.LoadInt32 and would race
// against a plain *p = v store.
func AtomicInt32(mu sync.Locker, p *int32) bind.Binder[int32] {
	return bind.New(mu, p).
		GetLocked(func(_ bind.Binder[int32], _ *jaws.Element) int32 {
			return atomic.LoadInt32(p)
		}).
		SetLocked(func(_ bind.Binder[int32], _ *jaws.Element, v int32) error {
			if atomic.LoadInt32(p) == v {
				return jaws.ErrValueUnchanged
			}
			atomic.StoreInt32(p, v)
			return nil
		})
}

// AtomicInt64Getter returns an HTMLGetter that renders the current value of p
// using atomic.LoadInt64. Producer/verifier code updates p with
// atomic.AddInt64 / StoreInt64 so reads see consistent values.
//
// Pass extra dependency tags via deps (e.g. a group tag like &state.Observed)
// so a single Request.Dirty call can refresh many gauges.
func AtomicInt64Getter(p *int64, deps ...any) bind.HTMLGetter {
	return bind.HTMLGetterFunc(func(*jaws.Element) template.HTML {
		// Decimal integers are safe to embed without escaping.
		return template.HTML(strconv.FormatInt(atomic.LoadInt64(p), 10)) // #nosec G203
	}, append([]any{p}, deps...)...)
}

// DriftGetter returns an HTMLGetter that displays sent-observed drift with a
// leading sign so the UI distinguishes positive (load-gen ahead) and
// negative (verifier ahead) values.
func DriftGetter(sent, observed *int64, deps ...any) bind.HTMLGetter {
	return bind.HTMLGetterFunc(func(*jaws.Element) template.HTML {
		s := atomic.LoadInt64(sent)
		o := atomic.LoadInt64(observed)
		d := s - o
		sign := "+"
		if d < 0 {
			sign = ""
		}
		return template.HTML(sign + strconv.FormatInt(d, 10)) // #nosec G203
	}, append([]any{sent, observed}, deps...)...)
}
