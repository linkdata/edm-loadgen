package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/linkdata/edm-loadgen/internal/pki"
	"github.com/linkdata/edm-loadgen/internal/sink"
	"github.com/linkdata/edm-loadgen/internal/state"
)

const (
	defaultEDMConfigDir        = "/tmp/edm-config"
	defaultEDMDataDir          = "/tmp/edm-data"
	defaultEDMLog              = "/tmp/dev-edm.log"
	defaultEDMStabiliseSeconds = 5
	defaultEDMReadySeconds     = 15
	defaultWellKnownURL        = "https://raw.githubusercontent.com/dnstapir/edm/main/rpm/SOURCES/well-known-domains.dawg"
	defaultEDMMQTTListen       = ":8883"
)

type edmMode string

const (
	edmModeRepo   edmMode = "repo"
	edmModeBinary edmMode = "binary"
)

type edmTarget struct {
	Path string
	Mode edmMode
}

type edmCommandSpec struct {
	Name string
	Args []string
	Dir  string
}

type edmOptions struct {
	Path         string
	Target       string
	MetricsURL   string
	ConfigDir    string
	DataDir      string
	LogPath      string
	Workers      string
	Stabilise    time.Duration
	ReadyTimeout time.Duration
	MQTTListen   string
	MQTTBundle   *pki.Bundle
}

type edmProcess struct {
	pid     int
	logPath string
	done    chan error
	stop    sync.Once
}

func defaultMQTTListen(edmPath, current string, explicit bool) string {
	if edmPath != "" && !explicit && current == "" {
		return envOr("MQTT_LISTEN", defaultEDMMQTTListen)
	}
	return current
}

func defaultTarget() string {
	input := os.Getenv("EDM_INPUT")
	if input == "" {
		return "tcp://127.0.0.1:53535"
	}
	if strings.Contains(input, "://") {
		return input
	}
	return "tcp://" + input
}

func defaultMetricsURL(target string) string {
	if v := os.Getenv("EDM_METRICS_URL"); v != "" {
		return v
	}
	input, err := edmInputArgs(target)
	if err != nil || len(input) != 2 || input[0] != "--input-tcp" {
		return "http://127.0.0.1:2112/metrics"
	}
	host := input[1]
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":2112/metrics"
}

func startEDMIfRequested(ctx context.Context, edmPath string, st *state.State, mqttBundle *pki.Bundle) (*edmProcess, error) {
	if edmPath == "" {
		return nil, nil
	}
	opts, err := edmOptionsFromEnv(edmPath, st, mqttBundle)
	if err != nil {
		return nil, err
	}
	return startEDM(ctx, opts)
}

func edmOptionsFromEnv(edmPath string, st *state.State, mqttBundle *pki.Bundle) (edmOptions, error) {
	st.RLock()
	target := st.Target
	metricsURL := st.MetricsURL
	mqttListen := st.MQTT.Listen
	st.RUnlock()

	stabilise, err := envSeconds("EDM_STABILISE_SECONDS", defaultEDMStabiliseSeconds)
	if err != nil {
		return edmOptions{}, err
	}
	ready, err := envSeconds("EDM_READY_TIMEOUT", defaultEDMReadySeconds)
	if err != nil {
		return edmOptions{}, err
	}
	return edmOptions{
		Path:         edmPath,
		Target:       target,
		MetricsURL:   metricsURL,
		ConfigDir:    envOr("EDM_CONFIG", defaultEDMConfigDir),
		DataDir:      envOr("EDM_DATA", defaultEDMDataDir),
		LogPath:      envOr("EDM_LOG", defaultEDMLog),
		Workers:      envOr("EDM_WORKERS", strconv.Itoa(runtime.NumCPU())),
		Stabilise:    stabilise,
		ReadyTimeout: ready,
		MQTTListen:   mqttListen,
		MQTTBundle:   mqttBundle,
	}, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envSeconds(name string, fallback int) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return time.Duration(fallback) * time.Second, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer number of seconds", name)
	}
	return time.Duration(n) * time.Second, nil
}

