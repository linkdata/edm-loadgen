// edm-loadgen drives synthetic DNS traffic into a running dnstapir-edm
// instance and reconciles what we sent against EDM's /metrics counters.
//
// Subcommands:
//
//	smoke     Send N hand-crafted frames and exit (foundation gate).
//	run       Headless: producer + verifier with a recurring text status line.
//	serve     Producer + verifier + JaWS web UI.
//	verify    One-shot snapshot of EDM /metrics.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // attached to a non-default mux only when --pprof is set
	"net/netip"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	mdns "github.com/miekg/dns"

	"github.com/linkdata/edm-loadgen/internal/dns"
	"github.com/linkdata/edm-loadgen/internal/dnstap"
	"github.com/linkdata/edm-loadgen/internal/mqtt"
	"github.com/linkdata/edm-loadgen/internal/pattern"
	"github.com/linkdata/edm-loadgen/internal/pki"
	"github.com/linkdata/edm-loadgen/internal/producer"
	"github.com/linkdata/edm-loadgen/internal/sink"
	"github.com/linkdata/edm-loadgen/internal/state"
	"github.com/linkdata/edm-loadgen/internal/verify"
	"github.com/linkdata/edm-loadgen/internal/web"
)

var appLog = slog.New(slog.NewTextHandler(os.Stderr, nil))

func usage() {
	fmt.Fprintln(os.Stderr, "usage: edm-loadgen <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  benchmark  run producer at unlimited speed for a fixed duration")
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
	case "benchmark":
		runBenchmark(os.Args[2:])
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
	defaultTarget := defaultTarget()
	target := fs.String("target", defaultTarget, "EDM Frame Streams socket")
	metricsURL := fs.String("metrics-url", defaultMetricsURL(defaultTarget), "EDM metrics URL")
	qps := fs.Int("qps", 100, "queries per second")
	workers := fs.Int("workers", 0, "number of parallel builder goroutines (0 = GOMAXPROCS)")
	pprofAddr := fs.String("pprof", "", "if set, serve net/http/pprof at this address (e.g. :6061)")
	duration := fs.Duration("duration", 0, "total run time, 0 = until SIGINT")
	domains := fs.String("well-known-source", "", "CSV/text top-domains list")
	configPath := fs.String("config", "", "JSON config file (overlaid before flags)")
	edmPath := fs.String("edm", "", "optional EDM binary or source checkout to launch alongside the load generator")
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
	mqttListen := fs.String("mqtt-listen", "", `enable embedded MQTT broker on this address (e.g. ":8883"); empty = off`)
	mqttKeysDir := fs.String("mqtt-keys-dir", envOr("KEYS_DIR", "./keys"), "directory for the MQTT broker's TLS material (auto-generated on first use)")
	mqttNodeName := fs.String("mqtt-node-name", envOr("NODE_NAME", "edm-loadgen-1"), "EDM node name; topic prefix and client-id stem")
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
	if set["mqtt-listen"] || *configPath == "" {
		st.MQTT.Listen = *mqttListen
	}
	if set["mqtt-keys-dir"] || *configPath == "" {
		st.MQTT.KeysDir = *mqttKeysDir
	}
	if set["mqtt-node-name"] || *configPath == "" {
		st.MQTT.NodeName = *mqttNodeName
	}
	st.MQTT.Listen = defaultMQTTListen(*edmPath, st.MQTT.Listen, set["mqtt-listen"])
	st.SetRunning(true)

	domainList, err := pattern.LoadDomains(*domains)
	if err != nil {
		die("domains: %v", err)
	}
	beacon := pattern.NewBeacon(st)
	snk, err := sink.Dial(st.Target, sink.Options{Timeout: 5 * time.Second, RetryInterval: 500 * time.Millisecond})
	if err != nil {
		die("dial: %v", err)
	}
	defer snk.Close()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling so Ctrl-C exits cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	mqttBundle, err := startMQTT(rootCtx, appLog, st)
	if err != nil {
		die("mqtt: %v", err)
	}
	edmProc, err := startEDMIfRequested(rootCtx, appLog, *edmPath, st, mqttBundle)
	if err != nil {
		die("edm: %v", err)
	}
	if edmProc != nil {
		defer edmProc.Stop()
	}
	startPprof(appLog, *pprofAddr)

	runCtx := rootCtx
	if *duration > 0 {
		var c context.CancelFunc
		runCtx, c = context.WithTimeout(rootCtx, *duration)
		defer c()
	}

	prod := producer.New(st, domainList, beacon, snk, *workers)
	go func() { _ = prod.Run(runCtx) }()

	scraper := verify.NewScraper(st.MetricsURL)
	go func() { _ = scraper.Run(runCtx, st, st.ReportInterval) }()

	statusLoop(runCtx, st)
}

