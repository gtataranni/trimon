package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gtataranni/trimon/pkg/types"
)

var labelKeyRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.\-]*$`)

// GlobalConfig holds daemon-wide probe defaults.
type GlobalConfig struct {
	Interval       time.Duration `yaml:"probe_every"`     // scheduler cadence: how often to run each probe
	PacketInterval time.Duration `yaml:"packet_interval"` // wait between individual ICMP echo sends (pro-bing Interval)
	Timeout        time.Duration `yaml:"timeout"`
	Count          int           `yaml:"count"`
	SourceIP       string        `yaml:"source_ip"`
}

// StdoutExporterConfig holds stdout exporter settings.
type StdoutExporterConfig struct {
	Enabled bool   `yaml:"enabled"`
	Format  string `yaml:"format"` // json | text
}

// OTLPTLSConfig holds TLS material paths for the OTLP exporter.
type OTLPTLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

// OTLPBatchConfig controls the OTel SDK periodic reader timing.
type OTLPBatchConfig struct {
	ExportInterval time.Duration `yaml:"export_interval"` // default 30s
	ExportTimeout  time.Duration `yaml:"export_timeout"`  // default 10s
}

// OTLPRetryConfig controls retries on transient export failures.
type OTLPRetryConfig struct {
	Enabled        bool          `yaml:"enabled"`
	MaxElapsedTime time.Duration `yaml:"max_elapsed_time"` // default 300s
}

// OTLPExporterConfig holds OTLP exporter settings.
type OTLPExporterConfig struct {
	Enabled         bool            `yaml:"enabled"`
	Endpoint        string          `yaml:"endpoint"`
	Protocol        string          `yaml:"protocol"` // grpc | http
	Insecure        bool            `yaml:"insecure"`
	TLS             OTLPTLSConfig   `yaml:"tls"`
	Batch           OTLPBatchConfig `yaml:"batch"`
	Retry           OTLPRetryConfig `yaml:"retry"`
	ShutdownTimeout time.Duration   `yaml:"shutdown_timeout"` // default 10s
}

// ExportersConfig groups all exporter configurations.
type ExportersConfig struct {
	Stdout StdoutExporterConfig `yaml:"stdout"`
	OTLP   OTLPExporterConfig   `yaml:"otlp"`
}

// ServerConfig holds the HTTP server bind address.
type ServerConfig struct {
	Listen string `yaml:"listen"`
}

// PipelineConfig holds the result-channel pipeline settings.
type PipelineConfig struct {
	BufferSize int `yaml:"buffer_size"` // default 1000
}

// rawHTTPConfig is the YAML shape for HTTP/HTTPS probe parameters.
// FollowRedirects is *bool to distinguish false from omitted (default true).
type rawHTTPConfig struct {
	Scheme               string `yaml:"scheme"`
	Port                 int    `yaml:"port"`
	Path                 string `yaml:"path"`
	Method               string `yaml:"method"`
	ExpectedStatus       int    `yaml:"expected_status"`
	FollowRedirects      *bool  `yaml:"follow_redirects"`
	TLSExpiryWarningDays int    `yaml:"tls_expiry_warning_days"`
}

// rawTCPConfig is the YAML shape for TCP probe parameters.
type rawTCPConfig struct {
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"`
}

// rawUDPConfig is the YAML shape for UDP probe parameters.
type rawUDPConfig struct {
	Port             int    `yaml:"port"`
	Payload          string `yaml:"payload"`
	ExpectedResponse string `yaml:"expected_response"`
}

// rawDNSConfig is the YAML shape for DNS probe parameters.
type rawDNSConfig struct {
	RecordType     string   `yaml:"record_type"`
	Resolver       string   `yaml:"resolver"`
	ExpectedAnswer []string `yaml:"expected_answer"`
}

// rawProbeConfig mirrors the YAML shape before merging globals.
type rawProbeConfig struct {
	Name           string            `yaml:"name"`
	Type           string            `yaml:"type"`
	Targets        []string          `yaml:"targets"`
	MaxResolvedIPs int               `yaml:"max_resolved_ips"`
	SourceIP       string            `yaml:"source_ip"`
	Interval       *Duration         `yaml:"probe_every"`
	PacketInterval *Duration         `yaml:"packet_interval"`
	Timeout        *Duration         `yaml:"timeout"`
	Count          *int              `yaml:"count"`
	Labels         map[string]string `yaml:"labels"`
	HTTP           *rawHTTPConfig    `yaml:"http"`
	TCP            *rawTCPConfig     `yaml:"tcp"`
	UDP            *rawUDPConfig     `yaml:"udp"`
	DNS            *rawDNSConfig     `yaml:"dns"`
}

