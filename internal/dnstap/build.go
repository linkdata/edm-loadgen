// Package dnstap builds [*dnstap.Dnstap] envelopes that EDM will accept on its
// --input-tcp / --input-unix Frame Streams socket.
//
// EDM only processes message types whose name ends in _RESPONSE; QUERY frames
// are silently dropped. We always emit CLIENT_RESPONSE.
package dnstap

import (
	"net/netip"
	"time"

	dnstap "github.com/dnstap/golang-dnstap"
)

// Query is the minimal set of inputs needed to construct a synthetic
// CLIENT_RESPONSE dnstap envelope.
type Query struct {
	// SrcIP is the synthetic client address (becomes QueryAddress).
	SrcIP netip.Addr
	// DstIP is the resolver address we are pretending to be (becomes ResponseAddress).
	DstIP netip.Addr
	// SrcPort defaults to 12345 when zero.
	SrcPort uint16
	// DstPort defaults to 53 when zero.
	DstPort uint16
	// At is the response timestamp; zero means "now".
	At time.Time
	// DNS is the raw DNS wire-format response payload.
	DNS []byte
}

// Identity is reported as the dnstap identity field on every frame. EDM
// surfaces this as session_data.ServerID in its parquet output, which makes it
// easy to confirm in DuckDB that load-gen traffic is what's being recorded.
var Identity = []byte("edm-loadgen")

// NewClientResponse returns a populated *dnstap.Dnstap envelope. It does not
// allocate the protobuf message types via &dnstap.Message{...}; callers should
// pass the result straight to proto.Marshal.
func NewClientResponse(q Query) *dnstap.Dnstap {
	t := dnstap.Message_CLIENT_RESPONSE
	at := q.At
	if at.IsZero() {
		at = time.Now()
	}
	srcPort := uint32(q.SrcPort)
	if srcPort == 0 {
		srcPort = 12345
	}
	dstPort := uint32(q.DstPort)
	if dstPort == 0 {
		dstPort = 53
	}

	fam := dnstap.SocketFamily_INET
	if q.SrcIP.Is6() && !q.SrcIP.Is4In6() {
		fam = dnstap.SocketFamily_INET6
	}

	var srcSlice, dstSlice []byte
	if fam == dnstap.SocketFamily_INET6 {
		s6 := q.SrcIP.As16()
		d6 := q.DstIP.As16()
		srcSlice = s6[:]
		dstSlice = d6[:]
	} else {
		s4 := q.SrcIP.As4()
		d4 := q.DstIP.As4()
		srcSlice = s4[:]
		dstSlice = d4[:]
	}

	proto := uint32(17) // UDP
	respSec := uint64(at.Unix())
	respNsec := uint32(at.Nanosecond())

	dt := &dnstap.Dnstap{
		Identity: Identity,
		Type:     dnstap.Dnstap_MESSAGE.Enum(),
		Message: &dnstap.Message{
			Type:             &t,
			SocketFamily:     &fam,
			SocketProtocol:   familyAwareProtocol(&proto),
			QueryAddress:     srcSlice,
			ResponseAddress:  dstSlice,
			QueryPort:        &srcPort,
			ResponsePort:     &dstPort,
			ResponseTimeSec:  &respSec,
			ResponseTimeNsec: &respNsec,
			ResponseMessage:  q.DNS,
		},
	}
	return dt
}

func familyAwareProtocol(p *uint32) *dnstap.SocketProtocol {
	// EDM expects this field as the SocketProtocol enum, not a raw uint32.
	// 17 (UDP) -> UDP; 6 (TCP) -> TCP. Keep the cast local so callers stay
	// in the integer space the IANA tables use.
	switch *p {
	case 6:
		v := dnstap.SocketProtocol_TCP
		return &v
	default:
		v := dnstap.SocketProtocol_UDP
		return &v
	}
}
