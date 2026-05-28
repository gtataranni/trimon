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

// ProbeConfig is the merged, validated probe configuration built by internal/config.
type ProbeConfig struct {
	Name           string
	Type           string
	Targets        []string // one or more IPs or FQDNs; FQDNs are re-resolved at every probe run
	MaxResolvedIPs int      // cap on IPs probed per FQDN entry (0 = unlimited)
	SourceIP       string
	Interval       time.Duration // scheduler cadence: how often to run the probe
	PacketInterval time.Duration // pro-bing: wait between individual ICMP echo sends
	Timeout        time.Duration
	Count          int
	Labels         map[string]string
}

// ProbeResult is the output of a single probe run against one IP target.
type ProbeResult struct {
	Timestamp time.Time `json:"ts"`
	ProbeName string    `json:"probe"`
	Target    string    `json:"target"`
	// FQDN is the domain name that resolved to Target. Non-empty only when
	// the probe config entry was a hostname rather than a literal IP address.
	FQDN            string `json:"fqdn,omitempty"`
	SourceIP        string `json:"source_ip"`
	ProbeType       string `json:"type"`
	PacketsSent     int    `json:"packets_sent"`
	PacketsReceived int    `json:"packets_received"`
	// RTT* only valid when PacketsReceived > 0; zero otherwise.
	RTTMinMS        float64           `json:"rtt_min_ms"`
	RTTMeanMS       float64           `json:"rtt_mean_ms"`
	RTTMaxMS        float64           `json:"rtt_max_ms"`
	RTTStddevMS     float64           `json:"rtt_stddev_ms"`
	PacketLossRatio float64           `json:"packet_loss"`
	Status          Status            `json:"status"`
	ErrorMsg        string            `json:"error_msg,omitempty"`
	ErrorType       string            `json:"error_type,omitempty"`
	Labels          map[string]string `json:"labels"`
}
