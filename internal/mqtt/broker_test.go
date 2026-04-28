package mqtt_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/linkdata/edm-loadgen/internal/mqtt"
	"github.com/linkdata/edm-loadgen/internal/pki"
)

// TestBrokerCountsPublishes stands up the broker on a random TLS port, has a
// Paho client publish two messages (one matching the EDM prefix, one not),
// and confirms the counters land in the expected buckets.
func TestBrokerCountsPublishes(t *testing.T) {
	dir := t.TempDir()
	bundle, err := pki.Ensure(dir, []string{"127.0.0.1"}, "broker-test")
	if err != nil {
		t.Fatalf("pki.Ensure: %v", err)
	}

	// Pick an ephemeral port by listening once and immediately closing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ephemeral listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	// External counters — same shape as state.ReceivedCounters.
	var total, edmTopic, connections int64
	br, err := mqtt.New(mqtt.Options{
		Listen:         addr,
		CertFile:       bundle.ServerCert,
		KeyFile:        bundle.ServerKey,
		EDMTopicPrefix: "events/up/test/",
		Total:          &total,
		EDMTopic:       &edmTopic,
		Connections:    &connections,
	})
	if err != nil {
		t.Fatalf("mqtt.New: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	doneCh := make(chan error, 1)
	go func() { doneCh <- br.Run(ctx) }()
	defer func() {
		cancel()
		<-doneCh
	}()

	// TLS client trusting the broker's CA.
	caPEM, err := os.ReadFile(bundle.CACert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	clientCert, err := tls.LoadX509KeyPair(bundle.ClientCert, bundle.ClientKey)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   "127.0.0.1",
	}

	opts := paho.NewClientOptions().
		AddBroker("tls://"+addr).
		SetTLSConfig(tlsConfig).
		SetClientID("edm-loadgen-broker-test").
		SetConnectTimeout(2 * time.Second)
	cl := paho.NewClient(opts)

	tok := cl.Connect()
	if !tok.WaitTimeout(3 * time.Second) {
		t.Fatal("connect timeout")
	}
	if err := tok.Error(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Disconnect(250)

	for _, m := range []struct {
		topic, body string
	}{
		{"events/up/test/edm", "hello-edm"},
		{"chatter/random", "ignored"},
	} {
		t.Run(m.topic, func(t *testing.T) {
			tok := cl.Publish(m.topic, 0, false, []byte(m.body))
			if !tok.WaitTimeout(2 * time.Second) {
				t.Fatal("publish timeout")
			}
			if err := tok.Error(); err != nil {
				t.Fatalf("publish: %v", err)
			}
		})
	}

	// Counters update on the broker side asynchronously; allow a short
	// settle window before assertions.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := br.Stats()
		if s.Total >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	s := br.Stats()
	if s.Total != 2 {
		t.Errorf("Total = %d, want 2", s.Total)
	}
	if s.EDMTopic != 1 {
		t.Errorf("EDMTopic = %d, want 1", s.EDMTopic)
	}

	// External counters should match Stats.
	if got := atomic.LoadInt64(&total); got != 2 {
		t.Errorf("external Total = %d, want 2", got)
	}
	if got := atomic.LoadInt64(&edmTopic); got != 1 {
		t.Errorf("external EDMTopic = %d, want 1", got)
	}
}