// Duration is a yaml-decodable wrapper around time.Duration.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	d.Duration = dur
	return nil
}

// rawProbeFile is the YAML shape of the probe config file (--probes flag).
type rawProbeFile struct {
	Global GlobalConfig     `yaml:"global"`
	Probes []rawProbeConfig `yaml:"probes"`
}

// rawOpsFile is the YAML shape of the ops config file (--config flag).
type rawOpsFile struct {
	Exporters ExportersConfig `yaml:"exporters"`
	Server    ServerConfig    `yaml:"server"`
	Pipeline  PipelineConfig  `yaml:"pipeline"`
}

// Config is the validated, merged configuration.
type Config struct {
	Global    GlobalConfig
	Exporters ExportersConfig
	Server    ServerConfig
	Pipeline  PipelineConfig
	Probes    []types.ProbeConfig
	// SHA256 is the hex-encoded SHA-256 of the raw probe config file bytes.
	// It tracks the probe file only since that is the only file hot-reloaded.
	SHA256 string
}

var knownProbeTypes = map[string]bool{
	types.ProbeTypeICMP: true, types.ProbeTypeHTTP: true, types.ProbeTypeTCP: true,
	types.ProbeTypeUDP: true, types.ProbeTypeDNS: true,
}

// localInterfaceAddrs returns the list of unicast addresses assigned to local interfaces.
// It is a package-level variable so tests can replace it with a stub.
var localInterfaceAddrs = func() ([]net.Addr, error) {
	return net.InterfaceAddrs()
}

// isLocalIP reports whether ip is assigned to a local interface.
func isLocalIP(ip string) (bool, error) {
	addrs, err := localInterfaceAddrs()
	if err != nil {
		return false, fmt.Errorf("listing local interfaces: %w", err)
	}
	for _, addr := range addrs {
		var ipNet *net.IPNet
		switch v := addr.(type) {
		case *net.IPNet:
			ipNet = v
		case *net.IPAddr:
			ipNet = &net.IPNet{IP: v.IP, Mask: net.CIDRMask(32, 32)}
		default:
			continue
		}
		if ipNet.IP.Equal(net.ParseIP(ip)) {
			return true, nil
		}
	}
	return false, nil
}

// Load reads and validates the ops config at opsPath and the probe config at probePath.
//
// Single-read semantics per file: each file is read exactly once into a byte buffer, then
// parsed and validated without any further disk access. Do not introduce additional reads
// of either path anywhere in this call chain.
func Load(opsPath, probePath string) (*Config, error) {
	opsData, err := os.ReadFile(opsPath)
	if err != nil {
		return nil, fmt.Errorf("reading ops config: %w", err)
	}
	probeData, err := os.ReadFile(probePath)
	if err != nil {
		return nil, fmt.Errorf("reading probe config: %w", err)
	}
	return parse(opsData, probeData)
}

func parse(opsData, probeData []byte) (*Config, error) {
	ops, err := parseOpsFile(opsData)
	if err != nil {
		return nil, err
	}

	global, probes, err := parseProbeFile(probeData)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(probeData)

	return &Config{
		Global:    global,
		Exporters: ops.Exporters,
		Server:    ops.Server,
		Pipeline:  ops.Pipeline,
		Probes:    probes,
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

func parseOpsFile(data []byte) (*rawOpsFile, error) {
	var raw rawOpsFile

	raw.Server.Listen = "127.0.0.1:8080"
	raw.Exporters.Stdout.Format = "json"
	raw.Exporters.OTLP.Protocol = "grpc"
	raw.Exporters.OTLP.Batch.ExportInterval = 30 * time.Second
	raw.Exporters.OTLP.Batch.ExportTimeout = 10 * time.Second
	raw.Exporters.OTLP.Retry.Enabled = true
	raw.Exporters.OTLP.Retry.MaxElapsedTime = 300 * time.Second
	raw.Exporters.OTLP.ShutdownTimeout = 10 * time.Second
	raw.Pipeline.BufferSize = 1000

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing ops config YAML: %w", err)
	}

	if raw.Exporters.Stdout.Format != "json" && raw.Exporters.Stdout.Format != "text" {
		return nil, fmt.Errorf("exporters.stdout.format must be \"json\" or \"text\", got %q", raw.Exporters.Stdout.Format)
	}
	if err := validateOTLP(raw.Exporters.OTLP); err != nil {
		return nil, err
	}
	if raw.Pipeline.BufferSize <= 0 {
		return nil, errors.New("pipeline.buffer_size must be positive")
	}

	return &raw, nil
}

func parseProbeFile(data []byte) (GlobalConfig, []types.ProbeConfig, error) {
	var raw rawProbeFile

	raw.Global.Interval = 30 * time.Second
	raw.Global.PacketInterval = 1 * time.Second
	raw.Global.Timeout = 5 * time.Second
	raw.Global.Count = 3

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return GlobalConfig{}, nil, fmt.Errorf("parsing probe config YAML: %w", err)
	}

	if err := validateGlobal(raw.Global); err != nil {
		return GlobalConfig{}, nil, err
	}

	probes, err := mergeAndValidateProbes(raw.Probes, raw.Global)
	if err != nil {
		return GlobalConfig{}, nil, err
	}

	return raw.Global, probes, nil
}