type benchmarkResult struct {
	DurationSeconds       float64          `json:"duration_seconds"`
	FirstFrameWaitSeconds float64          `json:"first_frame_wait_seconds"`
	SentTotal             int64            `json:"sent_total"`
	FramesPerSecond       int64            `json:"frames_per_second"`
	EDMProcessed          int64            `json:"edm_processed"`
	EDMNewQname           int64            `json:"edm_new_qname"`
	EDMIgnoredTotal       int64            `json:"edm_ignored_total"`
	Drift                 int64            `json:"drift"`
	MQTTReceived          int64            `json:"mqtt_received"`
	MQTTEDMTopic          int64            `json:"mqtt_edm_topic"`
	PerPattern            map[string]int64 `json:"per_pattern"`
	InterruptedEarly      bool             `json:"interrupted_early"`
}

func runBenchmark(argv []string) {
	fs := flag.NewFlagSet("benchmark", flag.ExitOnError)
	defaultTarget := defaultTarget()
	target := fs.String("target", defaultTarget, "EDM Frame Streams socket")
	metricsURL := fs.String("metrics-url", defaultMetricsURL(defaultTarget), "EDM metrics URL")
	workers := fs.Int("workers", 0, "number of parallel builder goroutines (0 = GOMAXPROCS)")
	pprofAddr := fs.String("pprof", "", "if set, serve net/http/pprof at this address (e.g. :6061)")
	duration := fs.Duration("duration", 10*time.Second, "benchmark duration")
	domains := fs.String("well-known-source", "", "CSV/text top-domains list")
	configPath := fs.String("config", "", "JSON config file (overlaid before flags)")
	edmPath := fs.String("edm", "", "optional EDM binary or source checkout to launch alongside the load generator")
	mixBg := fs.Int("mix.background", 80, "")
	mixWk := fs.Int("mix.wellknown", 0, "")
	mixDga := fs.Int("mix.dga", 5, "")
	mixBeacon := fs.Int("mix.beacon", 2, "")
	mixFf := fs.Int("mix.fastflux", 1, "")
	mixDd := fs.Int("mix.dyndns", 2, "")
	mixExfil := fs.Int("mix.exfil", 5, "")
	mixExotic := fs.Int("mix.exotic", 3, "")
	mixEva := fs.Int("mix.evasion", 2, "")
	mqttListen := fs.String("mqtt-listen", "", `enable embedded MQTT broker on this address (e.g. ":8883"); empty = off`)
	mqttKeysDir := fs.String("mqtt-keys-dir", envOr("KEYS_DIR", "./keys"), "directory for the MQTT broker's TLS material (auto-generated on first use)")
	mqttNodeName := fs.String("mqtt-node-name", envOr("NODE_NAME", "edm-loadgen-1"), "EDM node name; topic prefix and client-id stem")
	if err := fs.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *duration <= 0 {
		die("benchmark: --duration must be greater than 0")
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
	overlayMix(st, fs, set, *configPath == "",
		mixBg, mixWk, mixDga, mixBeacon, mixFf, mixDd, mixExfil, mixExotic, mixEva)
	if set["mqtt-listen"] || *configPath == "" {
		st.MQTT.Listen = *mqttListen
	}
	if set["mqtt-keys-dir"] || *configPath == "" {
		st.MQTT.KeysDir = *mqttKeysDir
	}
	if set["mqtt-node-name"] || *configPath == "" {
		st.MQTT.NodeName = *mqttNodeName
	}
	st.MQTT.Listen = defaultMQTTListen(*edmPath, st.MQTT.Listen, set["mqtt-listen"])
	st.SetRunning(true)

	domainList, err := pattern.LoadDomains(*domains)
	if err != nil {
		die("domains: %v", err)
	}
	beacon := pattern.NewBeacon(st)
	snk, err := sink.Dial(st.Target, sink.Options{Timeout: 5 * time.Second, RetryInterval: 500 * time.Millisecond})
	if err != nil {
		die("dial: %v", err)
	}
	defer snk.Close()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		cancel()
	}()

	mqttBundle, err := startMQTT(rootCtx, appLog, st)
	if err != nil {
		die("mqtt: %v", err)
	}
	edmProc, err := startEDMIfRequested(rootCtx, appLog, *edmPath, st, mqttBundle)
	if err != nil {
		die("edm: %v", err)
	}
	if edmProc != nil {
		defer edmProc.Stop()
	}
	startPprof(appLog, *pprofAddr)

	scraper := verify.NewScraper(st.MetricsURL)
	_ = scraper.Update(rootCtx, st)

	runCtx, stopRun := context.WithCancel(rootCtx)
	defer stopRun()
	prod := producer.NewUnbounded(st, domainList, beacon, snk, *workers)
	producerStarted := time.Now()
	prodDone := make(chan error, 1)
	go func() { prodDone <- prod.Run(runCtx) }()

	start, err := waitForFirstBenchmarkFrame(rootCtx, st, prodDone)
	if err != nil {
		stopRun()
		select {
		case <-prodDone:
		default:
		}
		die("benchmark: %v", err)
	}
	firstFrameWait := start.Sub(producerStarted)
	appLog.Info("benchmark timing started", "sent_total", atomic.LoadInt64(&st.Sent.Total))
	progressDone := make(chan struct{})
	go logBenchmarkProgress(runCtx, st, start, progressDone)
	defer func() {
		if progressDone != nil {
			stopRun()
			<-progressDone
		}
	}()

	timer := time.NewTimer(*duration)
	defer timer.Stop()
	interruptedEarly := false
	prodStopped := false
	var prodErr error
	select {
	case <-timer.C:
	case <-rootCtx.Done():
		interruptedEarly = true
	case prodErr = <-prodDone:
		prodStopped = true
		interruptedEarly = true
	}
	end := time.Now()
	if !prodStopped {
		stopRun()
		prodErr = <-prodDone
	}
	if prodStopped && prodErr != nil && rootCtx.Err() == nil {
		die("benchmark: producer stopped early: %v", prodErr)
	}
	if prodErr != nil && !errors.Is(prodErr, context.Canceled) && !errors.Is(prodErr, context.DeadlineExceeded) {
		die("benchmark: producer: %v", prodErr)
	}
	elapsed := end.Sub(start)
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}
	stopRun()
	<-progressDone
	progressDone = nil
	_ = scraper.Update(context.Background(), st)

	rep := verify.Reconcile(st)
	result := benchmarkResult{
		DurationSeconds:       elapsed.Seconds(),
		FirstFrameWaitSeconds: firstFrameWait.Seconds(),
		SentTotal:             rep.SentTotal,
		FramesPerSecond:       roundRate(rep.SentTotal, elapsed),
		EDMProcessed:          rep.EDMProcessed,
		EDMNewQname:           rep.EDMNewQname,
		EDMIgnoredTotal:       rep.EDMIgnoredTotal,
		Drift:                 rep.Drift,
		MQTTReceived:          atomic.LoadInt64(&st.Received.Total),
		MQTTEDMTopic:          atomic.LoadInt64(&st.Received.EDMTopic),
		PerPattern:            rep.PerPattern,
		InterruptedEarly:      interruptedEarly,
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		die("benchmark: marshal result: %v", err)
	}
	fmt.Println(string(out))
}

