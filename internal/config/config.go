package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gtataranni/trimon/pkg/types"
)

var labelKeyRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.\-]*$`)

// GlobalConfig holds daemon-wide defaults.
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

// rawProbeConfig mirrors the YAML shape before merging globals.
type rawProbeConfig struct {
	Name           string            `yaml:"name"`
	Type           string            `yaml:"type"`
	Target         string            `yaml:"target"`
	SourceIP       string            `yaml:"source_ip"`
	Interval       *Duration         `yaml:"probe_every"`
	PacketInterval *Duration         `yaml:"packet_interval"`
	Timeout        *Duration         `yaml:"timeout"`
	Count          *int              `yaml:"count"`
	Labels         map[string]string `yaml:"labels"`
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

// rawFile is the top-level YAML document shape.
type rawFile struct {
	Global    GlobalConfig     `yaml:"global"`
	Exporters ExportersConfig  `yaml:"exporters"`
	Server    ServerConfig     `yaml:"server"`
	Pipeline  PipelineConfig   `yaml:"pipeline"`
	Probes    []rawProbeConfig `yaml:"probes"`
}

// Config is the validated, merged configuration.
type Config struct {
	Global    GlobalConfig
	Exporters ExportersConfig
	Server    ServerConfig
	Pipeline  PipelineConfig
	Probes    []types.ProbeConfig
	// SHA256 is the hex-encoded SHA-256 of the raw config file bytes that produced this Config.
	SHA256 string
}

var knownProbeTypes = map[string]bool{
	"icmp": true,
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

// Load reads and validates the config file at path.
//
// Single-read semantics: the file is read exactly once into a byte buffer here,
// then that buffer is passed to parse() and threaded through every validator
// without any subsequent disk access. This prevents a TOCTOU race where an
// attacker who can write to the config file swaps in different content between
// the read and the validation/use phases. Do not introduce a second read of
// `path` (or any other config file) anywhere in this call chain.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return parse(data)
}

func parse(data []byte) (*Config, error) {
	var raw rawFile

	// Apply defaults before unmarshalling so zero values are distinguishable.
	raw.Global.Interval = 30 * time.Second
	raw.Global.PacketInterval = 1 * time.Second
	raw.Global.Timeout = 5 * time.Second
	raw.Global.Count = 3
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
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	if err := validateGlobal(raw.Global); err != nil {
		return nil, err
	}

	probes, err := mergeAndValidateProbes(raw.Probes, raw.Global)
	if err != nil {
		return nil, err
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

	sum := sha256.Sum256(data)

	return &Config{
		Global:    raw.Global,
		Exporters: raw.Exporters,
		Server:    raw.Server,
		Pipeline:  raw.Pipeline,
		Probes:    probes,
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
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
			return nil, fmt.Errorf("probe %q: unknown type %q (supported: icmp)", r.Name, r.Type)
		}

		if r.Target == "" {
			return nil, fmt.Errorf("probe %q: target is required", r.Name)
		}
		if err := resolveTarget(r.Target); err != nil {
			return nil, fmt.Errorf("probe %q: %w", r.Name, err)
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

		if err := validateTimings(interval, packetInterval, timeout, count); err != nil {
			return nil, fmt.Errorf("probe %q: %w", r.Name, err)
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
			Target:         r.Target,
			SourceIP:       sourceIP,
			Interval:       interval,
			PacketInterval: packetInterval,
			Timeout:        timeout,
			Count:          count,
			Labels:         labels,
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

// resolveTarget checks that target is either a valid IP or a resolvable hostname.
func resolveTarget(target string) error {
	if net.ParseIP(target) != nil {
		return nil
	}
	addrs, err := net.LookupHost(target)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("target %q is not a valid IP and could not be resolved: %v", target, err)
	}
	return nil
}
