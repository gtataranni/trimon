package types

import "time"

// Status values for ProbeResult.
type Status string

const (
	StatusSuccess Status = "success" // 0% packet loss
	StatusPartial Status = "partial" // 0% < loss < 100%
	StatusFailure Status = "failure" // 100% loss
	StatusError   Status = "error"   // probe could not execute
)

func (s Status) IsSuccess() bool { return s == StatusSuccess }
func (s Status) IsUp() bool      { return s == StatusSuccess || s == StatusPartial }
func (s Status) IsError() bool   { return s == StatusError }

// ProbeConfig holds per-probe configuration after parsing and validation.
type ProbeConfig struct {
	Name           string            `yaml:"name"`
	Type           string            `yaml:"type"`
	Target         string            `yaml:"target"`
	SourceIP       string            `yaml:"source_ip"`
	Interval       time.Duration     `yaml:"probe_every"`      // scheduler cadence: how often to run the probe
	PacketInterval time.Duration     `yaml:"packet_interval"`  // pro-bing: wait between individual ICMP echo sends
	Timeout        time.Duration     `yaml:"timeout"`
	Count          int               `yaml:"count"`
	Labels         map[string]string `yaml:"labels"`
}

// ProbeResult is the output of a single probe run.
type ProbeResult struct {
	Timestamp         time.Time         `json:"ts"`
	ProbeName         string            `json:"probe"`
	Target            string            `json:"target"`
	SourceIP          string            `json:"source_ip"`
	ProbeType         string            `json:"type"`
	PacketsSent       int               `json:"packets_sent"`
	PacketsReceived   int               `json:"packets_received"`
	RTTMinMS          float64           `json:"rtt_min_ms"`
	RTTMeanMS         float64           `json:"rtt_mean_ms"`
	RTTMaxMS          float64           `json:"rtt_max_ms"`
	RTTStddevMS       float64           `json:"rtt_stddev_ms"`
	PacketLossRatio   float64           `json:"packet_loss"`
	Status            Status            `json:"status"`
	ErrorMsg          string            `json:"error_msg,omitempty"`
	ErrorType         string            `json:"error_type,omitempty"`
	Labels            map[string]string `json:"labels"`
}
