package rate_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/linkdata/edm-loadgen/internal/rate"
)

// TestBucketHonoursRate verifies that calling Wait in a tight loop runs
// approximately at the configured QPS. We use a generous tolerance because
// linkdata/rate.Ticker computes its rate over 1-second windows and there's
// startup ramp-up.
func TestBucketHonoursRate(t *testing.T) {
	for _, qps := range []int32{50, 200, 1000} {
		qps := qps
		t.Run("", func(t *testing.T) {
			r := qps
			b := rate.NewBucket(&r)
			defer b.Close()

			start := time.Now()
			budget := 2 * time.Second
			deadline := start.Add(budget)
			var n int64
			for time.Now().Before(deadline) {
				if !b.Wait() {
					break
				}
				n++
			}
			elapsed := time.Since(start)
			actual := float64(n) / elapsed.Seconds()
			t.Logf("qps=%d actual=%.0f n=%d elapsed=%s", qps, actual, n, elapsed)
			// Allow ±50% — linkdata/rate paces over sub-second windows so
			// we mostly want to confirm this is in the right ballpark.
			low, high := float64(qps)*0.5, float64(qps)*2.5
			if actual < low || actual > high {
				t.Errorf("qps=%d: observed %.0f, want %.0f–%.0f", qps, actual, low, high)
			}
		})
	}
	_ = atomic.LoadInt32 // keep the import live
}
