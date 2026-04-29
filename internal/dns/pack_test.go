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

func TestBuilderResponseRoundTrip(t *testing.T) {
	rr, err := mdns.NewRR(`example.com. 300 IN TXT "v=spf1 -all"`)
	if err != nil {
		t.Fatalf("NewRR: %v", err)
	}
	var builder Builder
	b, err := builder.Response(0xbeef, "example.com", mdns.TypeTXT, []mdns.RR{rr})
	if err != nil {
		t.Fatalf("Builder.Response: %v", err)
	}
	got := new(mdns.Msg)
	if err := got.Unpack(b); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if got.Id != 0xbeef {
		t.Errorf("Id = %x, want 0xbeef", got.Id)
	}
	if len(got.Question) != 1 || got.Question[0].Qtype != mdns.TypeTXT {
		t.Errorf("Question = %+v", got.Question)
	}
	if len(got.Answer) != 1 {
		t.Fatalf("Answer count = %d, want 1", len(got.Answer))
	}
	if txt, ok := got.Answer[0].(*mdns.TXT); !ok || len(txt.Txt) != 1 || txt.Txt[0] != "v=spf1 -all" {
		t.Fatalf("Answer[0] = %#v, want TXT v=spf1 -all", got.Answer[0])
	}
}