func classifyEDMPath(path string) (edmTarget, error) {
	if path == "" {
		return edmTarget{}, errors.New("--edm requires a path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return edmTarget{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		cmdDir := filepath.Join(path, "cmd", "dnstapir-edm")
		cmdInfo, err := os.Stat(cmdDir)
		if err != nil || !cmdInfo.IsDir() {
			return edmTarget{}, fmt.Errorf("%s is a directory, but %s is missing; expected an EDM source checkout", path, cmdDir)
		}
		return edmTarget{Path: path, Mode: edmModeRepo}, nil
	}
	if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return edmTarget{Path: path, Mode: edmModeBinary}, nil
	}
	return edmTarget{}, fmt.Errorf("%s is neither an EDM source checkout nor an executable EDM binary", path)
}

func buildEDMCommandSpec(target edmTarget, args []string) (edmCommandSpec, error) {
	switch target.Mode {
	case edmModeRepo:
		cmdArgs := append([]string{"run", "./cmd/dnstapir-edm"}, args...)
		return edmCommandSpec{Name: "go", Args: cmdArgs, Dir: target.Path}, nil
	case edmModeBinary:
		return edmCommandSpec{Name: target.Path, Args: append([]string(nil), args...)}, nil
	default:
		return edmCommandSpec{}, fmt.Errorf("unknown EDM target mode %q", target.Mode)
	}
}

func buildEDMArgs(opts edmOptions) ([]string, error) {
	input, err := edmInputArgs(opts.Target)
	if err != nil {
		return nil, err
	}
	args := []string{
		"run",
		input[0], input[1],
		"--data-dir", opts.DataDir,
		"--config-file", filepath.Join(opts.ConfigDir, "edm.toml"),
		"--well-known-domains-file", filepath.Join(opts.ConfigDir, "well-known-domains.dawg"),
		"--minimiser-workers", opts.Workers,
		"--disable-histogram-sender",
	}
	if opts.MQTTListen != "" && opts.MQTTBundle != nil {
		args = append(args,
			"--disable-mqtt-filequeue",
			"--mqtt-server", "tls://"+mqttHost(opts.MQTTListen),
			"--mqtt-keepalive", "30",
			"--mqtt-ca-file", opts.MQTTBundle.CACert,
			"--mqtt-client-cert-file", opts.MQTTBundle.ClientCert,
			"--mqtt-client-key-file", opts.MQTTBundle.ClientKey,
			"--mqtt-signing-key-file", opts.MQTTBundle.JWSKey,
		)
	} else {
		args = append(args, "--disable-mqtt")
	}
	return args, nil
}

func edmInputArgs(target string) ([]string, error) {
	addr, err := sink.ParseTarget(target)
	if err != nil {
		return nil, err
	}
	switch addr.Network() {
	case "tcp":
		return []string{"--input-tcp", addr.String()}, nil
	case "unix":
		return []string{"--input-unix", addr.String()}, nil
	default:
		return nil, fmt.Errorf("unsupported EDM target network %q", addr.Network())
	}
}

func startEDM(ctx context.Context, opts edmOptions) (*edmProcess, error) {
	target, err := classifyEDMPath(opts.Path)
	if err != nil {
		return nil, err
	}
	if err := bootstrapEDMFiles(opts); err != nil {
		return nil, err
	}
	args, err := buildEDMArgs(opts)
	if err != nil {
		return nil, err
	}
	spec, err := buildEDMCommandSpec(target, args)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open EDM log %s: %w", opts.LogPath, err)
	}

	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	fmt.Fprintf(os.Stderr, "edm-loadgen: starting EDM (logs -> %s)\n", opts.LogPath)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start %s: %w", spec.Name, err)
	}
	proc := &edmProcess{
		pid:     cmd.Process.Pid,
		logPath: opts.LogPath,
		done:    make(chan error, 1),
	}
	go func() {
		proc.done <- cmd.Wait()
		close(proc.done)
		_ = logFile.Close()
	}()
	if err := waitForEDMReady(ctx, proc, opts); err != nil {
		proc.Stop()
		return nil, err
	}
	return proc, nil
}

