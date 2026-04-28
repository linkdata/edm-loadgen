// Package producer drives the main load-generation goroutine: pull a query
// from the mixer, build a dnstap envelope, ship it through the sink. It also
// owns the beacon pump, since beacons run on their own time-driven cadence
// rather than through the QPS bucket.
package producer

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/linkdata/edm-loadgen/internal/dns"
	"github.com/linkdata/edm-loadgen/internal/dnstap"
	"github.com/linkdata/edm-loadgen/internal/mix"
	"github.com/linkdata/edm-loadgen/internal/pattern"
	"github.com/linkdata/edm-loadgen/internal/rate"
	"github.com/linkdata/edm-loadgen/internal/sink"
	"github.com/linkdata/edm-loadgen/internal/state"
)

// Producer is a long-running orchestrator that drains a mixer at the configured
// QPS and writes envelopes to a Sink. Run blocks until ctx is cancelled.
type Producer struct {
	st     *state.State
	mixer  *mix.Mixer
	beacon *pattern.Beacon
	sink   *sink.Sink
	bucket *rate.Bucket
}

// New constructs a Producer with the given dependencies. The bucket is wired
// to st.QPS so live UI knob changes apply on the next tick.
func New(st *state.State, mixer *mix.Mixer, beacon *pattern.Beacon, snk *sink.Sink) *Producer {
	return &Producer{
		st:     st,
		mixer:  mixer,
		beacon: beacon,
		sink:   snk,
		bucket: rate.NewBucket(&st.QPS),
	}
}

// Run starts the main and beacon goroutines. Returns when ctx is cancelled or
// the bucket is closed.
func (p *Producer) Run(ctx context.Context) error {
	defer p.bucket.Close()

	beaconCtx, cancelBeacon := context.WithCancel(ctx)
	defer cancelBeacon()
	go p.runBeacon(beaconCtx)
	go p.publishRate(beaconCtx)

	for p.bucket.Wait() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !p.st.IsRunning() {
			// Paused — drop ticks until Running flips back on.
			continue
		}
		gen := p.mixer.Pick()
		if gen == nil {
			continue
		}
		q, err := gen.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if err := p.send(gen.Name(), q); err != nil {
			// Sink errors are visible via metrics drift; do not spam stderr.
			continue
		}
	}
	return ctx.Err()
}

// runBeacon emits beacon queries on its own time-driven cadence, independent
// of the QPS bucket. Honours the beacon mix weight: when zero, the beacon is
// silent.
func (p *Producer) runBeacon(ctx context.Context) {
	for {
		if atomic.LoadInt32(&p.st.Mix.Beacon) <= 0 {
			// Sleep briefly and re-check; cheaper than rebuilding the
			// generator on every weight flip.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		q, err := p.beacon.Next(ctx)
		if err != nil {
			return
		}
		if !p.st.IsRunning() {
			continue
		}
		_ = p.send("beacon", q)
	}
}

// publishRate samples the bucket's observed rate every 500ms and writes it
// to st.ObservedQPS. The bucket itself only re-measures internally roughly
// once per second, so a faster sampling interval is fine and the UI can
// dirty its gauge at the broadcast cadence.
func (p *Producer) publishRate(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			atomic.StoreInt32(&p.st.ObservedQPS, 0)
			return
		case <-t.C:
			atomic.StoreInt32(&p.st.ObservedQPS, p.bucket.Rate())
		}
	}
}

// send marshals q into a dnstap envelope and writes it. Counter updates are
// best-effort: even if the sink call later fails, we have already incremented
// Sent, so the verifier's drift line will reflect the failure.
func (p *Producer) send(patternName string, q pattern.Query) error {
	dnsBytes, err := dns.Response(uint16(time.Now().UnixNano()), q.QName, q.QType, q.Answers)
	if err != nil {
		return fmt.Errorf("producer: pack dns: %w", err)
	}
	dt := dnstap.NewClientResponse(dnstap.Query{
		SrcIP: q.SrcIP,
		DstIP: q.DstIP,
		At:    q.At,
		DNS:   dnsBytes,
	})
	if err := p.sink.Send(dt); err != nil {
		return err
	}
	bumpCounter(p.st, patternName)
	return nil
}

func bumpCounter(st *state.State, name string) {
	switch name {
	case "background":
		atomic.AddInt64(&st.Sent.Background, 1)
	case "wellknown":
		atomic.AddInt64(&st.Sent.WellKnown, 1)
	case "dga":
		atomic.AddInt64(&st.Sent.DGA, 1)
	case "beacon":
		atomic.AddInt64(&st.Sent.Beacon, 1)
	case "fastflux":
		atomic.AddInt64(&st.Sent.FastFlux, 1)
	case "dyndns":
		atomic.AddInt64(&st.Sent.DynDNS, 1)
	case "exfil":
		atomic.AddInt64(&st.Sent.Exfil, 1)
	case "exotic":
		atomic.AddInt64(&st.Sent.Exotic, 1)
	case "evasion":
		atomic.AddInt64(&st.Sent.Evasion, 1)
	}
	atomic.AddInt64(&st.Sent.Total, 1)
}