// validateTimings checks the four timing/count fields shared between the global
// defaults and each merged probe configuration. It enforces:
//   - interval, timeout, count > 0
//   - packetInterval >= 1ms
//   - packet_interval * count < timeout  (all packets must fit within the probe budget)
//   - timeout < interval                 (probe must complete before the next run is due)
//   - packet_interval * count < interval is enforced implicitly as consequence
//
// The caller is expected to wrap returned errors with a "global." or
// per-probe context prefix.
func validateTimings(interval, packetInterval, timeout time.Duration, count int) error {
	if interval <= 0 {
		return errors.New("probe_every must be positive")
	}
	if packetInterval < time.Millisecond {
		return errors.New("packet_interval must be >= 1ms")
	}
	if timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if count <= 0 {
		return errors.New("count must be positive")
	}
	if minDuration := packetInterval * time.Duration(count); minDuration >= timeout {
		return fmt.Errorf("packet_interval * count (%v) must be less than timeout (%v)", minDuration, timeout)
	}
	if timeout >= interval {
		return fmt.Errorf("timeout (%v) must be less than probe_every (%v)", timeout, interval)
	}
	return nil
}

func validateGlobal(g GlobalConfig) error {
	if err := validateTimings(g.Interval, g.PacketInterval, g.Timeout, g.Count); err != nil {
		return fmt.Errorf("global.%w", err)
	}
	return nil
}

