// Package dns builds raw DNS wire-format payloads for embedding inside dnstap
// envelopes. It is a thin wrapper around github.com/miekg/dns.
package dns

import (
	"fmt"

	mdns "github.com/miekg/dns"
)

// Response packs a DNS response message in wire format. The returned bytes are
// suitable for the ResponseMessage field of a dnstap envelope.
//
// id is the DNS transaction ID. qname is normalised to FQDN by the caller.
func Response(id uint16, qname string, qtype uint16, answers []mdns.RR) (b []byte, err error) {
	msg := new(mdns.Msg)
	msg.Id = id
	msg.Response = true
	msg.RecursionDesired = true
	msg.RecursionAvailable = true
	msg.Question = []mdns.Question{{
		Name:   mdns.Fqdn(qname),
		Qtype:  qtype,
		Qclass: mdns.ClassINET,
	}}
	msg.Answer = answers
	b, err = msg.Pack()
	if err != nil {
		err = fmt.Errorf("packing dns response for %q: %w", qname, err)
	}
	return
}
