package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/linkdata/edm-loadgen/internal/pki"
)

func TestDefaultMQTTListen(t *testing.T) {
	t.Setenv("MQTT_LISTEN", "")
	cases := []struct {
		name     string
		edmPath  string
		current  string
		explicit bool
		want     string
	}{
		{name: "no edm stays disabled", want: ""},
		{name: "edm defaults mqtt", edmPath: "../edm", want: ":8883"},
		{name: "explicit empty disables mqtt", edmPath: "../edm", explicit: true, want: ""},
		{name: "current value wins", edmPath: "../edm", current: "127.0.0.1:18883", want: "127.0.0.1:18883"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := defaultMQTTListen(c.edmPath, c.current, c.explicit)
			if got != c.want {
				t.Fatalf("defaultMQTTListen()=%q, want %q", got, c.want)
			}
		})
	}
}

func TestDefaultMQTTListenUsesEnv(t *testing.T) {
	t.Setenv("MQTT_LISTEN", "127.0.0.1:18883")
	got := defaultMQTTListen("../edm", "", false)
	if got != "127.0.0.1:18883" {
		t.Fatalf("defaultMQTTListen()=%q, want env value", got)
	}
}

func TestDefaultTarget(t *testing.T) {
	t.Setenv("EDM_INPUT", "")
	if got := defaultTarget(); got != "tcp://127.0.0.1:53535" {
		t.Fatalf("defaultTarget()=%q", got)
	}
	t.Setenv("EDM_INPUT", "127.0.0.1:15353")
	if got := defaultTarget(); got != "tcp://127.0.0.1:15353" {
		t.Fatalf("defaultTarget()=%q, want tcp EDM_INPUT", got)
	}
	t.Setenv("EDM_INPUT", "unix:///tmp/edm.sock")
	if got := defaultTarget(); got != "unix:///tmp/edm.sock" {
		t.Fatalf("defaultTarget()=%q, want URL EDM_INPUT", got)
	}
}

func TestDefaultMetricsURL(t *testing.T) {
	t.Setenv("EDM_METRICS_URL", "")
	if got := defaultMetricsURL("tcp://127.0.0.1:53535"); got != "http://127.0.0.1:2112/metrics" {
		t.Fatalf("defaultMetricsURL()=%q", got)
	}
	if got := defaultMetricsURL("tcp://192.0.2.10:53535"); got != "http://192.0.2.10:2112/metrics" {
		t.Fatalf("defaultMetricsURL()=%q, want host-derived URL", got)
	}
	if got := defaultMetricsURL("unix:///tmp/edm.sock"); got != "http://127.0.0.1:2112/metrics" {
		t.Fatalf("defaultMetricsURL()=%q, want fallback URL", got)
	}
	t.Setenv("EDM_METRICS_URL", "http://127.0.0.1:12112/metrics")
	if got := defaultMetricsURL("tcp://192.0.2.10:53535"); got != "http://127.0.0.1:12112/metrics" {
		t.Fatalf("defaultMetricsURL()=%q, want env override", got)
	}
}

