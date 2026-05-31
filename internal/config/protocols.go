package config

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/gtataranni/trimon/pkg/types"
)

// validateProtocolConfig validates the protocol-specific config block for the
// probe's type and writes the typed result into pc. ICMP carries no protocol
// block. Returned errors omit the "probe %q:" prefix; the caller adds it.
func validateProtocolConfig(r rawProbeConfig, pc *types.ProbeConfig) error {
	switch r.Type {
	case types.ProbeTypeICMP:
		// no protocol-specific config block
	case types.ProbeTypeHTTP:
		if r.HTTP == nil {
			return errors.New(`http config block is required for type "http"`)
		}
		cfg, err := validateHTTPConfig(r.HTTP)
		if err != nil {
			return err
		}
		pc.HTTP = cfg
	case types.ProbeTypeTCP:
		if r.TCP == nil {
			return errors.New(`tcp config block is required for type "tcp"`)
		}
		cfg, err := validateTCPConfig(r.TCP)
		if err != nil {
			return err
		}
		pc.TCP = cfg
	case types.ProbeTypeUDP:
		if r.UDP == nil {
			return errors.New(`udp config block is required for type "udp"`)
		}
		cfg, err := validateUDPConfig(r.UDP)
		if err != nil {
			return err
		}
		pc.UDP = cfg
	case types.ProbeTypeDNS:
		if r.DNS == nil {
			return errors.New(`dns config block is required for type "dns"`)
		}
		cfg, err := validateDNSConfig(r.DNS)
		if err != nil {
			return err
		}
		pc.DNS = cfg
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
