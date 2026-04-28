// Package web hosts the JaWS-driven control panel for edm-loadgen.
//
// The page model exposes a set of methods that the index.html template binds
// to JaWS widgets ($.Range, $.Span, $.Button, $.Text). Per the JaWS skill
// rules, all tags are pointers into state.State so dirties target the right
// dependencies and survive struct refactors.
package web

import (
	"html/template"
	"strconv"

	"github.com/linkdata/jaws"
	"github.com/linkdata/jaws/lib/bind"
	"github.com/linkdata/jaws/lib/ui"

	"github.com/linkdata/edm-loadgen/internal/state"
)

// Page is the template root ("dot") for index.html. It is comparable (a
// pointer to a struct holding only a *state.State and a *jaws.Jaws), which
// JaWS requires for the dot.
type Page struct {
	st *state.State
	jw *jaws.Jaws
}

// NewPage constructs a Page bound to the running state and JaWS instance.
func NewPage(st *state.State, jw *jaws.Jaws) *Page {
	return &Page{st: st, jw: jw}
}

// State returns the underlying *state.State for code paths that need direct
// access (e.g. the producer goroutine).
func (p *Page) State() *state.State { return p.st }

// ----- Connection knobs --------------------------------------------------

// Target is bound by an InputText widget. The field is editable but only
// before the producer is started; the template marks the widget readonly
// based on Running.
func (p *Page) Target() bind.Setter[string] {
	return bind.New(p.st.Locker(), &p.st.Target)
}

// MetricsURL same.
func (p *Page) MetricsURL() bind.Setter[string] {
	return bind.New(p.st.Locker(), &p.st.MetricsURL)
}

// ----- Pacing & run/stop -------------------------------------------------

// QPS is the global rate knob. Bound to a Range widget (slider).
func (p *Page) QPS() bind.Binder[int32] {
	return AtomicInt32(p.st.Locker(), &p.st.QPS)
}

// RunButton emits a button labelled "Run" or "Stop" and toggles state.Running
// on click. Returns a ui.Object so the dot ownership stays clean.
func (p *Page) RunButton() ui.Object {
	label := bind.HTMLGetterFunc(func(*jaws.Element) template.HTML {
		if p.st.IsRunning() {
			return "Stop"
		}
		return "Run"
	}, &p.st.Running)
	return ui.New(label).Clicked(func(_ ui.Object, elem *jaws.Element, _ jaws.Click) error {
		p.st.SetRunning(!p.st.IsRunning())
		elem.Request.Dirty(&p.st.Running)
		return nil
	})
}

// ----- Mix weight sliders ------------------------------------------------

// MixBackground binds the background pattern weight.
func (p *Page) MixBackground() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.Background) }
// MixWellKnown binds the wellknown wrapper weight.
func (p *Page) MixWellKnown() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.WellKnown) }
// MixDGA binds the DGA pattern weight.
func (p *Page) MixDGA() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.DGA) }
// MixBeacon binds the beacon pattern weight.
func (p *Page) MixBeacon() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.Beacon) }
// MixFastFlux binds the fastflux pattern weight.
func (p *Page) MixFastFlux() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.FastFlux) }
// MixDynDNS binds the dyn-DNS pattern weight.
func (p *Page) MixDynDNS() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.DynDNS) }
// MixExfil binds the exfil pattern weight.
func (p *Page) MixExfil() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.Exfil) }
// MixExotic binds the exotic pattern weight.
func (p *Page) MixExotic() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.Exotic) }
// MixEvasion binds the evasion pattern weight.
func (p *Page) MixEvasion() bind.Binder[int32] { return AtomicInt32(p.st.Locker(), &p.st.Mix.Evasion) }

// ----- Live counters -----------------------------------------------------

// SentTotal is the load-gen's total send count.
func (p *Page) SentTotal() bind.HTMLGetter { return AtomicInt64Getter(&p.st.Sent.Total, &p.st.Sent) }

// EDMProcessed mirrors edm_processed_dnstap_total.
func (p *Page) EDMProcessed() bind.HTMLGetter {
	return AtomicInt64Getter(&p.st.Observed.EDMProcessed, &p.st.Observed)
}

// EDMNewQname mirrors edm_new_qname_queued_total.
func (p *Page) EDMNewQname() bind.HTMLGetter {
	return AtomicInt64Getter(&p.st.Observed.EDMNewQname, &p.st.Observed)
}

// EDMIgnored mirrors the sum of edm_ignored_*_total.
func (p *Page) EDMIgnored() bind.HTMLGetter {
	return AtomicInt64Getter(&p.st.Observed.EDMIgnoredTotal, &p.st.Observed)
}

// EDMCryptopanHits mirrors edm_cryptopan_lru_hit_total.
func (p *Page) EDMCryptopanHits() bind.HTMLGetter {
	return AtomicInt64Getter(&p.st.Observed.EDMCryptopanHits, &p.st.Observed)
}

// Drift renders sent − observed as a signed integer.
func (p *Page) Drift() bind.HTMLGetter {
	return DriftGetter(&p.st.Sent.Total, &p.st.Observed.EDMProcessed, &p.st.Observed, &p.st.Sent)
}

// PerPattern returns a slice describing each pattern's sent count. The
// template renders this as a table; each row's identity tag is its
// SentField pointer so a single Request.Dirty(&state.Sent) refreshes them
// all together.
type PatternRow struct {
	Name string
	Sent *int64
}

// Patterns lists patterns in display order.
func (p *Page) Patterns() []PatternRow {
	return []PatternRow{
		{"background", &p.st.Sent.Background},
		{"wellknown", &p.st.Sent.WellKnown},
		{"dga", &p.st.Sent.DGA},
		{"beacon", &p.st.Sent.Beacon},
		{"fastflux", &p.st.Sent.FastFlux},
		{"dyndns", &p.st.Sent.DynDNS},
		{"exfil", &p.st.Sent.Exfil},
		{"exotic", &p.st.Sent.Exotic},
		{"evasion", &p.st.Sent.Evasion},
	}
}

// SentCount renders an individual PatternRow's count for use inside the
// Patterns range.
func (r PatternRow) SentCount() bind.HTMLGetter {
	return AtomicInt64Getter(r.Sent)
}

// QPSText shows the current QPS knob value next to the slider.
func (p *Page) QPSText() bind.HTMLGetter {
	return bind.HTMLGetterFunc(func(*jaws.Element) template.HTML {
		v := atomicLoad32(&p.st.QPS)
		return template.HTML(strconv.FormatInt(int64(v), 10)) // #nosec G203
	}, &p.st.QPS)
}
