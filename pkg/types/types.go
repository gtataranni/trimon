package types

import "time"

// Status values for ProbeResult.
type Status string

const (
	ProbeTypeICMP = "icmp"
	ProbeTypeTCP  = "tcp"
	ProbeTypeHTTP = "http"

	StatusSuccess Status = "success" // 0% packet loss
	StatusPartial Status = "partial" // 0% < loss < 100%
	StatusFailure Status = "failure" // 100% loss
	StatusError   Status = "error"   // probe could not execute
)

func (s Status) IsSuccess() bool { return s == StatusSuccess }
func (s Status) IsUp() bool      { return s == StatusSuccess || s == StatusPartial }
func (s Status) IsError() bool   { return s == StatusError }

// HTTPConfig holds HTTP/HTTPS probe parameters.
type HTTPConfig struct {
	Scheme               string // "http" or "https"
	Port                 int    // 0 = scheme default (80/443)
	Path                 string // default "/"
	Method               string // "GET", "HEAD", or "POST"
	ExpectedStatus       int    // 0 = any 2xx; otherwise exact match
	FollowRedirects      bool
	TLSExpiryWarningDays int // if > 0: StatusPartial when cert expires within N days
}

// TCP probe modes.
const (
	// TCPModeConnect completes a full TCP handshake (kernel connect), then
	// closes it. No special privileges required. Measures handshake latency.
	TCPModeConnect = "connect"
	// TCPModeSYN sends a raw half-open SYN and classifies the reply without
	// completing the handshake. Requires raw sockets (CAP_NET_RAW) and Linux.
	// Measures network round-trip and port reachability.
	TCPModeSYN = "syn"
)

// TCPConfig holds TCP probe parameters.
type TCPConfig struct {
	Port int    // required; 1–65535
	Mode string // "connect" (default) or "syn"
}

// ProbeConfig is the merged, validated probe configuration built by internal/config.
type ProbeConfig struct {
	Name           string
	Type           string
	Targets        []string // one or more IPs or FQDNs; FQDNs are re-resolved at every probe run
	MaxResolvedIPs int      // cap on IPs probed per FQDN entry (0 = unlimited)
	SourceIP       string
	Interval       time.Duration // scheduler cadence: how often to run the probe
	PacketInterval time.Duration // wait between individual probe attempts
	Timeout        time.Duration
	Count          int
	Labels         map[string]string
	HTTP           *HTTPConfig
	TCP            *TCPConfig
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
	// RTT* only valid for multi-packet probes (e.g. ICMP) when PacketsReceived > 0.
	RTTMinMS    float64 `json:"rtt_min_ms"`
	RTTMeanMS   float64 `json:"rtt_mean_ms"`
	RTTMaxMS    float64 `json:"rtt_max_ms"`
	RTTStddevMS float64 `json:"rtt_stddev_ms"`
	// DurationMS is the wall-clock time from request start to body drain.
	// Set by single-request probes (e.g. HTTP); zero for multi-packet probes.
	DurationMS      float64 `json:"duration_ms,omitempty"`
	PacketLossRatio float64 `json:"packet_loss"`
	// PortOpen is the TCP port reachability state, set only by TCP probes.
	// nil   = not applicable (non-TCP probe, or probe could not run / status=error)
	// true  = port open  (SYN/ACK received)
	// false = port not open (RST/closed, or no reply at all)
	// Combine with probe.up to distinguish closed (up=1) from filtered/down (up=0).
	PortOpen  *bool             `json:"port_open,omitempty"`
	Status    Status            `json:"status"`
	ErrorMsg  string            `json:"error_msg,omitempty"`
	ErrorType string            `json:"error_type,omitempty"`
	Labels    map[string]string `json:"labels"`
}
