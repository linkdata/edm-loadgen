package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/linkdata/jaws"
	"github.com/linkdata/jaws/jawsboot"
	"github.com/linkdata/jaws/lib/ui"

	"github.com/linkdata/edm-loadgen/internal/state"
)

//go:embed assets/*.html assets/*.css
var assetsFS embed.FS

// Server bundles the JaWS instance, http.Server, and the broadcast goroutine
// that dirties live-counter tags so the UI updates without polling.
type Server struct {
	jw   *jaws.Jaws
	srv  *http.Server
	st   *state.State
	tick time.Duration
}

// New constructs a JaWS server bound to st. Listening starts when Run is
// called. tick controls how often the broadcast loop refreshes live gauges
// (e.g. 500ms) — the verifier's slower scrape is what supplies the source
// data, this just paces redraws.
func New(st *state.State, listenAddr string, tick time.Duration) (*Server, error) {
	if tick <= 0 {
		tick = 500 * time.Millisecond
	}

	jw, err := jaws.New()
	if err != nil {
		return nil, fmt.Errorf("web: jaws.New: %w", err)
	}

	tmpl, err := template.ParseFS(assetsFS, "assets/*.html")
	if err != nil {
		jw.Close()
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}
	if err := jw.AddTemplateLookuper(tmpl); err != nil {
		jw.Close()
		return nil, fmt.Errorf("web: add template lookuper: %w", err)
	}

	staticFS, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		jw.Close()
		return nil, fmt.Errorf("web: sub static fs: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /jaws/", jw)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// jawsboot.Setup mounts Bootstrap CSS/JS at /jawsboot/* and registers
	// the resulting URLs against our mux's HandleFunc shape.
	bootURLs, err := jawsboot.Setup(jw, mux.Handle, "/jawsboot/")
	if err != nil {
		jw.Close()
		return nil, fmt.Errorf("web: jawsboot.Setup: %w", err)
	}
	headRefs := []string{"/static/styles.css"}
	for _, u := range bootURLs {
		headRefs = append(headRefs, u.String())
	}
	if err := jw.GenerateHeadHTML(headRefs...); err != nil {
		jw.Close()
		return nil, fmt.Errorf("web: GenerateHeadHTML: %w", err)
	}

	page := NewPage(st, jw)
	mux.Handle("GET /", ui.Handler(jw, "index.html", page))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return &Server{jw: jw, srv: srv, st: st, tick: tick}, nil
}

// Run starts the JaWS event loop, the HTTP server, and the broadcast loop.
// Returns when ctx is cancelled or the listener stops.
func (s *Server) Run(ctx context.Context) error {
	go s.jw.Serve()
	go s.broadcastLoop(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		s.jw.Close()
		return ctx.Err()
	case err := <-errCh:
		s.jw.Close()
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// Addr returns the listener address (useful for tests).
func (s *Server) Addr() string { return s.srv.Addr }

// broadcastLoop periodically dirties the live-counter group tags so all the
// gauges and per-pattern rows refresh together. We use Jaws.Dirty rather than
// Request.Dirty because the producer/verifier goroutines do not have a
// request bound; with a single connected operator this is fine.
func (s *Server) broadcastLoop(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Group tags: refresh everything that depends on live counters.
			s.jw.Dirty(&s.st.Sent, &s.st.Observed, &s.st.Received)
		}
	}
}
