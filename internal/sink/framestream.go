// Package sink ships dnstap envelopes to EDM over a Frame Streams socket.
//
// It wraps [dnstap.NewSocketWriter] from github.com/dnstap/golang-dnstap, which
// already handles the bidirectional handshake and reconnects on transient
// errors. Sink adds: target-URL parsing (tcp://, unix://), proto.Marshal of the
// envelope, and a typed Send method that callers actually want to use.
package sink

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	dnstap "github.com/dnstap/golang-dnstap"
	"google.golang.org/protobuf/proto"
)

// ErrTargetScheme is returned when ParseTarget receives an unsupported scheme.
var ErrTargetScheme = errors.New("sink: target scheme must be tcp:// or unix://")

// Sink is a long-lived connection to an EDM Frame Streams input. It is safe
// for concurrent use by a single producer goroutine; the underlying SocketWriter
// serialises writes internally via the framestream encoder.
type Sink struct {
	w dnstap.Writer
}

// Options tune the underlying SocketWriter. Zero values pick library defaults.
type Options struct {
	// Timeout is the per-write deadline. Default 5s.
	Timeout time.Duration
	// RetryInterval is how long the SocketWriter waits between reconnect
	// attempts after a write failure. Default 1s.
	RetryInterval time.Duration
}

// Dial parses target ("tcp://host:port" or "unix:///path") and returns a Sink.
// The connection is lazy: the first Send establishes it, and Send blocks
// indefinitely while the SocketWriter retries.
func Dial(target string, opt Options) (s *Sink, err error) {
	addr, err := ParseTarget(target)
	if err != nil {
		return
	}
	to := opt.Timeout
	if to == 0 {
		to = 5 * time.Second
	}
	ri := opt.RetryInterval
	if ri == 0 {
		ri = 1 * time.Second
	}
	// NOTE: dnstap.SocketWriterOptions.Dialer documents that a nil dialer
	// defaults to "a 30 second timeout dialer", but openWriter() actually
	// nil-dereferences. Workaround: always supply one.
	w := dnstap.NewSocketWriter(addr, &dnstap.SocketWriterOptions{
		Timeout:       to,
		RetryInterval: ri,
		Dialer:        &net.Dialer{Timeout: 5 * time.Second},
	})
	s = &Sink{w: w}
	return
}

// Send marshals dt and writes it as a single dnstap frame.
func (s *Sink) Send(dt *dnstap.Dnstap) error {
	frame, err := proto.Marshal(dt)
	if err != nil {
		return fmt.Errorf("sink: marshal: %w", err)
	}
	if _, err = s.w.WriteFrame(frame); err != nil {
		return fmt.Errorf("sink: writeframe: %w", err)
	}
	return nil
}

// Close releases the underlying socket.
func (s *Sink) Close() error {
	return s.w.Close()
}

// ParseTarget converts "tcp://127.0.0.1:53535" or "unix:///run/edm.sock" into a
// net.Addr suitable for dnstap.NewSocketWriter.
func ParseTarget(target string) (net.Addr, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("sink: parse target %q: %w", target, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "tcp":
		return net.ResolveTCPAddr("tcp", u.Host)
	case "unix":
		path := u.Path
		if u.Host != "" && path == "" {
			path = u.Host
		}
		return net.ResolveUnixAddr("unix", path)
	default:
		return nil, fmt.Errorf("%w: got %q", ErrTargetScheme, u.Scheme)
	}
}
