package dns

import (
	"net"
	"testing"

	mdns "github.com/miekg/dns"
)

func TestResponseRoundTrip(t *testing.T) {
	rr, err := mdns.NewRR("example.com. 300 IN A 192.0.2.1")
	if err != nil {
		t.Fatalf("NewRR: %v", err)
	}
	b, err := Response(0x1234, "example.com", mdns.TypeA, []mdns.RR{rr})
	if err != nil {
		t.Fatalf("Response: %v", err)
	}
	if len(b) < 12 {
		t.Fatalf("response too short: %d bytes", len(b))
	}
	got := new(mdns.Msg)
	if err := got.Unpack(b); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if got.Id != 0x1234 {
		t.Errorf("Id = %x, want 0x1234", got.Id)
	}
	if !got.Response {
		t.Error("QR bit not set")
	}
	if len(got.Question) != 1 || got.Question[0].Name != "example.com." {
		t.Errorf("Question = %+v", got.Question)
	}
	if len(got.Answer) != 1 {
		t.Fatalf("Answer count = %d, want 1", len(got.Answer))
	}
	a, ok := got.Answer[0].(*mdns.A)
	if !ok {
		t.Fatalf("Answer[0] type = %T, want *dns.A", got.Answer[0])
	}
	if !a.A.Equal(net.ParseIP("192.0.2.1")) {
		t.Errorf("Answer A = %s, want 192.0.2.1", a.A)
	}
}