func logBenchmarkProgress(ctx context.Context, st *state.State, start time.Time, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var prevTotal int64
	prevAt := start
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			total := atomic.LoadInt64(&st.Sent.Total)
			delta := total - prevTotal
			elapsed := now.Sub(prevAt)
			appLog.Info("benchmark progress",
				"elapsed", now.Sub(start).Round(time.Millisecond).String(),
				"sent_total", total,
				"frames_per_second", roundRate(delta, elapsed),
			)
			prevTotal = total
			prevAt = now
		}
	}
}

func roundRate(n int64, d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(math.Round(float64(n) / d.Seconds()))
}

func waitForFirstBenchmarkFrame(ctx context.Context, st *state.State, prodDone <-chan error) (time.Time, error) {
	if atomic.LoadInt64(&st.Sent.Total) > 0 {
		return time.Now(), nil
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-prodDone:
			if err == nil {
				err = errors.New("producer exited")
			}
			return time.Time{}, fmt.Errorf("producer stopped before first frame: %w", err)
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case now := <-ticker.C:
			if atomic.LoadInt64(&st.Sent.Total) > 0 {
				return now, nil
			}
		}
	}
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
	defaultTarget := defaultTarget()
	target := fs.String("target", defaultTarget, "EDM Frame Streams socket")
	metricsURL := fs.String("metrics-url", defaultMetricsURL(defaultTarget), "EDM metrics URL")
	listen := fs.String("listen", envOr("LOADGEN_LISTEN", ":8090"), "HTTP listen address for the JaWS UI")
	qps := fs.Int("qps", 100, "starting queries per second")
	workers := fs.Int("workers", 0, "number of parallel builder goroutines (0 = GOMAXPROCS)")
	pprofAddr := fs.String("pprof", "", "if set, serve net/http/pprof at this address (e.g. :6061)")
	domains := fs.String("well-known-source", "", "CSV/text top-domains list")
	configPath := fs.String("config", "", "JSON config file (overlaid before flags)")
	edmPath := fs.String("edm", "", "optional EDM binary or source checkout to launch alongside the load generator")
	reportInterval := fs.Duration("report-interval", 2*time.Second, "verifier scrape cadence")
	uiTick := fs.Duration("ui-tick", 500*time.Millisecond, "UI broadcast cadence (live counters)")
	startStopped := fs.Bool("start-stopped", false, "do not begin generating until the UI Run button is pressed")
	mqttListen := fs.String("mqtt-listen", "", `enable embedded MQTT broker on this address (e.g. ":8883"); empty = off`)
	mqttKeysDir := fs.String("mqtt-keys-dir", envOr("KEYS_DIR", "./keys"), "directory for the MQTT broker's TLS material (auto-generated on first use)")
	mqttNodeName := fs.String("mqtt-node-name", envOr("NODE_NAME", "edm-loadgen-1"), "EDM node name; topic prefix and client-id stem")
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
	if set["mqtt-listen"] || *configPath == "" {
		st.MQTT.Listen = *mqttListen
	}
	if set["mqtt-keys-dir"] || *configPath == "" {
		st.MQTT.KeysDir = *mqttKeysDir
	}
	if set["mqtt-node-name"] || *configPath == "" {
		st.MQTT.NodeName = *mqttNodeName
	}
	st.MQTT.Listen = defaultMQTTListen(*edmPath, st.MQTT.Listen, set["mqtt-listen"])
	st.SetRunning(!*startStopped)

	domainList, err := pattern.LoadDomains(*domains)
	if err != nil {
		die("domains: %v", err)
	}
	beacon := pattern.NewBeacon(st)
	snk, err := sink.Dial(st.Target, sink.Options{Timeout: 5 * time.Second, RetryInterval: 500 * time.Millisecond})
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

	mqttBundle, err := startMQTT(rootCtx, appLog, st)
	if err != nil {
		die("mqtt: %v", err)
	}
	edmProc, err := startEDMIfRequested(rootCtx, appLog, *edmPath, st, mqttBundle)
	if err != nil {
		die("edm: %v", err)
	}
	if edmProc != nil {
		defer edmProc.Stop()
	}
	startPprof(appLog, *pprofAddr)

	prod := producer.New(st, domainList, beacon, snk, *workers)
	go func() { _ = prod.Run(rootCtx) }()

	scraper := verify.NewScraper(st.MetricsURL)
	go func() { _ = scraper.Run(rootCtx, st, st.ReportInterval) }()

	appLog.Info("edm-loadgen UI listening", "url", "http://"+*listen+"/")
	if err := srv.Run(rootCtx); err != nil && err != context.Canceled {
		die("serve: %v", err)
	}
}

