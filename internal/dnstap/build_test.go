package dnstap

import (
	"net/netip"
	"testing"
	"time"

	dnstap "github.com/dnstap/golang-dnstap"
	mdns "github.com/miekg/dns"
	"google.golang.org/protobuf/proto"
)

func TestNewClientResponseRoundTrip(t *testing.T) {
	rr, err := mdns.NewRR("example.com. 300 IN A 192.0.2.1")
	if err != nil {
		t.Fatalf("NewRR: %v", err)
	}
	msg := &mdns.Msg{
		MsgHdr:   mdns.MsgHdr{Id: 0xabcd, Response: true},
		Question: []mdns.Question{{Name: "example.com.", Qtype: mdns.TypeA, Qclass: mdns.ClassINET}},
		Answer:   []mdns.RR{rr},
	}
	dnsBytes, err := msg.Pack()
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	when := time.Date(2026, 4, 28, 12, 0, 0, 123_000_000, time.UTC)
	dt := NewClientResponse(Query{
		SrcIP: netip.MustParseAddr("203.0.113.5"),
		DstIP: netip.MustParseAddr("198.51.100.10"),
		At:    when,
		DNS:   dnsBytes,
	})

	wire, err := proto.Marshal(dt)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	got := new(dnstap.Dnstap)
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if string(got.Identity) != string(Identity) {
		t.Errorf("Identity = %q, want %q", got.Identity, Identity)
	}
	m := got.Message
	if m == nil {
		t.Fatal("Message is nil")
	}
	if m.GetType() != dnstap.Message_CLIENT_RESPONSE {
		t.Errorf("Message.Type = %v, want CLIENT_RESPONSE", m.GetType())
	}
	if m.GetSocketFamily() != dnstap.SocketFamily_INET {
		t.Errorf("SocketFamily = %v, want INET", m.GetSocketFamily())
	}
	if m.GetSocketProtocol() != dnstap.SocketProtocol_UDP {
		t.Errorf("SocketProtocol = %v, want UDP", m.GetSocketProtocol())
	}
	if got, want := netip.AddrFrom4([4]byte(m.QueryAddress)), netip.MustParseAddr("203.0.113.5"); got != want {
		t.Errorf("QueryAddress = %v, want %v", got, want)
	}
	if got, want := netip.AddrFrom4([4]byte(m.ResponseAddress)), netip.MustParseAddr("198.51.100.10"); got != want {
		t.Errorf("ResponseAddress = %v, want %v", got, want)
	}
	if m.GetQueryPort() != 12345 {
		t.Errorf("QueryPort = %d, want 12345", m.GetQueryPort())
	}
	if m.GetResponsePort() != 53 {
		t.Errorf("ResponsePort = %d, want 53", m.GetResponsePort())
	}
	if int64(m.GetResponseTimeSec()) != when.Unix() {
		t.Errorf("ResponseTimeSec = %d, want %d", m.GetResponseTimeSec(), when.Unix())
	}
	if m.GetResponseTimeNsec() != 123_000_000 {
		t.Errorf("ResponseTimeNsec = %d, want 123000000", m.GetResponseTimeNsec())
	}

	parsed := new(mdns.Msg)
	if err := parsed.Unpack(m.ResponseMessage); err != nil {
		t.Fatalf("ResponseMessage Unpack: %v", err)
	}
	if len(parsed.Question) != 1 || parsed.Question[0].Name != "example.com." {
		t.Errorf("ResponseMessage question = %+v", parsed.Question)
	}
}

func TestNewClientResponseIPv6(t *testing.T) {
	dt := NewClientResponse(Query{
		SrcIP: netip.MustParseAddr("2001:db8::1"),
		DstIP: netip.MustParseAddr("2001:db8::53"),
	})
	if dt.Message.GetSocketFamily() != dnstap.SocketFamily_INET6 {
		t.Errorf("SocketFamily = %v, want INET6", dt.Message.GetSocketFamily())
	}
	if got := len(dt.Message.QueryAddress); got != 16 {
		t.Errorf("QueryAddress length = %d, want 16", got)
	}
}
