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
	"google.golang.org/protobuf/proto"
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

// ClientResponseBuilder reuses protobuf envelope state for a single producer
// worker. It is not safe for concurrent use.
type ClientResponseBuilder struct {
	dt  dnstap.Dnstap
	msg dnstap.Message

	dtType   dnstap.Dnstap_Type
	msgType  dnstap.Message_Type
	family   dnstap.SocketFamily
	protocol dnstap.SocketProtocol
	src4     [4]byte
	dst4     [4]byte
	src16    [16]byte
	dst16    [16]byte
	srcPort  uint32
	dstPort  uint32
	respSec  uint64
	respNsec uint32

	buf []byte
}

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

// MarshalClientResponse marshals q into b's reusable buffer. The returned
// slice is valid until the next call on the same Builder.
func (b *ClientResponseBuilder) MarshalClientResponse(q Query) ([]byte, error) {
	wire, err := b.MarshalClientResponseAppend(b.buf[:0], q)
	if err != nil {
		return nil, err
	}
	b.buf = wire[:0]
	return wire, nil
}

// MarshalClientResponseAppend appends the marshalled dnstap CLIENT_RESPONSE to
// dst and returns the resulting frame bytes. dst is owned by the caller.
func (b *ClientResponseBuilder) MarshalClientResponseAppend(dst []byte, q Query) ([]byte, error) {
	b.prepare(q)
	return proto.MarshalOptions{}.MarshalAppend(dst, &b.dt)
}

func (b *ClientResponseBuilder) prepare(q Query) {
	at := q.At
	if at.IsZero() {
		at = time.Now()
	}
	b.dtType = dnstap.Dnstap_MESSAGE
	b.msgType = dnstap.Message_CLIENT_RESPONSE
	b.family = dnstap.SocketFamily_INET
	if q.SrcIP.Is6() && !q.SrcIP.Is4In6() {
		b.family = dnstap.SocketFamily_INET6
	}
	b.protocol = dnstap.SocketProtocol_UDP
	b.srcPort = uint32(q.SrcPort)
	if b.srcPort == 0 {
		b.srcPort = 12345
	}
	b.dstPort = uint32(q.DstPort)
	if b.dstPort == 0 {
		b.dstPort = 53
	}
	b.respSec = uint64(at.Unix())
	b.respNsec = uint32(at.Nanosecond())

	var src, dst []byte
	if b.family == dnstap.SocketFamily_INET6 {
		b.src16 = q.SrcIP.As16()
		b.dst16 = q.DstIP.As16()
		src = b.src16[:]
		dst = b.dst16[:]
	} else {
		b.src4 = q.SrcIP.As4()
		b.dst4 = q.DstIP.As4()
		src = b.src4[:]
		dst = b.dst4[:]
	}

	b.msg = dnstap.Message{
		Type:             &b.msgType,
		SocketFamily:     &b.family,
		SocketProtocol:   &b.protocol,
		QueryAddress:     src,
		ResponseAddress:  dst,
		QueryPort:        &b.srcPort,
		ResponsePort:     &b.dstPort,
		ResponseTimeSec:  &b.respSec,
		ResponseTimeNsec: &b.respNsec,
		ResponseMessage:  q.DNS,
	}
	b.dt = dnstap.Dnstap{
		Identity: Identity,
		Type:     &b.dtType,
		Message:  &b.msg,
	}
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
