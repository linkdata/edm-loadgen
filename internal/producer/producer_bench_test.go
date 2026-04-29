package producer

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/linkdata/edm-loadgen/internal/mix"
	"github.com/linkdata/edm-loadgen/internal/state"
)

var benchmarkProducerBytes int64

func BenchmarkBuildMixedFrameParallel(b *testing.B) {
	st := state.New()
	ctx := context.Background()
	var seed atomic.Uint64
	var total atomic.Int64

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		m, err := mix.NewWithPatterns(st, nil, seed.Add(1)<<32)
		if err != nil {
			panic(err)
		}
		var builder frameBuilder
		pool := newFrameBufferPool()
		var local int64
		for pb.Next() {
			gen := m.Pick()
			if gen == nil {
				continue
			}
			q, err := gen.Next(ctx)
			if err != nil {
				panic(err)
			}
			buf := pool.get()
			frame, err := builder.buildAppend(q, buf)
			if err != nil {
				pool.put(buf)
				continue
			}
			local += int64(len(frame))
			pool.put(frame)
		}
		total.Add(local)
	})
	benchmarkProducerBytes = total.Load()
}