func TestClassifyEDMPath(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "dnstapir-edm"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "dnstapir-edm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nonExec := filepath.Join(root, "not-exec")
	if err := os.WriteFile(nonExec, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	notRepo := filepath.Join(root, "not-repo")
	if err := os.Mkdir(notRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		want    edmMode
		wantErr bool
	}{
		{name: "repo", path: repo, want: edmModeRepo},
		{name: "binary", path: bin, want: edmModeBinary},
		{name: "missing", path: filepath.Join(root, "missing"), wantErr: true},
		{name: "non executable", path: nonExec, wantErr: true},
		{name: "invalid repo", path: notRepo, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := classifyEDMPath(c.path)
			if (err != nil) != c.wantErr {
				t.Fatalf("classifyEDMPath() err=%v, wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && got.Mode != c.want {
				t.Fatalf("classifyEDMPath() mode=%q, want %q", got.Mode, c.want)
			}
		})
	}
}

func TestBuildEDMCommandSpec(t *testing.T) {
	args := []string{"run", "--input-tcp", "127.0.0.1:53535"}

	repo, err := buildEDMCommandSpec(edmTarget{Path: "/src/edm", Mode: edmModeRepo}, args)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Name != "go" || repo.Dir != "/src/edm" {
		t.Fatalf("repo spec name=%q dir=%q", repo.Name, repo.Dir)
	}
	if want := []string{"run", "./cmd/dnstapir-edm", "run", "--input-tcp", "127.0.0.1:53535"}; !reflect.DeepEqual(repo.Args, want) {
		t.Fatalf("repo args=%v, want %v", repo.Args, want)
	}

	bin, err := buildEDMCommandSpec(edmTarget{Path: "/bin/dnstapir-edm", Mode: edmModeBinary}, args)
	if err != nil {
		t.Fatal(err)
	}
	if bin.Name != "/bin/dnstapir-edm" || bin.Dir != "" {
		t.Fatalf("binary spec name=%q dir=%q", bin.Name, bin.Dir)
	}
	if !reflect.DeepEqual(bin.Args, args) {
		t.Fatalf("binary args=%v, want %v", bin.Args, args)
	}
}

func TestEDMInputArgs(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		want    []string
		wantErr bool
	}{
		{name: "tcp", target: "tcp://127.0.0.1:53535", want: []string{"--input-tcp", "127.0.0.1:53535"}},
		{name: "unix", target: "unix:///tmp/edm.sock", want: []string{"--input-unix", "/tmp/edm.sock"}},
		{name: "bad scheme", target: "http://127.0.0.1:53535", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := edmInputArgs(c.target)
			if (err != nil) != c.wantErr {
				t.Fatalf("edmInputArgs() err=%v, wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && !reflect.DeepEqual(got, c.want) {
				t.Fatalf("edmInputArgs()=%v, want %v", got, c.want)
			}
		})
	}
}

func TestBuildEDMArgs(t *testing.T) {
	base := edmOptions{
		Target:    "tcp://127.0.0.1:53535",
		ConfigDir: "/tmp/edm-config",
		DataDir:   "/tmp/edm-data",
		Workers:   "4",
	}

	got, err := buildEDMArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run",
		"--input-tcp", "127.0.0.1:53535",
		"--data-dir", "/tmp/edm-data",
		"--config-file", "/tmp/edm-config/edm.toml",
		"--well-known-domains-file", "/tmp/edm-config/well-known-domains.dawg",
		"--minimiser-workers", "4",
		"--disable-histogram-sender",
		"--disable-mqtt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildEDMArgs()=%v, want %v", got, want)
	}

	base.MQTTListen = ":8883"
	base.MQTTBundle = &pki.Bundle{
		CACert:     "/keys/ca.crt",
		ClientCert: "/keys/client.crt",
		ClientKey:  "/keys/client.key",
		JWSKey:     "/keys/jws.key",
	}
	got, err = buildEDMArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	mustContainArgs(t, got,
		"--disable-mqtt-filequeue",
		"--mqtt-server", "tls://127.0.0.1:8883",
		"--mqtt-ca-file", "/keys/ca.crt",
		"--mqtt-client-cert-file", "/keys/client.crt",
		"--mqtt-client-key-file", "/keys/client.key",
		"--mqtt-signing-key-file", "/keys/jws.key",
	)
	for _, arg := range got {
		if arg == "--disable-mqtt" {
			t.Fatalf("MQTT-enabled args unexpectedly contain --disable-mqtt: %v", got)
		}
	}
}

func mustContainArgs(t *testing.T, got []string, want ...string) {
	t.Helper()
	for i := 0; i < len(want); i++ {
		found := false
		for j := 0; j < len(got); j++ {
			if got[j] != want[i] {
				continue
			}
			if i+1 < len(want) && strings.HasPrefix(want[i], "--") && !strings.HasPrefix(want[i+1], "--") {
				if j+1 < len(got) && got[j+1] == want[i+1] {
					found = true
				}
				i++
			} else {
				found = true
			}
			break
		}
		if !found {
			t.Fatalf("args %v do not contain %v", got, want)
		}
	}
}
