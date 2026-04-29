// Package dns builds raw DNS wire-format payloads for embedding inside dnstap
// envelopes. It is a thin wrapper around github.com/miekg/dns.
package dns

import (
	"fmt"

	mdns "github.com/miekg/dns"
)

// Builder reuses the DNS message and pack buffer for a single producer worker.
// It is not safe for concurrent use. The returned wire slice is valid until
// the next Builder method call.
type Builder struct {
	msg      mdns.Msg
	question [1]mdns.Question
	packBuf  []byte
}

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

// Response packs a DNS response message in wire format using b's reusable
// buffer. The returned slice is overwritten by the next call on the same
// Builder.
func (b *Builder) Response(id uint16, qname string, qtype uint16, answers []mdns.RR) (wire []byte, err error) {
	b.question[0] = mdns.Question{
		Name:   mdns.Fqdn(qname),
		Qtype:  qtype,
		Qclass: mdns.ClassINET,
	}
	b.msg = mdns.Msg{
		MsgHdr: mdns.MsgHdr{
			Id:                 id,
			Response:           true,
			RecursionDesired:   true,
			RecursionAvailable: true,
		},
		Question: b.question[:],
		Answer:   answers,
	}
	wire, err = b.msg.PackBuffer(b.packBuf[:0])
	if err != nil {
		err = fmt.Errorf("packing dns response for %q: %w", qname, err)
		return nil, err
	}
	b.packBuf = wire[:0]
	return
}
