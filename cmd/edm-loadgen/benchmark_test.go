package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linkdata/edm-loadgen/internal/state"
)

func TestWaitForFirstBenchmarkFrame(t *testing.T) {
	st := state.New()
	prodDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go func() {
		time.Sleep(10 * time.Millisecond)
		atomic.StoreInt64(&st.Sent.Total, 1)
	}()

	start, err := waitForFirstBenchmarkFrame(ctx, st, prodDone)
	if err != nil {
		t.Fatalf("waitForFirstBenchmarkFrame: %v", err)
	}
	if start.IsZero() {
		t.Fatal("start time is zero")
	}
}

func TestWaitForFirstBenchmarkFrameProducerStops(t *testing.T) {
	st := state.New()
	prodDone := make(chan error, 1)
	want := errors.New("boom")
	prodDone <- want

	_, err := waitForFirstBenchmarkFrame(context.Background(), st, prodDone)
	if !errors.Is(err, want) {
		t.Fatalf("err=%v, want wrapped %v", err, want)
	}
}

func TestRoundRate(t *testing.T) {
	cases := []struct {
		n    int64
		d    time.Duration
		want int64
	}{
		{n: 10, d: time.Second, want: 10},
		{n: 15, d: 2 * time.Second, want: 8},
		{n: 14, d: 2 * time.Second, want: 7},
		{n: 10, d: 0, want: 0},
	}
	for _, c := range cases {
		if got := roundRate(c.n, c.d); got != c.want {
			t.Fatalf("roundRate(%d, %s)=%d, want %d", c.n, c.d, got, c.want)
		}
	}
}
