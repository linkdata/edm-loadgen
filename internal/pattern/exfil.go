package pattern

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/netip"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Exfil simulates DNS-tunnelling exfiltration: a single attacker zone receives
// a stream of queries whose subdomain labels carry encoded payload bytes.
//
// Three sub-modes:
//
//   - dnscat2: hex-encoded chunks of 30 bytes each, single label.
//   - iodine:  gzip + base32 + 4-byte sequence header, single label.
//   - raw-b32: plain base32 chunks, useful as a baseline.
//
// Each Next call advances through the current "session" payload one chunk at
// a time. When the session is exhausted, a fresh random payload starts.
type Exfil struct {
	st        *state.State
	rng       *rand.Rand
	src       netip.Addr
	session   []byte
	cursor    int
	chunkSize int
	seq       uint32
}

// NewExfil returns a generator. Session payload is generated lazily.
func NewExfil(st *state.State) *Exfil {
	rng := rand.New(rand.NewPCG(0xe2f, uint64(nowFunc().UnixNano())))
	return &Exfil{
		st:        st,
		rng:       rng,
		src:       netip.AddrFrom4([4]byte{198, 51, 100, 31}),
		chunkSize: 30,
	}
}

// Name returns the pattern identifier.
func (e *Exfil) Name() string { return "exfil" }

// Next emits one chunk of the current session's payload as a labelled qname.
func (e *Exfil) Next(ctx context.Context) (q Query, err error) {
	e.st.RLock()
	tool := e.st.Exfil.Tool
	domain := e.st.Exfil.Domain
	payloadBytes := e.st.Exfil.PayloadBytes
	e.st.RUnlock()

	// Refresh the session if exhausted or empty.
	if e.cursor >= len(e.session) {
		e.session = e.makePayload(payloadBytes)
		e.cursor = 0
		e.seq = 0
	}

	end := e.cursor + e.chunkSize
	if end > len(e.session) {
		end = len(e.session)
	}
	chunk := e.session[e.cursor:end]
	e.cursor = end
	e.seq++

	var label string
	switch tool {
	case "iodine":
		label = iodineLabel(e.seq, chunk)
	case "raw-b32":
		label = strictB32(chunk)
	default: // dnscat2
		label = hex.EncodeToString(chunk)
	}
	if len(label) > 63 {
		label = label[:63]
	}
	qname := label + "." + domain

	q = Query{
		QName:   qname,
		QType:   mdns.TypeTXT, // exfil tools commonly use TXT for response payload
		SrcIP:   e.src,
		DstIP:   resolverIP,
		Answers: []mdns.RR{&mdns.TXT{
			Hdr: mdns.RR_Header{Name: mdns.Fqdn(qname), Class: mdns.ClassINET, Ttl: 0, Rrtype: mdns.TypeTXT},
			Txt: []string{fmt.Sprintf("ack=%d", e.seq)},
		}},
		At: nowFunc(),
	}
	return
}

// makePayload returns n random bytes. Real exfil tools would send actual
// captured data; for load-gen any high-entropy stream is enough.
func (e *Exfil) makePayload(n int) []byte {
	if n <= 0 {
		n = 1024
	}
	if n > 1<<20 {
		n = 1 << 20 // cap at 1 MiB per session
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(e.rng.IntN(256))
	}
	return b
}

// iodineLabel encodes a 4-byte big-endian sequence header followed by
// gzip+base32 of the chunk. Real iodine framing is more elaborate; this
// captures the user-visible shape (base32 alphabet + sequence in header).
func iodineLabel(seq uint32, chunk []byte) string {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(chunk)
	_ = gz.Close()
	hdr := []byte{byte(seq >> 24), byte(seq >> 16), byte(seq >> 8), byte(seq)}
	body := append(hdr, buf.Bytes()...)
	return strictB32(body)
}

// strictB32 returns a lowercase base32 string with no padding, since DNS
// labels do not allow '='.
func strictB32(p []byte) string {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	out := enc.EncodeToString(p)
	// Lower-case to keep with normal DNS label style.
	b := make([]byte, len(out))
	for i := 0; i < len(out); i++ {
		c := out[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}
