package web

import (
	"fmt"
	"html/template"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/linkdata/bytecount"
	"github.com/linkdata/jaws"
	"github.com/linkdata/jaws/lib/bind"
)

// formatCount is the canonical pretty-printer for counters and rates in the
// UI. It uses github.com/linkdata/bytecount with `%#d` — base-1000 scaling
// and no unit suffix ("1.23M" instead of "1.23MB"). Values below 1000 are
// rendered as plain integers so small counters don't gain spurious decimals.
func formatCount(n int64) string {
	if n > -1000 && n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%#d", bytecount.N(n))
}

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

// AtomicInt64Getter renders the current value of p (via atomic.LoadInt64)
// pretty-printed through bytecount. Pass extra dependency tags via deps so
// a single Dirty call can refresh many gauges at once.
func AtomicInt64Getter(p *int64, deps ...any) bind.HTMLGetter {
	return bind.HTMLGetterFunc(func(*jaws.Element) template.HTML {
		// formatCount produces only digits, dots and ASCII scale letters
		// (k/M/G/...), all safe to embed as raw HTML without escaping.
		return template.HTML(formatCount(atomic.LoadInt64(p))) // #nosec G203
	}, append([]any{p}, deps...)...)
}

// DriftGetter returns an HTMLGetter that displays sent-observed drift with a
// leading sign and bytecount-scaled magnitude.
func DriftGetter(sent, observed *int64, deps ...any) bind.HTMLGetter {
	return bind.HTMLGetterFunc(func(*jaws.Element) template.HTML {
		d := atomic.LoadInt64(sent) - atomic.LoadInt64(observed)
		sign := "+"
		if d < 0 {
			sign = "-"
			d = -d
		}
		return template.HTML(sign + formatCount(d)) // #nosec G203
	}, append([]any{sent, observed}, deps...)...)
}
