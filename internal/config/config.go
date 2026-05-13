package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gtataranni/trimon/pkg/types"
)

// GlobalConfig holds daemon-wide defaults.
type GlobalConfig struct {
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
	Count    int           `yaml:"count"`
	SourceIP string        `yaml:"source_ip"`
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
	Enabled  bool            `yaml:"enabled"`
	Endpoint string          `yaml:"endpoint"`
	Protocol string          `yaml:"protocol"` // grpc | http
	Insecure bool            `yaml:"insecure"`
	TLS      OTLPTLSConfig   `yaml:"tls"`
	Batch    OTLPBatchConfig `yaml:"batch"`
	Retry    OTLPRetryConfig `yaml:"retry"`
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

// rawProbeConfig mirrors the YAML shape before merging globals.
type rawProbeConfig struct {
	Name     string            `yaml:"name"`
	Type     string            `yaml:"type"`
	Target   string            `yaml:"target"`
	SourceIP string            `yaml:"source_ip"`
	Interval *Duration         `yaml:"interval"`
	Timeout  *Duration         `yaml:"timeout"`
	Count    *int              `yaml:"count"`
	Labels   map[string]string `yaml:"labels"`
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
	Probes    []rawProbeConfig `yaml:"probes"`
}

// Config is the validated, merged configuration.
type Config struct {
	Global    GlobalConfig
	Exporters ExportersConfig
	Server    ServerConfig
	Probes    []types.ProbeConfig
}

var knownProbeTypes = map[string]bool{
	"icmp": true,
}

// Load reads and validates the config file at path.
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
	raw.Global.Timeout = 5 * time.Second
	raw.Global.Count = 3
	raw.Server.Listen = ":8080"
	raw.Exporters.Stdout.Format = "json"
	raw.Exporters.OTLP.Protocol = "grpc"
	raw.Exporters.OTLP.Batch.ExportInterval = 30 * time.Second
	raw.Exporters.OTLP.Batch.ExportTimeout = 10 * time.Second
	raw.Exporters.OTLP.Retry.Enabled = true
	raw.Exporters.OTLP.Retry.MaxElapsedTime = 300 * time.Second

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

	return &Config{
		Global:    raw.Global,
		Exporters: raw.Exporters,
		Server:    raw.Server,
		Probes:    probes,
	}, nil
}

func validateGlobal(g GlobalConfig) error {
	if g.Interval <= 0 {
		return errors.New("global.interval must be positive")
	}
	if g.Timeout <= 0 {
		return errors.New("global.timeout must be positive")
	}
	if g.Count <= 0 {
		return errors.New("global.count must be positive")
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

		interval := global.Interval
		if r.Interval != nil {
			interval = r.Interval.Duration
		}
		timeout := global.Timeout
		if r.Timeout != nil {
			timeout = r.Timeout.Duration
		}
		count := global.Count
		if r.Count != nil {
			count = *r.Count
		}

		if interval <= 0 {
			return nil, fmt.Errorf("probe %q: interval must be positive", r.Name)
		}
		if timeout <= 0 {
			return nil, fmt.Errorf("probe %q: timeout must be positive", r.Name)
		}
		if count <= 0 {
			return nil, fmt.Errorf("probe %q: count must be positive", r.Name)
		}

		labels := r.Labels
		if labels == nil {
			labels = make(map[string]string)
		}

		out = append(out, types.ProbeConfig{
			Name:     r.Name,
			Type:     r.Type,
			Target:   r.Target,
			SourceIP: sourceIP,
			Interval: interval,
			Timeout:  timeout,
			Count:    count,
			Labels:   labels,
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
