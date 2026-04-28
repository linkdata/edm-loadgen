// edm-loadgen drives synthetic DNS traffic into a running dnstapir-edm
// instance and reconciles what we sent against EDM's /metrics counters.
//
// Subcommands:
//
//	smoke     Send N hand-crafted frames and exit (foundation gate).
//	run       Headless: producer + verifier with a recurring text status line.
//	verify    One-shot snapshot of EDM /metrics.
//
// The serve subcommand (JaWS UI) is not yet wired up.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/dns"
	"github.com/linkdata/edm-loadgen/internal/dnstap"
	"github.com/linkdata/edm-loadgen/internal/mix"
	"github.com/linkdata/edm-loadgen/internal/producer"
	"github.com/linkdata/edm-loadgen/internal/sink"
	"github.com/linkdata/edm-loadgen/internal/state"
	"github.com/linkdata/edm-loadgen/internal/verify"
	"github.com/linkdata/edm-loadgen/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: edm-loadgen <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  smoke   send N hand-crafted frames and exit")
	fmt.Fprintln(os.Stderr, "  run     start producer + verifier with text status")
	fmt.Fprintln(os.Stderr, "  serve   producer + verifier + JaWS web UI")
	fmt.Fprintln(os.Stderr, "  verify  one-shot snapshot of EDM /metrics")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "smoke":
		runSmoke(os.Args[2:])
	case "run":
		runHeadless(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// runSmoke is the foundation gate: send a fixed number of static frames with
// no mixer, no rate limiter, no verifier. Useful for confirming wire-level
// connectivity to a fresh EDM instance.
func runSmoke(argv []string) {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	target := fs.String("target", "tcp://127.0.0.1:53535", "EDM Frame Streams socket")
	count := fs.Int("count", 100, "number of frames to send")
	qname := fs.String("qname", "example.com", "qname to embed in every frame")
	if err := fs.Parse(argv); err != nil {
		os.Exit(2)
	}
	s, err := sink.Dial(*target, sink.Options{Timeout: 5 * time.Second, RetryInterval: 500 * time.Millisecond})
	if err != nil {
		die("dial: %v", err)
	}
	defer s.Close()
	rr, err := mdns.NewRR(*qname + ". 60 IN A 192.0.2.1")
	if err != nil {
		die("build rr: %v", err)
	}
	src := netip.MustParseAddr("203.0.113.5")
	dst := netip.MustParseAddr("198.51.100.10")
	start := time.Now()
	for i := 0; i < *count; i++ {
		dnsBytes, err := dns.Response(uint16(i+1), *qname, mdns.TypeA, []mdns.RR{rr})
		if err != nil {
			die("dns.Response #%d: %v", i, err)
		}
		dt := dnstap.NewClientResponse(dnstap.Query{
			SrcIP: src, DstIP: dst, At: time.Now(), DNS: dnsBytes,
		})
		if err := s.Send(dt); err != nil {
			die("send #%d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("sent %d frames to %s in %s (%.0f fps)\n",
		*count, *target, elapsed, float64(*count)/elapsed.Seconds())
}

// runVerify hits EDM's /metrics once and prints the parsed snapshot as JSON.
func runVerify(argv []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	url := fs.String("metrics-url", "http://127.0.0.1:2112/metrics", "EDM metrics URL")
	if err := fs.Parse(argv); err != nil {
		os.Exit(2)
	}
	sc := verify.NewScraper(*url)
	snap, err := sc.Once(context.Background())
	if err != nil {
		die("verify: %v", err)
	}
	out, _ := json.MarshalIndent(snap, "", "  ")
	fmt.Println(string(out))
}

// runHeadless is the main mode: producer + verifier + recurring status line.
func runHeadless(argv []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	target := fs.String("target", "tcp://127.0.0.1:53535", "EDM Frame Streams socket")
	metricsURL := fs.String("metrics-url", "http://127.0.0.1:2112/metrics", "EDM metrics URL")
	qps := fs.Int("qps", 100, "queries per second")
	duration := fs.Duration("duration", 0, "total run time, 0 = until SIGINT")
	domains := fs.String("well-known-source", "", "CSV/text top-domains list")
	configPath := fs.String("config", "", "JSON config file (overlaid before flags)")
	reportInterval := fs.Duration("report-interval", 5*time.Second, "verifier scrape cadence")
	mixBg := fs.Int("mix.background", 80, "")
	mixWk := fs.Int("mix.wellknown", 0, "")
	mixDga := fs.Int("mix.dga", 5, "")
	mixBeacon := fs.Int("mix.beacon", 2, "")
	mixFf := fs.Int("mix.fastflux", 1, "")
	mixDd := fs.Int("mix.dyndns", 2, "")
	mixExfil := fs.Int("mix.exfil", 5, "")
	mixExotic := fs.Int("mix.exotic", 3, "")
	mixEva := fs.Int("mix.evasion", 2, "")
	if err := fs.Parse(argv); err != nil {
		os.Exit(2)
	}

	// Track which flags were explicitly set on the command line so that a
	// config file can supply values that flags don't override.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	st := state.New()
	if *configPath != "" {
		cfg, err := state.LoadConfigFile(*configPath)
		if err != nil {
			die("%v", err)
		}
		if err := cfg.Apply(st); err != nil {
			die("%v", err)
		}
	}
	if set["target"] || *configPath == "" {
		st.Target = *target
	}
	if set["metrics-url"] || *configPath == "" {
		st.MetricsURL = *metricsURL
	}
	if set["report-interval"] || *configPath == "" {
		st.ReportInterval = *reportInterval
	}
	if set["qps"] || *configPath == "" {
		atomic.StoreInt32(&st.QPS, int32(*qps))
	}
	overlayMix(st, fs, set, *configPath == "",
		mixBg, mixWk, mixDga, mixBeacon, mixFf, mixDd, mixExfil, mixExotic, mixEva)
	st.SetRunning(true)

	mixer, beacon, err := mix.FromState(st, *domains)
	if err != nil {
		die("mix: %v", err)
	}
	snk, err := sink.Dial(*target, sink.Options{Timeout: 5 * time.Second, RetryInterval: 500 * time.Millisecond})
	if err != nil {
		die("dial: %v", err)
	}
	defer snk.Close()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if *duration > 0 {
		var c context.CancelFunc
		rootCtx, c = context.WithTimeout(rootCtx, *duration)
		defer c()
	}

	// Signal handling so Ctrl-C exits cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	prod := producer.New(st, mixer, beacon, snk)
	go func() { _ = prod.Run(rootCtx) }()

	scraper := verify.NewScraper(*metricsURL)
	go func() { _ = scraper.Run(rootCtx, st, *reportInterval) }()

	statusLoop(rootCtx, st)
}

// statusLoop prints a recurring summary line every report-interval until ctx
// is cancelled. The very first line waits one tick so the verifier has a
// real snapshot.
func statusLoop(ctx context.Context, st *state.State) {
	st.RLock()
	interval := st.ReportInterval
	st.RUnlock()
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			fmt.Println(formatStatus(st, time.Since(start)))
			return
		case <-t.C:
			fmt.Println(formatStatus(st, time.Since(start)))
		}
	}
}

func formatStatus(st *state.State, elapsed time.Duration) string {
	rep := verify.Reconcile(st)
	return fmt.Sprintf(
		"t=%s sent=%d edm_processed=%d (drift %+d) edm_new_qname=%d edm_ignored=%d  bg:%d wk:%d dga:%d beacon:%d ff:%d dyn:%d exfil:%d exotic:%d eva:%d",
		fmtElapsed(elapsed),
		rep.SentTotal, rep.EDMProcessed, rep.Drift,
		rep.EDMNewQname, rep.EDMIgnoredTotal,
		rep.PerPattern["background"],
		rep.PerPattern["wellknown"],
		rep.PerPattern["dga"],
		rep.PerPattern["beacon"],
		rep.PerPattern["fastflux"],
		rep.PerPattern["dyndns"],
		rep.PerPattern["exfil"],
		rep.PerPattern["exotic"],
		rep.PerPattern["evasion"],
	)
}

func fmtElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// overlayMix writes the per-pattern mix flags into st, but only for flags the
// user explicitly set (or all of them if no config file was supplied so the
// flag defaults still take effect).
func overlayMix(st *state.State, fs *flag.FlagSet, set map[string]bool, noConfig bool,
	bg, wk, dga, beacon, ff, dd, exfil, exotic, eva *int) {
	maybe := func(name string, p *int, dst *int32) {
		if set[name] || noConfig {
			atomic.StoreInt32(dst, int32(*p))
		}
	}
	maybe("mix.background", bg, &st.Mix.Background)
	maybe("mix.wellknown", wk, &st.Mix.WellKnown)
	maybe("mix.dga", dga, &st.Mix.DGA)
	maybe("mix.beacon", beacon, &st.Mix.Beacon)
	maybe("mix.fastflux", ff, &st.Mix.FastFlux)
	maybe("mix.dyndns", dd, &st.Mix.DynDNS)
	maybe("mix.exfil", exfil, &st.Mix.Exfil)
	maybe("mix.exotic", exotic, &st.Mix.Exotic)
	maybe("mix.evasion", eva, &st.Mix.Evasion)
}

// runServe is identical to runHeadless except it also starts the JaWS web UI
// and skips the recurring text status line (the UI is the status surface).
func runServe(argv []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	target := fs.String("target", "tcp://127.0.0.1:53535", "EDM Frame Streams socket")
	metricsURL := fs.String("metrics-url", "http://127.0.0.1:2112/metrics", "EDM metrics URL")
	listen := fs.String("listen", ":8090", "HTTP listen address for the JaWS UI")
	qps := fs.Int("qps", 100, "starting queries per second")
	domains := fs.String("well-known-source", "", "CSV/text top-domains list")
	configPath := fs.String("config", "", "JSON config file (overlaid before flags)")
	reportInterval := fs.Duration("report-interval", 2*time.Second, "verifier scrape cadence")
	uiTick := fs.Duration("ui-tick", 500*time.Millisecond, "UI broadcast cadence (live counters)")
	startStopped := fs.Bool("start-stopped", false, "do not begin generating until the UI Run button is pressed")
	if err := fs.Parse(argv); err != nil {
		os.Exit(2)
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	st := state.New()
	if *configPath != "" {
		cfg, err := state.LoadConfigFile(*configPath)
		if err != nil {
			die("%v", err)
		}
		if err := cfg.Apply(st); err != nil {
			die("%v", err)
		}
	}
	if set["target"] || *configPath == "" {
		st.Target = *target
	}
	if set["metrics-url"] || *configPath == "" {
		st.MetricsURL = *metricsURL
	}
	if set["report-interval"] || *configPath == "" {
		st.ReportInterval = *reportInterval
	}
	if set["qps"] || *configPath == "" {
		atomic.StoreInt32(&st.QPS, int32(*qps))
	}
	st.SetRunning(!*startStopped)

	mixer, beacon, err := mix.FromState(st, *domains)
	if err != nil {
		die("mix: %v", err)
	}
	snk, err := sink.Dial(*target, sink.Options{Timeout: 5 * time.Second, RetryInterval: 500 * time.Millisecond})
	if err != nil {
		die("dial: %v", err)
	}
	defer snk.Close()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	srv, err := web.New(st, *listen, *uiTick)
	if err != nil {
		die("web: %v", err)
	}

	prod := producer.New(st, mixer, beacon, snk)
	go func() { _ = prod.Run(rootCtx) }()

	scraper := verify.NewScraper(*metricsURL)
	go func() { _ = scraper.Run(rootCtx, st, *reportInterval) }()

	fmt.Fprintf(os.Stderr, "edm-loadgen UI on http://%s/\n", *listen)
	if err := srv.Run(rootCtx); err != nil && err != context.Canceled {
		die("serve: %v", err)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
