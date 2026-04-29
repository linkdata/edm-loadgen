package dnstap

import (
	"net/netip"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
	"google.golang.org/protobuf/proto"
)

var benchmarkDnstapBytes int

func BenchmarkNewClientResponseMarshal(b *testing.B) {
	q := benchmarkQuery(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dt := NewClientResponse(q)
		wire, err := proto.Marshal(dt)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkDnstapBytes = len(wire)
	}
}

func BenchmarkClientResponseBuilderMarshal(b *testing.B) {
	q := benchmarkQuery(b)
	var builder ClientResponseBuilder
	buf := make([]byte, 0, 256)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wire, err := builder.MarshalClientResponseAppend(buf, q)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkDnstapBytes = len(wire)
		buf = wire[:0]
	}
}

func benchmarkQuery(b *testing.B) Query {
	b.Helper()
	msg := &mdns.Msg{
		MsgHdr:   mdns.MsgHdr{Id: 0xabcd, Response: true, RecursionDesired: true, RecursionAvailable: true},
		Question: []mdns.Question{{Name: "example.com.", Qtype: mdns.TypeA, Qclass: mdns.ClassINET}},
	}
	rr, err := mdns.NewRR("example.com. 300 IN A 192.0.2.1")
	if err != nil {
		b.Fatal(err)
	}
	msg.Answer = []mdns.RR{rr}
	dnsBytes, err := msg.Pack()
	if err != nil {
		b.Fatal(err)
	}
	return Query{
		SrcIP: netip.MustParseAddr("203.0.113.5"),
		DstIP: netip.MustParseAddr("198.51.100.10"),
		At:    time.Date(2026, 4, 28, 12, 0, 0, 123_000_000, time.UTC),
		DNS:   dnsBytes,
	}
}
