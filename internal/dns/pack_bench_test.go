package dns

import (
	"testing"

	mdns "github.com/miekg/dns"
)

var benchmarkDNSBytes int

func BenchmarkResponse(b *testing.B) {
	benchmarkResponseCases(b, func(id uint16, qname string, qtype uint16, answers []mdns.RR) ([]byte, error) {
		return Response(id, qname, qtype, answers)
	})
}

func BenchmarkBuilderResponse(b *testing.B) {
	var builder Builder
	benchmarkResponseCases(b, builder.Response)
}

func benchmarkResponseCases(b *testing.B, pack func(uint16, string, uint16, []mdns.RR) ([]byte, error)) {
	for _, tc := range []struct {
		name  string
		qname string
		qtype uint16
		rr    mdns.RR
	}{
		{
			name:  "A",
			qname: "example.com",
			qtype: mdns.TypeA,
			rr:    mustBenchmarkRR(b, "example.com. 300 IN A 192.0.2.1"),
		},
		{
			name:  "TXT",
			qname: "example.com",
			qtype: mdns.TypeTXT,
			rr:    mustBenchmarkRR(b, `example.com. 300 IN TXT "v=spf1 -all"`),
		},
	} {
		b.Run(tc.name, func(b *testing.B) {
			answers := []mdns.RR{tc.rr}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				wire, err := pack(uint16(i), tc.qname, tc.qtype, answers)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkDNSBytes = len(wire)
			}
		})
	}
}

func mustBenchmarkRR(b *testing.B, s string) mdns.RR {
	b.Helper()
	rr, err := mdns.NewRR(s)
	if err != nil {
		b.Fatal(err)
	}
	return rr
}
