package config

import (
	"net"
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	yaml := `
global:
  probe_every: 30s
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
    probe_every: 10s
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
		t.Errorf("probe probe_every: want 10s, got %v", p.Interval)
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
  probe_every: 30s
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
  probe_every: 30s
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
  probe_every: 30s
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
  probe_every: 30s
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
  probe_every: 30s
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
  probe_every: 30s
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
		t.Errorf("default probe_every: want 30s, got %v", cfg.Global.Interval)
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

func TestOTLPDefaults(t *testing.T) {
	y := `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
probes: []
`
	cfg, err := parse([]byte(y))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	o := cfg.Exporters.OTLP
	if o.Protocol != "grpc" {
		t.Errorf("default protocol: want grpc, got %q", o.Protocol)
	}
	if o.Batch.ExportInterval != 30*time.Second {
		t.Errorf("default export_interval: want 30s, got %v", o.Batch.ExportInterval)
	}
	if o.Batch.ExportTimeout != 10*time.Second {
		t.Errorf("default export_timeout: want 10s, got %v", o.Batch.ExportTimeout)
	}
	if !o.Retry.Enabled {
		t.Error("default retry.enabled: want true")
	}
	if o.Retry.MaxElapsedTime != 300*time.Second {
		t.Errorf("default max_elapsed_time: want 300s, got %v", o.Retry.MaxElapsedTime)
	}
}

func TestNonLocalSourceIPRejected(t *testing.T) {
	// Stub localInterfaceAddrs to return only loopback so we can use a
	// deterministic "not-local" IP address without depending on real interfaces.
	orig := localInterfaceAddrs
	localInterfaceAddrs = func() ([]net.Addr, error) {
		_, ipnet, _ := net.ParseCIDR("127.0.0.1/8")
		// Preserve host bits as InterfaceAddrs does.
		ipnet.IP = net.ParseIP("127.0.0.1").To4()
		return []net.Addr{ipnet}, nil
	}
	defer func() { localInterfaceAddrs = orig }()

	yaml := `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
probes:
  - name: nonlocal
    type: icmp
    target: "127.0.0.1"
    source_ip: "10.0.0.1"
`
	_, err := parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for non-local source_ip, got nil")
	}
}

func TestLocalSourceIPAccepted(t *testing.T) {
	// Stub localInterfaceAddrs to return a known set of addresses.
	orig := localInterfaceAddrs
	localInterfaceAddrs = func() ([]net.Addr, error) {
		_, loopback, _ := net.ParseCIDR("127.0.0.1/8")
		loopback.IP = net.ParseIP("127.0.0.1").To4()
		return []net.Addr{loopback}, nil
	}
	defer func() { localInterfaceAddrs = orig }()

	yaml := `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
probes:
  - name: local
    type: icmp
    target: "127.0.0.1"
    source_ip: "127.0.0.1"
`
	_, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error for local source_ip, got: %v", err)
	}
}

func TestOTLPValidation(t *testing.T) {
	base := `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
probes: []
exporters:
  otlp:
`
	cases := []struct {
		name    string
		snippet string
		wantErr bool
	}{
		{
			name: "disabled with no endpoint is valid",
			snippet: `
    enabled: false
`,
			wantErr: false,
		},
		{
			name: "enabled with endpoint and insecure is valid",
			snippet: `
    enabled: true
    endpoint: "localhost:4317"
    insecure: true
`,
			wantErr: false,
		},
		{
			name: "enabled without endpoint errors",
			snippet: `
    enabled: true
    insecure: true
`,
			wantErr: true,
		},
		{
			name: "invalid protocol errors",
			snippet: `
    enabled: true
    endpoint: "localhost:4317"
    protocol: ftp
`,
			wantErr: true,
		},
		{
			name: "http protocol is valid",
			snippet: `
    enabled: true
    endpoint: "localhost:4318"
    protocol: http
    insecure: true
`,
			wantErr: false,
		},
		{
			name: "cert_file without key_file errors",
			snippet: `
    enabled: true
    endpoint: "localhost:4317"
    tls:
      cert_file: "/path/to/cert.pem"
`,
			wantErr: true,
		},
		{
			name: "key_file without cert_file errors",
			snippet: `
    enabled: true
    endpoint: "localhost:4317"
    tls:
      key_file: "/path/to/key.pem"
`,
			wantErr: true,
		},
		{
			name: "both cert and key set is valid",
			snippet: `
    enabled: true
    endpoint: "localhost:4317"
    tls:
      cert_file: "/path/to/cert.pem"
      key_file: "/path/to/key.pem"
`,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(base + tc.snippet))
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
