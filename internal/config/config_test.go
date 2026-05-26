package config

import (
	"net"
	"os"
	"testing"
	"time"
)

// minValidProbeYAML is the smallest probe config that passes all validators.
const minValidProbeYAML = `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
probes: []
`

// minValidOpsYAML is the smallest ops config that passes all validators.
// Empty bytes are also valid (all defaults apply), but this makes intent explicit.
const minValidOpsYAML = ``

func TestParseValid(t *testing.T) {
	probeYAML := `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
  source_ip: "127.0.0.1"

probes:
  - name: loopback
    type: icmp
    target: "127.0.0.1"
    probe_every: 10s
    timeout: 6s
    count: 5
    labels:
      env: test
`
	opsYAML := `
exporters:
  stdout:
    enabled: true
    format: json

server:
  listen: ":9090"
`
	cfg, err := parse([]byte(opsYAML), []byte(probeYAML))
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
	probeYAML := `
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
	_, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
	if err == nil {
		t.Fatal("expected duplicate name error, got nil")
	}
}

func TestUnknownProbeType(t *testing.T) {
	probeYAML := `
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
	_, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
	if err == nil {
		t.Fatal("expected unknown type error, got nil")
	}
}

func TestEmptySourceIPIsValid(t *testing.T) {
	probeYAML := `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
probes:
  - name: noip
    type: icmp
    target: "127.0.0.1"
`
	_, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
	if err != nil {
		t.Fatalf("expected no error for empty source_ip, got: %v", err)
	}
}

func TestInvalidSourceIP(t *testing.T) {
	probeYAML := `
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
	_, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
	if err == nil {
		t.Fatal("expected error for invalid source_ip, got nil")
	}
}

func TestGlobalSourceIPFallback(t *testing.T) {
	probeYAML := `
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
	cfg, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Probes[0].SourceIP != "127.0.0.1" {
		t.Errorf("expected inherited source_ip, got %q", cfg.Probes[0].SourceIP)
	}
}

func TestInvalidExporterFormat(t *testing.T) {
	opsYAML := `
exporters:
  stdout:
    format: xml
`
	_, err := parse([]byte(opsYAML), []byte(minValidProbeYAML))
	if err == nil {
		t.Fatal("expected invalid format error, got nil")
	}
}

func TestGlobalDefaults(t *testing.T) {
	probeYAML := `
global:
  source_ip: "127.0.0.1"
probes: []
`
	cfg, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
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
		t.Errorf("default stdout format: want json, got %q", cfg.Exporters.Stdout.Format)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Errorf("default listen: want 127.0.0.1:8080, got %q", cfg.Server.Listen)
	}
}

func TestOTLPDefaults(t *testing.T) {
	cfg, err := parse([]byte(minValidOpsYAML), []byte(minValidProbeYAML))
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
	orig := localInterfaceAddrs
	localInterfaceAddrs = func() ([]net.Addr, error) {
		_, ipnet, _ := net.ParseCIDR("127.0.0.1/8")
		ipnet.IP = net.ParseIP("127.0.0.1").To4()
		return []net.Addr{ipnet}, nil
	}
	defer func() { localInterfaceAddrs = orig }()

	probeYAML := `
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
	_, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
	if err == nil {
		t.Fatal("expected error for non-local source_ip, got nil")
	}
}

func TestLocalSourceIPAccepted(t *testing.T) {
	orig := localInterfaceAddrs
	localInterfaceAddrs = func() ([]net.Addr, error) {
		_, loopback, _ := net.ParseCIDR("127.0.0.1/8")
		loopback.IP = net.ParseIP("127.0.0.1").To4()
		return []net.Addr{loopback}, nil
	}
	defer func() { localInterfaceAddrs = orig }()

	probeYAML := `
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
	_, err := parse([]byte(minValidOpsYAML), []byte(probeYAML))
	if err != nil {
		t.Fatalf("expected no error for local source_ip, got: %v", err)
	}
}

