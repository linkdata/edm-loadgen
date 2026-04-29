package producer

import (
	"context"
	"net/netip"
	"testing"

	gdnstap "github.com/dnstap/golang-dnstap"
	mdns "github.com/miekg/dns"
	"google.golang.org/protobuf/proto"

	"github.com/linkdata/edm-loadgen/internal/pattern"
	"github.com/linkdata/edm-loadgen/internal/state"
)

func TestFrameBuilderGeneratedQueries(t *testing.T) {
	st := state.New()
	st.Exotic.PayloadBytesMin = 16
	st.Exotic.PayloadBytesMax = 120
	bg, err := pattern.NewBackground(st, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	ff, err := pattern.NewFastFlux(st)
	if err != nil {
		t.Fatal(err)
	}
	gens := []pattern.Generator{
		bg,
		pattern.NewDGA(st),
		ff,
		pattern.NewDynDNS(st),
		pattern.NewExfil(st),
		pattern.NewExotic(st, bg),
	}

	ctx := context.Background()
	var builder frameBuilder
	for _, gen := range gens {
		q, err := gen.Next(ctx)
		if err != nil {
			t.Fatalf("%s.Next: %v", gen.Name(), err)
		}
		frame, err := builder.build(q)
		if err != nil {
			t.Fatalf("%s build: %v", gen.Name(), err)
		}
		assertFrameMatchesQuery(t, frame, q)
	}
}

func TestFrameBuilderIPv6(t *testing.T) {
	q := pattern.Query{
		QName: "example.com",
		QType: mdns.TypeAAAA,
		SrcIP: netip.MustParseAddr("2001:db8::1"),
		DstIP: netip.MustParseAddr("2001:db8::53"),
	}
	var builder frameBuilder
	frame, err := builder.build(q)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	dt := decodeFrame(t, frame)
	if dt.Message.GetSocketFamily() != gdnstap.SocketFamily_INET6 {
		t.Errorf("SocketFamily = %v, want INET6", dt.Message.GetSocketFamily())
	}
	if got := len(dt.Message.QueryAddress); got != 16 {
		t.Errorf("QueryAddress length = %d, want 16", got)
	}
	assertFrameMatchesQuery(t, frame, q)
}

func assertFrameMatchesQuery(t *testing.T, frame []byte, q pattern.Query) {
	t.Helper()
	dt := decodeFrame(t, frame)
	if dt.Message.GetType() != gdnstap.Message_CLIENT_RESPONSE {
		t.Fatalf("Message.Type = %v, want CLIENT_RESPONSE", dt.Message.GetType())
	}
	msg := new(mdns.Msg)
	if err := msg.Unpack(dt.Message.ResponseMessage); err != nil {
		t.Fatalf("ResponseMessage Unpack: %v", err)
	}
	if len(msg.Question) != 1 {
		t.Fatalf("Question count = %d, want 1", len(msg.Question))
	}
	if got, want := msg.Question[0].Name, mdns.Fqdn(q.QName); got != want {
		t.Errorf("Question.Name = %q, want %q", got, want)
	}
	if got := msg.Question[0].Qtype; got != q.QType {
		t.Errorf("Question.Qtype = %d, want %d", got, q.QType)
	}
}

func decodeFrame(t *testing.T, frame []byte) *gdnstap.Dnstap {
	t.Helper()
	dt := new(gdnstap.Dnstap)
	if err := proto.Unmarshal(frame, dt); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if dt.Message == nil {
		t.Fatal("Message is nil")
	}
	return dt
}
