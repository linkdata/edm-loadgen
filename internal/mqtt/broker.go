// Package mqtt embeds a minimal MQTT broker so EDM's publish path is live
// without requiring an external broker. The broker accepts TLS connections,
// allows any client to connect with any cert, and counts received Publish
// packets via an OnPublish hook.
//
// This is dev-only territory: no JWS validation, no ACLs, no client-cert
// pinning. Its sole job is to make EDM's edm_new_qname_queued_total counter
// tick in our test setups.
package mqtt

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Broker is an embedded MQTT broker fronted by a TLS listener.
type Broker struct {
	srv      *mochi.Server
	hook     *countingHook
	stopOnce func()
}

// Options configures a new Broker.
type Options struct {
	// Listen is the TLS listen address (e.g. "127.0.0.1:8883" or ":8883").
	Listen string
	// CertFile / KeyFile is the TLS server material (typically what
	// pki.Ensure wrote as server.crt / server.key).
	CertFile string
	KeyFile  string
	// EDMTopicPrefix matches Publish packets whose TopicName starts with
	// this string into the EDMTopic counter (e.g. "events/up/<NodeName>/").
	// Empty disables prefix matching; only the total counter increments.
	EDMTopicPrefix string

	// Total / EDMTopic / Connections are external int64 pointers the
	// OnPublish / OnConnect hooks atomically increment. When nil, the
	// broker uses internal storage (still accessible via Stats()).
	// Pointing these at state.ReceivedCounters fields lets the UI read
	// the counters with no extra plumbing.
	Total       *int64
	EDMTopic    *int64
	Connections *int64
}

// New constructs a Broker but does not start it. Run starts the listener.
func New(opts Options) (*Broker, error) {
	if opts.Listen == "" {
		return nil, errors.New("mqtt: empty Listen")
	}
	cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("mqtt: load tls keypair: %w", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// Accept any client cert. EDM presents one but we have no business
		// validating it in dev.
		ClientAuth: tls.RequestClientCert,
		MinVersion: tls.VersionTLS12,
	}

	// Quiet mochi's default logger: it writes to stdout at info level by
	// default and is noisy for our purposes. Drop everything below Warn.
	srv := mochi.New(&mochi.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		return nil, fmt.Errorf("mqtt: add allow hook: %w", err)
	}

	hook := &countingHook{
		edmPrefix: opts.EDMTopicPrefix,
		total:     coalesce(opts.Total),
		edmTopic:  coalesce(opts.EDMTopic),
		connections: coalesce(opts.Connections),
	}
	if err := srv.AddHook(hook, nil); err != nil {
		return nil, fmt.Errorf("mqtt: add counting hook: %w", err)
	}

	tcp := listeners.NewTCP(listeners.Config{
		ID:        "edm-loadgen-tls",
		Address:   opts.Listen,
		TLSConfig: tlsConfig,
	})
	if err := srv.AddListener(tcp); err != nil {
		return nil, fmt.Errorf("mqtt: add listener: %w", err)
	}
	return &Broker{srv: srv, hook: hook}, nil
}

// Run starts the broker and blocks until ctx is cancelled. Server.Serve is
// non-blocking, so we use ctx as the lifecycle signal.
func (b *Broker) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- b.srv.Serve()
	}()
	// Brief pause so listener startup errors surface fast.
	select {
	case err := <-errCh:
		return fmt.Errorf("mqtt: serve: %w", err)
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case <-ctx.Done():
		_ = b.srv.Close()
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("mqtt: serve: %w", err)
		}
		return nil
	}
}

// Close shuts down the broker outside of Run.
func (b *Broker) Close() error { return b.srv.Close() }

// Stats is a snapshot of the broker's per-publish counters.
type Stats struct {
	Total       int64 // every Publish from any client
	EDMTopic    int64 // Publishes whose TopicName matches EDMTopicPrefix
	Connections int64 // total client Connect events seen so far
}

// Stats reads the counters atomically.
func (b *Broker) Stats() Stats {
	return Stats{
		Total:       atomic.LoadInt64(b.hook.total),
		EDMTopic:    atomic.LoadInt64(b.hook.edmTopic),
		Connections: atomic.LoadInt64(b.hook.connections),
	}
}

// countingHook is a mochi.Hook that increments per-publish counters via
// caller-supplied (or internally-allocated) pointers. It embeds HookBase so
// unset Hook methods get default no-op implementations.
type countingHook struct {
	mochi.HookBase
	edmPrefix string

	total       *int64
	edmTopic    *int64
	connections *int64
}

// coalesce returns p if non-nil, else a fresh int64 pointer. Callers can
// then count into either the supplied external storage or the broker's
// internal storage uniformly.
func coalesce(p *int64) *int64 {
	if p != nil {
		return p
	}
	var v int64
	return &v
}

func (h *countingHook) ID() string { return "edm-loadgen-counter" }

func (h *countingHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mochi.OnPublish,
		mochi.OnConnect,
	}, []byte{b})
}

func (h *countingHook) OnPublish(_ *mochi.Client, pk packets.Packet) (packets.Packet, error) {
	atomic.AddInt64(h.total, 1)
	if h.edmPrefix != "" && strings.HasPrefix(pk.TopicName, h.edmPrefix) {
		atomic.AddInt64(h.edmTopic, 1)
	}
	return pk, nil
}

func (h *countingHook) OnConnect(_ *mochi.Client, _ packets.Packet) error {
	atomic.AddInt64(h.connections, 1)
	return nil
}