func TestProbeTimingBounds(t *testing.T) {
	cases := []struct {
		name      string
		probeYAML string
		wantErr   bool
	}{
		{
			name: "packet_interval below 1ms is rejected",
			probeYAML: `
  - name: tooFast
    type: icmp
    target: "127.0.0.1"
    packet_interval: 500us
`,
			wantErr: true,
		},
		{
			name: "packet_interval at exactly 1ms is accepted",
			probeYAML: `
  - name: minOK
    type: icmp
    target: "127.0.0.1"
    packet_interval: 1ms
`,
			wantErr: false,
		},
		{
			name: "packet_interval * count equal to timeout is rejected",
			probeYAML: `
  - name: piCountEqTimeout
    type: icmp
    target: "127.0.0.1"
    packet_interval: 1s
    count: 5
    timeout: 5s
    probe_every: 30s
`,
			wantErr: true,
		},
		{
			name: "packet_interval * count greater than timeout is rejected",
			probeYAML: `
  - name: piCountGtTimeout
    type: icmp
    target: "127.0.0.1"
    packet_interval: 1s
    count: 5
    timeout: 4s
    probe_every: 30s
`,
			wantErr: true,
		},
		{
			name: "packet_interval * count less than timeout is accepted",
			probeYAML: `
  - name: piCountLtTimeout
    type: icmp
    target: "127.0.0.1"
    packet_interval: 1s
    count: 3
    timeout: 5s
    probe_every: 30s
`,
			wantErr: false,
		},
		{
			name: "timeout equal to probe_every is rejected",
			probeYAML: `
  - name: timeoutEqInterval
    type: icmp
    target: "127.0.0.1"
    packet_interval: 100ms
    count: 3
    timeout: 30s
    probe_every: 30s
`,
			wantErr: true,
		},
		{
			name: "timeout greater than probe_every is rejected",
			probeYAML: `
  - name: timeoutGtInterval
    type: icmp
    target: "127.0.0.1"
    packet_interval: 100ms
    count: 3
    timeout: 35s
    probe_every: 30s
`,
			wantErr: true,
		},
		{
			name: "timeout less than probe_every is accepted",
			probeYAML: `
  - name: timeoutLtInterval
    type: icmp
    target: "127.0.0.1"
    packet_interval: 100ms
    count: 3
    timeout: 5s
    probe_every: 30s
`,
			wantErr: false,
		},
	}

	baseProbe := `
global:
  probe_every: 1500s
  packet_interval: 1s
  timeout: 1200s
  count: 3
probes:`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(minValidOpsYAML), []byte(baseProbe+tc.probeYAML))
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGlobalTimingBounds(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "global packet_interval below 1ms is rejected",
			yaml: `
global:
  probe_every: 30s
  packet_interval: 500us
  timeout: 5s
  count: 3
probes: []
`,
			wantErr: true,
		},
		{
			name: "global packet_interval * count equal to timeout is rejected",
			yaml: `
global:
  probe_every: 30s
  packet_interval: 2s
  timeout: 6s
  count: 3
probes: []
`,
			wantErr: true,
		},
		{
			name: "global packet_interval * count greater than timeout is rejected",
			yaml: `
global:
  probe_every: 30s
  packet_interval: 2s
  timeout: 5s
  count: 3
probes: []
`,
			wantErr: true,
		},
		{
			name: "global timeout equal to probe_every is rejected",
			yaml: `
global:
  probe_every: 30s
  packet_interval: 1s
  timeout: 30s
  count: 3
probes: []
`,
			wantErr: true,
		},
		{
			name: "global timeout greater than probe_every is rejected",
			yaml: `
global:
  probe_every: 30s
  packet_interval: 1s
  timeout: 35s
  count: 3
probes: []
`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(minValidOpsYAML), []byte(tc.yaml))
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLabelValidation(t *testing.T) {
	base := `
global:
  probe_every: 30s
  timeout: 5s
  count: 3
probes:
  - name: lbl
    type: icmp
    target: "127.0.0.1"
    labels:
`
	cases := []struct {
		name    string
		snippet string
		wantErr bool
	}{
		{
			name:    "valid key and value",
			snippet: `      env: production`,
			wantErr: false,
		},
		{
			name:    "valid key with dots and dashes",
			snippet: `      service.name-v2: api`,
			wantErr: false,
		},
		{
			name:    "valid key starting with underscore",
			snippet: `      _internal: true`,
			wantErr: false,
		},
		{
			name:    "key starting with digit is rejected",
			snippet: `      "1bad": val`,
			wantErr: true,
		},
		{
			name:    "key with space is rejected",
			snippet: `      "bad key": val`,
			wantErr: true,
		},
		{
			name:    "key with newline is rejected",
			snippet: "      \"bad\\nkey\": val",
			wantErr: true,
		},
		{
			name:    "value with newline is rejected",
			snippet: "      env: \"pro\\nduction\"",
			wantErr: true,
		},
		{
			name:    "value with tab is rejected",
			snippet: "      env: \"pro\\tduction\"",
			wantErr: true,
		},
		{
			name:    "value with carriage return is rejected",
			snippet: "      env: \"pro\\rduction\"",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(minValidOpsYAML), []byte(base+tc.snippet+"\n"))
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOTLPValidation(t *testing.T) {
	opsBase := `
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
			_, err := parse([]byte(opsBase+tc.snippet), []byte(minValidProbeYAML))
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadTwoFiles(t *testing.T) {
	opsContent := `
exporters:
  stdout:
    enabled: true
    format: text
server:
  listen: ":9191"
`
	probeContent := `
global:
  probe_every: 15s
  timeout: 3s
  count: 2
probes:
  - name: lo
    type: icmp
    target: "127.0.0.1"
`
	opsFile := writeTempFile(t, opsContent)
	probeFile := writeTempFile(t, probeContent)

	cfg, err := Load(opsFile, probeFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Listen != ":9191" {
		t.Errorf("server.listen: want :9191, got %q", cfg.Server.Listen)
	}
	if cfg.Exporters.Stdout.Format != "text" {
		t.Errorf("stdout.format: want text, got %q", cfg.Exporters.Stdout.Format)
	}
	if cfg.Global.Interval != 15*time.Second {
		t.Errorf("probe_every: want 15s, got %v", cfg.Global.Interval)
	}
	if len(cfg.Probes) != 1 || cfg.Probes[0].Name != "lo" {
		t.Errorf("probes: want [{lo}], got %v", cfg.Probes)
	}
	if cfg.SHA256 == "" {
		t.Error("SHA256 should not be empty")
	}
}

func TestLoadMissingOpsFile(t *testing.T) {
	probeFile := writeTempFile(t, minValidProbeYAML)
	_, err := Load("/nonexistent/ops.yaml", probeFile)
	if err == nil {
		t.Fatal("expected error for missing ops file, got nil")
	}
}

func TestLoadMissingProbeFile(t *testing.T) {
	opsFile := writeTempFile(t, minValidOpsYAML)
	_, err := Load(opsFile, "/nonexistent/probes.yaml")
	if err == nil {
		t.Fatal("expected error for missing probe file, got nil")
	}
}

func TestLoadInvalidOpsFile(t *testing.T) {
	opsContent := `
exporters:
  otlp:
    enabled: true
    # endpoint intentionally omitted — should fail validation
`
	opsFile := writeTempFile(t, opsContent)
	probeFile := writeTempFile(t, minValidProbeYAML)
	_, err := Load(opsFile, probeFile)
	if err == nil {
		t.Fatal("expected error for invalid OTLP config, got nil")
	}
}

func TestLoadInvalidProbeFile(t *testing.T) {
	probeContent := `
global:
  probe_every: 5s
  timeout: 10s
  count: 3
probes: []
`
	opsFile := writeTempFile(t, minValidOpsYAML)
	probeFile := writeTempFile(t, probeContent)
	_, err := Load(opsFile, probeFile)
	if err == nil {
		t.Fatal("expected error for invalid probe timings (timeout >= probe_every), got nil")
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	_ = f.Close()
	return f.Name()
}
