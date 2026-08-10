package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

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

// rawProbeFile is the YAML shape of the probe config file (--probes flag).
type rawProbeFile struct {
	Global GlobalConfig     `yaml:"global"`
	Probes []rawProbeConfig `yaml:"probes"`
}

// rawProbeFragment is the YAML shape of a probe file inside a probe config
// directory. Global is a pointer purely to detect presence of the `global:` key,
// which is rejected outside the reserved _global.yaml file.
type rawProbeFragment struct {
	Global *GlobalConfig    `yaml:"global"`
	Probes []rawProbeConfig `yaml:"probes"`
}

// rawOpsFile is the YAML shape of the ops config file (--config flag).
type rawOpsFile struct {
	Exporters ExportersConfig `yaml:"exporters"`
	Server    ServerConfig    `yaml:"server"`
	Pipeline  PipelineConfig  `yaml:"pipeline"`
}