func bootstrapEDMFiles(opts edmOptions) error {
	if err := os.MkdirAll(opts.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("mkdir EDM config dir: %w", err)
	}
	configPath := filepath.Join(opts.ConfigDir, "edm.toml")
	if !regularFileExists(configPath) {
		src, err := findBundledEDMConfig()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read bundled EDM config %s: %w", src, err)
		}
		if err := os.WriteFile(configPath, body, 0o644); err != nil {
			return fmt.Errorf("write EDM config %s: %w", configPath, err)
		}
		fmt.Fprintf(os.Stderr, "edm-loadgen: copied edm.toml to %s\n", configPath)
	}

	dawgPath := filepath.Join(opts.ConfigDir, "well-known-domains.dawg")
	if !regularFileExists(dawgPath) {
		if err := fetchWellKnownDomains(dawgPath); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return fmt.Errorf("mkdir EDM data dir: %w", err)
	}
	return nil
}

func findBundledEDMConfig() (string, error) {
	candidates := []string{filepath.Join("configs", "edm.toml")}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", "configs", "edm.toml"))
	}
	for _, c := range candidates {
		if regularFileExists(c) {
			return c, nil
		}
	}
	return "", errors.New("missing bundled configs/edm.toml")
}

func fetchWellKnownDomains(path string) error {
	fmt.Fprintf(os.Stderr, "edm-loadgen: fetching well-known-domains.dawg from upstream EDM...\n")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(defaultWellKnownURL)
	if err != nil {
		return fmt.Errorf("fetch well-known-domains.dawg: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch well-known-domains.dawg: HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read well-known-domains.dawg response: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func waitForEDMReady(ctx context.Context, proc *edmProcess, opts edmOptions) error {
	if opts.Stabilise > 0 {
		deadline := time.Now().Add(opts.Stabilise)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for time.Now().Before(deadline) {
			select {
			case err := <-proc.done:
				return edmExitedError("stabilising", err, proc.logPath)
			case <-ctx.Done():
				return ctx.Err()
			case <-tick.C:
			}
		}
	}

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(opts.ReadyTimeout)
	for {
		if err, ok := processDone(proc); ok {
			return edmExitedError("waiting for /metrics", err, proc.logPath)
		}
		if metricsReady(ctx, client, opts.MetricsURL) {
			fmt.Fprintf(os.Stderr, "edm-loadgen: EDM ready at %s\n", opts.MetricsURL)
			return nil
		}
		if opts.ReadyTimeout == 0 || time.Now().After(deadline) {
			return fmt.Errorf("EDM did not respond on %s within %s\nEDM log tail:\n%s", opts.MetricsURL, opts.ReadyTimeout, tailLog(proc.logPath, 20))
		}
		select {
		case err := <-proc.done:
			return edmExitedError("waiting for /metrics", err, proc.logPath)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func processDone(proc *edmProcess) (error, bool) {
	select {
	case err := <-proc.done:
		return err, true
	default:
		return nil, false
	}
}

func metricsReady(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func edmExitedError(phase string, err error, logPath string) error {
	if err == nil {
		err = errors.New("process exited")
	}
	return fmt.Errorf("EDM exited while %s: %w\nEDM log tail:\n%s", phase, err, tailLog(logPath, 20))
}

func (p *edmProcess) Stop() {
	if p == nil {
		return
	}
	p.stop.Do(func() {
		fmt.Fprintf(os.Stderr, "edm-loadgen: stopping EDM (pgid %d)\n", p.pid)
		_ = syscall.Kill(-p.pid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			fmt.Fprintf(os.Stderr, "edm-loadgen: EDM did not exit within 10s, sending SIGKILL\n")
			_ = syscall.Kill(-p.pid, syscall.SIGKILL)
			<-p.done
		}
		drainProcessGroup(p.pid, 10*time.Second)
	})
}

func drainProcessGroup(pgid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for processGroupExists(pgid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	deadline = time.Now().Add(timeout)
	for processGroupExists(pgid) {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func processGroupExists(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func tailLog(path string, maxLines int) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(unable to read %s: %v)", path, err)
	}
	body = bytes.TrimRight(body, "\n")
	if len(body) == 0 {
		return "(empty)"
	}
	lines := bytes.Split(body, []byte("\n"))
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.TrimRight(string(bytes.Join(lines, []byte("\n"))), "\n")
}
