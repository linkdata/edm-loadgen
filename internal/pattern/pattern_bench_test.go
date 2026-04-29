package pattern

import (
	"context"
	"testing"

	"github.com/linkdata/edm-loadgen/internal/state"
)

var benchmarkQuery Query

func BenchmarkGeneratorsNext(b *testing.B) {
	ctx := context.Background()

	b.Run("background", func(b *testing.B) {
		st := state.New()
		bg, err := NewBackground(st, nil, 1)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkNext(b, ctx, bg)
	})

	b.Run("wellknown", func(b *testing.B) {
		st := state.New()
		bg, err := NewBackground(st, nil, 1)
		if err != nil {
			b.Fatal(err)
		}
		wk := NewWellKnown(bg, func() float64 { return st.WellKnown.Fraction })
		benchmarkNext(b, ctx, wk)
	})

	b.Run("dga", func(b *testing.B) {
		benchmarkNext(b, ctx, NewDGA(state.New()))
	})

	b.Run("fastflux", func(b *testing.B) {
		ff, err := NewFastFlux(state.New())
		if err != nil {
			b.Fatal(err)
		}
		benchmarkNext(b, ctx, ff)
	})

	b.Run("dyndns", func(b *testing.B) {
		benchmarkNext(b, ctx, NewDynDNS(state.New()))
	})

	for _, tool := range []string{"dnscat2", "iodine", "raw-b32"} {
		tool := tool
		b.Run("exfil/"+tool, func(b *testing.B) {
			st := state.New()
			st.Exfil.Tool = tool
			st.Exfil.PayloadBytes = 4096
			benchmarkNext(b, ctx, NewExfil(st))
		})
	}

	b.Run("exotic", func(b *testing.B) {
		st := state.New()
		bg, err := NewBackground(st, nil, 1)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkNext(b, ctx, NewExotic(st, bg))
	})

	b.Run("evasion", func(b *testing.B) {
		st := state.New()
		bg, err := NewBackground(st, nil, 1)
		if err != nil {
			b.Fatal(err)
		}
		dga := NewDGA(st)
		exfil := NewExfil(st)
		exotic := NewExotic(st, bg)
		benchmarkNext(b, ctx, NewEvasion(st, dga, exfil, exotic))
	})
}

func benchmarkNext(b *testing.B, ctx context.Context, gen Generator) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q, err := gen.Next(ctx)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkQuery = q
	}
}