// startPprof launches an HTTP server on addr that serves net/http/pprof. The
// pprof package registers its handlers on http.DefaultServeMux at import
// time; we only need to put a listener in front of it. No-op when addr is
// empty.
func startPprof(log *slog.Logger, addr string) {
	if addr == "" {
		return
	}
	go func() {
		// Best-effort: a port collision here just means no profiling.
		_ = http.ListenAndServe(addr, nil) //nolint:gosec // dev tooling only
	}()
	log.Info("pprof listening", "url", "http://"+addr+"/debug/pprof/")
}

// startMQTT starts the embedded broker if st.MQTT.Listen is set. It ensures
// the keys directory has the cert material, opens the listener, and prints
// the EDM-side flags the user must pass to wire EDM into our broker.
//
// The broker runs on its own goroutine until ctx is cancelled. Returns the
// generated key bundle, or nil when MQTT is disabled.
func startMQTT(ctx context.Context, log *slog.Logger, st *state.State) (*pki.Bundle, error) {
	if st.MQTT.Listen == "" {
		return nil, nil
	}
	bundle, err := pki.Ensure(st.MQTT.KeysDir, []string{"127.0.0.1", "localhost"}, st.MQTT.NodeName)
	if err != nil {
		return nil, fmt.Errorf("pki: %w", err)
	}
	// EDM derives its publish topic from the JWK kid:
	//     events/up/<kid>/new_qname
	// We set the kid to NodeName via pki.Ensure, so the broker matches the
	// same prefix here.
	prefix := "events/up/" + st.MQTT.NodeName + "/"
	br, err := mqtt.New(mqtt.Options{
		Listen:         st.MQTT.Listen,
		CertFile:       bundle.ServerCert,
		KeyFile:        bundle.ServerKey,
		EDMTopicPrefix: prefix,
		Total:          &st.Received.Total,
		EDMTopic:       &st.Received.EDMTopic,
		Connections:    &st.Received.Connections,
	})
	if err != nil {
		return nil, err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- br.Run(ctx) }()

	select {
	case err := <-errCh:
		return nil, err
	case <-time.After(75 * time.Millisecond):
	}

	log.Info("MQTT broker listening",
		"listen", st.MQTT.Listen,
		"server", "tls://"+mqttHost(st.MQTT.Listen),
		"topic_prefix", "events/up/"+st.MQTT.NodeName+"/",
		"ca_file", bundle.CACert,
		"client_cert_file", bundle.ClientCert,
		"client_key_file", bundle.ClientKey,
		"signing_key_file", bundle.JWSKey,
	)
	return &bundle, nil
}

// mqttHost rewrites a bind-all address (":8883") into a connectable form
// ("127.0.0.1:8883") so the EDM-side flag pasted from the printout works
// without further editing.
func mqttHost(listen string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return "127.0.0.1" + listen
	}
	return listen
}

func die(format string, args ...any) {
	appLog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
