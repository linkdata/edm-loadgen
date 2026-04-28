// Package rate paces the producer goroutine.
//
// The Bucket type wraps github.com/linkdata/rate.Ticker, which observes its
// rate cell as a *int32. Callers update the rate by atomically writing to
// the same int32 — no Bucket method call required.
package rate

import lkrate "github.com/linkdata/rate"

// Bucket gates a producer goroutine to a per-second rate.
//
// Consumers read directly from the underlying ticker's channel via Wait. The
// linkdata/rate.Ticker.Wait() helper bumps a "padding" counter after each
// receive that suppresses the internal rate gate; we deliberately bypass it
// by reading <-t.C ourselves, which keeps every tick paced.
type Bucket struct {
	t *lkrate.Ticker
}

// NewBucket returns a Bucket whose maximum rate is read from max on every
// tick. The caller writes to *max via atomic.StoreInt32 (or any normal store
// that publishes to other goroutines) and the next tick observes the new
// rate without restart.
func NewBucket(max *int32) *Bucket {
	return &Bucket{t: lkrate.NewTicker(nil, max)}
}

// Wait blocks until the next available tick. Returns false if the underlying
// ticker is closed.
func (b *Bucket) Wait() bool {
	_, ok := <-b.t.C
	return ok
}

// Close stops the ticker.
func (b *Bucket) Close() { b.t.Close() }

// Rate reports the current observed rate (advisory).
func (b *Bucket) Rate() int32 { return b.t.Rate() }
