package sink

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	dnstap "github.com/dnstap/golang-dnstap"
	mdns "github.com/miekg/dns"
	"google.golang.org/protobuf/proto"

	loadgendns "github.com/linkdata/edm-loadgen/internal/dns"
	loadgentap "github.com/linkdata/edm-loadgen/internal/dnstap"
)

// TestSinkRoundTrip stands up an in-process EDM-style FrameStreamSockInput,
// sends 5 frames through Sink, and verifies all 5 arrive intact on the
// receiver side.
func TestSinkRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	dti := dnstap.NewFrameStreamSockInput(ln)
	dti.SetTimeout(5 * time.Second)

	rxCh := make(chan []byte, 16)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		dti.ReadInto(rxCh)
	}()

	target := "tcp://" + ln.Addr().String()
	s, err := Dial(target, Options{Timeout: 2 * time.Second, RetryInterval: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	rr, _ := mdns.NewRR("example.com. 60 IN A 192.0.2.1")
	for i := 0; i < 5; i++ {
		dnsBytes, err := loadgendns.Response(uint16(i+1), "example.com", mdns.TypeA, []mdns.RR{rr})
		if err != nil {
			t.Fatalf("dns.Response: %v", err)
		}
		dt := loadgentap.NewClientResponse(loadgentap.Query{
			SrcIP: netip.MustParseAddr("203.0.113.5"),
			DstIP: netip.MustParseAddr("198.51.100.10"),
			At:    time.Now(),
			DNS:   dnsBytes,
		})
		if err := s.Send(dt); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Logf("Close (non-fatal): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got := 0
	for got < 5 {
		select {
		case frame, ok := <-rxCh:
			if !ok {
				t.Fatalf("rxCh closed after %d frames, want 5", got)
			}
			dt := new(dnstap.Dnstap)
			if err := proto.Unmarshal(frame, dt); err != nil {
				t.Fatalf("unmarshal frame %d: %v", got, err)
			}
			if dt.Message == nil || dt.Message.GetType() != dnstap.Message_CLIENT_RESPONSE {
				t.Fatalf("frame %d: unexpected envelope: %+v", got, dt)
			}
			got++
		case <-ctx.Done():
			t.Fatalf("timeout after %d/5 frames", got)
		}
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in      string
		network string
		wantErr bool
	}{
		{"tcp://127.0.0.1:53535", "tcp", false},
		{"unix:///run/edm.sock", "unix", false},
		{"http://nope", "", true},
		{"://broken", "", true},
	}
	for _, c := range cases {
		addr, err := ParseTarget(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && addr.Network() != c.network {
			t.Errorf("%q: network=%q, want %q", c.in, addr.Network(), c.network)
		}
	}
}
