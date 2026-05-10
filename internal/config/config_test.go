package config

import (
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	yaml := `
global:
  interval: 30s
  timeout: 5s
  count: 3
  source_ip: "127.0.0.1"

exporters:
  stdout:
    enabled: true
    format: json

server:
  listen: ":9090"

probes:
  - name: loopback
    type: icmp
    target: "127.0.0.1"
    interval: 10s
    timeout: 2s
    count: 5
    labels:
      env: test
`
	cfg, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Probes) != 1 {
		t.Fatalf("want 1 probe, got %d", len(cfg.Probes))
	}
	p := cfg.Probes[0]
	if p.Name != "loopback" {
		t.Errorf("probe name: want loopback, got %q", p.Name)
	}
	if p.Interval != 10*time.Second {
		t.Errorf("probe interval: want 10s, got %v", p.Interval)
	}
	if p.SourceIP != "127.0.0.1" {
		t.Errorf("source_ip: want 127.0.0.1, got %q", p.SourceIP)
	}
	if p.Labels["env"] != "test" {
		t.Errorf("label env: want test, got %q", p.Labels["env"])
	}
}

func TestDuplicateProbeName(t *testing.T) {
	yaml := `
global:
  interval: 30s
  timeout: 5s
  count: 3
  source_ip: "127.0.0.1"
probes:
  - name: dup
    type: icmp
    target: "127.0.0.1"
  - name: dup
    type: icmp
    target: "127.0.0.1"
`
	_, err := parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected duplicate name error, got nil")
	}
}

func TestUnknownProbeType(t *testing.T) {
	yaml := `
global:
  interval: 30s
  timeout: 5s
  count: 3
  source_ip: "127.0.0.1"
probes:
  - name: bad
    type: grpc
    target: "127.0.0.1"
`
	_, err := parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected unknown type error, got nil")
	}
}

func TestEmptySourceIPIsValid(t *testing.T) {
	// Empty source_ip is intentional: the OS picks the interface.
	yaml := `
global:
  interval: 30s
  timeout: 5s
  count: 3
probes:
  - name: noip
    type: icmp
    target: "127.0.0.1"
`
	_, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error for empty source_ip, got: %v", err)
	}
}

func TestInvalidSourceIP(t *testing.T) {
	yaml := `
global:
  interval: 30s
  timeout: 5s
  count: 3
probes:
  - name: badip
    type: icmp
    target: "127.0.0.1"
    source_ip: "not-an-ip"
`
	_, err := parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid source_ip, got nil")
	}
}

func TestGlobalSourceIPFallback(t *testing.T) {
	yaml := `
global:
  interval: 30s
  timeout: 5s
  count: 3
  source_ip: "127.0.0.1"
probes:
  - name: inherited
    type: icmp
    target: "127.0.0.1"
`
	cfg, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Probes[0].SourceIP != "127.0.0.1" {
		t.Errorf("expected inherited source_ip, got %q", cfg.Probes[0].SourceIP)
	}
}

func TestInvalidExporterFormat(t *testing.T) {
	yaml := `
global:
  interval: 30s
  timeout: 5s
  count: 3
  source_ip: "127.0.0.1"
exporters:
  stdout:
    format: xml
probes: []
`
	_, err := parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected invalid format error, got nil")
	}
}

func TestGlobalDefaults(t *testing.T) {
	yaml := `
global:
  source_ip: "127.0.0.1"
probes: []
`
	cfg, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.Interval != 30*time.Second {
		t.Errorf("default interval: want 30s, got %v", cfg.Global.Interval)
	}
	if cfg.Global.Count != 3 {
		t.Errorf("default count: want 3, got %d", cfg.Global.Count)
	}
	if cfg.Exporters.Stdout.Format != "json" {
		t.Errorf("default format: want json, got %q", cfg.Exporters.Stdout.Format)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("default listen: want :8080, got %q", cfg.Server.Listen)
	}
}