func mergeAndValidateProbes(raws []rawProbeConfig, global GlobalConfig) ([]types.ProbeConfig, error) {
	seen := make(map[string]bool, len(raws))
	out := make([]types.ProbeConfig, 0, len(raws))

	for i, r := range raws {
		if r.Name == "" {
			return nil, fmt.Errorf("probe[%d]: name is required", i)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("probe name %q is not unique", r.Name)
		}
		seen[r.Name] = true

		if r.Type == "" {
			return nil, fmt.Errorf("probe %q: type is required", r.Name)
		}
		if !knownProbeTypes[r.Type] {
			return nil, fmt.Errorf("probe %q: unknown type %q (supported: %s)", r.Name, r.Type, strings.Join(slices.Sorted(maps.Keys(knownProbeTypes)), ", "))
		}

		if len(r.Targets) == 0 {
			return nil, fmt.Errorf("probe %q: targets is required and must have at least one entry", r.Name)
		}
		for _, t := range r.Targets {
			// DNS targets are query names, not hosts to connect to, so they must
			// not be resolved at load time (NXDOMAIN targets are valid).
			validate := validateOneTarget
			if r.Type == types.ProbeTypeDNS {
				validate = validateDNSQueryName
			}
			if err := validate(t); err != nil {
				return nil, fmt.Errorf("probe %q: %w", r.Name, err)
			}
		}
		if r.MaxResolvedIPs < 0 {
			return nil, fmt.Errorf("probe %q: max_resolved_ips must be >= 0", r.Name)
		}

		// source_ip: per-probe takes precedence, then global default; empty = OS picks.
		sourceIP := r.SourceIP
		if sourceIP == "" {
			sourceIP = global.SourceIP
		}
		if sourceIP != "" && net.ParseIP(sourceIP) == nil {
			return nil, fmt.Errorf("probe %q: source_ip %q is not a valid IP address", r.Name, sourceIP)
		}
		if sourceIP != "" {
			local, err := isLocalIP(sourceIP)
			if err != nil {
				return nil, fmt.Errorf("probe %q: %w", r.Name, err)
			}
			if !local {
				return nil, fmt.Errorf("probe %q: source_ip %q is not assigned to any local interface", r.Name, sourceIP)
			}
		}

		interval := global.Interval
		if r.Interval != nil {
			interval = r.Interval.Duration
		}
		packetInterval := global.PacketInterval
		if r.PacketInterval != nil {
			packetInterval = r.PacketInterval.Duration
		}
		timeout := global.Timeout
		if r.Timeout != nil {
			timeout = r.Timeout.Duration
		}
		count := global.Count
		if r.Count != nil {
			count = *r.Count
		}

		if r.Type != types.ProbeTypeHTTP {
			// HTTP does not support count > 1. For the rest we validate timings
			if err := validateTimings(interval, packetInterval, timeout, count); err != nil {
				return nil, fmt.Errorf("probe %q: %w", r.Name, err)
			}
		}

		var httpCfg *types.HTTPConfig
		if r.Type == types.ProbeTypeHTTP {
			if r.HTTP == nil {
				return nil, fmt.Errorf("probe %q: http config block is required for type \"http\"", r.Name)
			}
			var err error
			httpCfg, err = validateHTTPConfig(r.HTTP)
			if err != nil {
				return nil, fmt.Errorf("probe %q: %w", r.Name, err)
			}
		}

		var tcpCfg *types.TCPConfig
		if r.Type == types.ProbeTypeTCP {
			if r.TCP == nil {
				return nil, fmt.Errorf("probe %q: tcp config block is required for type \"tcp\"", r.Name)
			}
			var err error
			tcpCfg, err = validateTCPConfig(r.TCP)
			if err != nil {
				return nil, fmt.Errorf("probe %q: %w", r.Name, err)
			}
		}

		var udpCfg *types.UDPConfig
		if r.Type == types.ProbeTypeUDP {
			if r.UDP == nil {
				return nil, fmt.Errorf("probe %q: udp config block is required for type \"udp\"", r.Name)
			}
			var err error
			udpCfg, err = validateUDPConfig(r.UDP)
			if err != nil {
				return nil, fmt.Errorf("probe %q: %w", r.Name, err)
			}
		}

		var dnsCfg *types.DNSConfig
		if r.Type == types.ProbeTypeDNS {
			if r.DNS == nil {
				return nil, fmt.Errorf("probe %q: dns config block is required for type \"dns\"", r.Name)
			}
			var err error
			dnsCfg, err = validateDNSConfig(r.DNS)
			if err != nil {
				return nil, fmt.Errorf("probe %q: %w", r.Name, err)
			}
		}

		labels := r.Labels
		for k, v := range labels {
			if !labelKeyRE.MatchString(k) {
				return nil, fmt.Errorf("probe %q: label key %q is not a valid OTel attribute name", r.Name, k)
			}
			if strings.ContainsAny(v, "\n\r\t") {
				return nil, fmt.Errorf("probe %q: label %q value contains control characters", r.Name, k)
			}
		}

		out = append(out, types.ProbeConfig{
			Name:           r.Name,
			Type:           r.Type,
			Targets:        r.Targets,
			MaxResolvedIPs: r.MaxResolvedIPs,
			SourceIP:       sourceIP,
			Interval:       interval,
			PacketInterval: packetInterval,
			Timeout:        timeout,
			Count:          count,
			Labels:         labels,
			HTTP:           httpCfg,
			TCP:            tcpCfg,
			UDP:            udpCfg,
			DNS:            dnsCfg,
		})
	}
	return out, nil
}

func validateOTLP(o OTLPExporterConfig) error {
	if !o.Enabled {
		return nil
	}
	if o.Endpoint == "" {
		return errors.New("exporters.otlp.endpoint is required when otlp is enabled")
	}
	if o.Protocol != "grpc" && o.Protocol != "http" {
		return fmt.Errorf("exporters.otlp.protocol must be \"grpc\" or \"http\", got %q", o.Protocol)
	}
	if (o.TLS.CertFile == "") != (o.TLS.KeyFile == "") {
		return errors.New("exporters.otlp.tls: cert_file and key_file must both be set or both be empty")
	}
	if o.ShutdownTimeout <= 0 {
		return errors.New("exporters.otlp.shutdown_timeout must be positive")
	}
	return nil
}

// validateHTTPConfig validates and applies defaults to a rawHTTPConfig, returning the
// typed config on success.
func validateHTTPConfig(r *rawHTTPConfig) (*types.HTTPConfig, error) {
	cfg := &types.HTTPConfig{
		Scheme:          "http",
		Path:            "/",
		Method:          "GET",
		FollowRedirects: true,
	}

	if r.Scheme != "" {
		s := strings.ToLower(r.Scheme)
		if s != "http" && s != "https" {
			return nil, fmt.Errorf("http.scheme must be \"http\" or \"https\", got %q", r.Scheme)
		}
		cfg.Scheme = s
	}
	if r.Port != 0 {
		if r.Port < 1 || r.Port > 65535 {
			return nil, fmt.Errorf("http.port must be in [1, 65535], got %d", r.Port)
		}
		cfg.Port = r.Port
	}
	if r.Path != "" {
		cfg.Path = r.Path
	}
	if r.Method != "" {
		m := strings.ToUpper(r.Method)
		if m != "GET" && m != "HEAD" && m != "POST" {
			return nil, fmt.Errorf("http.method must be GET, HEAD, or POST, got %q", r.Method)
		}
		cfg.Method = m
	}
	if r.ExpectedStatus != 0 {
		if r.ExpectedStatus < 100 || r.ExpectedStatus > 599 {
			return nil, fmt.Errorf("http.expected_status must be in [100, 599] or 0 (any 2xx), got %d", r.ExpectedStatus)
		}
		cfg.ExpectedStatus = r.ExpectedStatus
	}
	if r.FollowRedirects != nil {
		cfg.FollowRedirects = *r.FollowRedirects
	}
	if r.TLSExpiryWarningDays < 0 {
		return nil, fmt.Errorf("http.tls_expiry_warning_days must be >= 0, got %d", r.TLSExpiryWarningDays)
	}
	cfg.TLSExpiryWarningDays = r.TLSExpiryWarningDays
	return cfg, nil
}

// validateTCPConfig validates a rawTCPConfig, returning the typed config on success.
func validateTCPConfig(r *rawTCPConfig) (*types.TCPConfig, error) {
	if r.Port < 1 || r.Port > 65535 {
		return nil, fmt.Errorf("tcp.port must be in [1, 65535], got %d", r.Port)
	}
	mode := r.Mode
	if mode == "" {
		mode = types.TCPModeConnect
	}
	if mode != types.TCPModeConnect && mode != types.TCPModeSYN {
		return nil, fmt.Errorf("tcp.mode must be %q or %q, got %q", types.TCPModeConnect, types.TCPModeSYN, r.Mode)
	}
	return &types.TCPConfig{Port: r.Port, Mode: mode}, nil
}

// validateUDPConfig validates a rawUDPConfig, returning the typed config on success.
// Payload and ExpectedResponse are raw byte strings; ExpectedResponse without a
// payload is rejected (there is nothing to elicit the expected reply).
func validateUDPConfig(r *rawUDPConfig) (*types.UDPConfig, error) {
	if r.Port < 1 || r.Port > 65535 {
		return nil, fmt.Errorf("udp.port must be in [1, 65535], got %d", r.Port)
	}
	if r.ExpectedResponse != "" && r.Payload == "" {
		return nil, errors.New("udp.expected_response requires a non-empty udp.payload")
	}
	return &types.UDPConfig{Port: r.Port, Payload: r.Payload, ExpectedResponse: r.ExpectedResponse}, nil
}

// dnsRecordTypes is the set of supported DNS record types (upper-cased).
var dnsRecordTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true}

// validateDNSConfig validates a rawDNSConfig and applies defaults, returning the
// typed config on success. RecordType defaults to "A"; Resolver, when set, must
// be a valid host:port.
func validateDNSConfig(r *rawDNSConfig) (*types.DNSConfig, error) {
	recordType := "A"
	if r.RecordType != "" {
		recordType = strings.ToUpper(r.RecordType)
		if !dnsRecordTypes[recordType] {
			return nil, fmt.Errorf("dns.record_type must be one of A, AAAA, CNAME, MX, TXT, got %q", r.RecordType)
		}
	}
	if r.Resolver != "" {
		if _, err := net.ResolveTCPAddr("tcp", r.Resolver); err != nil {
			return nil, fmt.Errorf("dns.resolver must be a valid host:port: %w", err)
		}
	}
	return &types.DNSConfig{RecordType: recordType, Resolver: r.Resolver, ExpectedAnswer: r.ExpectedAnswer}, nil
}

// validateDNSQueryName checks a DNS query name syntactically without resolving
// it, so NXDOMAIN targets remain valid. It rejects only empty names and names
// containing whitespace.
func validateDNSQueryName(entry string) error {
	if entry == "" {
		return errors.New("dns target query name must not be empty")
	}
	if strings.ContainsAny(entry, " \t\r\n") {
		return fmt.Errorf("dns target query name %q must not contain whitespace", entry)
	}
	return nil
}

// validateOneTarget checks that entry is either a valid IP or a resolvable hostname.
func validateOneTarget(entry string) error {
	if net.ParseIP(entry) != nil {
		return nil
	}
	addrs, err := net.LookupHost(entry)
	if err != nil {
		return fmt.Errorf("target %q is not a valid IP and could not be resolved: %w", entry, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("target %q is not a valid IP and resolved to no addresses", entry)
	}
	return nil
}
