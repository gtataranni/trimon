package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

var labelKeyRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.\-]*$`)

var knownProbeTypes = map[string]bool{
	types.ProbeTypeICMP: true, types.ProbeTypeHTTP: true, types.ProbeTypeTCP: true,
	types.ProbeTypeUDP: true, types.ProbeTypeDNS: true,
}

// localInterfaceAddrs returns the list of unicast addresses assigned to local interfaces.
// It is a package-level variable so tests can replace it with a stub.
var localInterfaceAddrs = func() ([]net.Addr, error) {
	return net.InterfaceAddrs()
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

// mergeAndValidateProbes merges global defaults into each raw probe and validates
// it, returning the typed probe configs. Probe names must be unique across the set.
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

		pc, err := mergeAndValidateProbe(r, global)
		if err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, nil
}

// mergeAndValidateProbe merges global defaults into one raw probe and validates
// every field. Name uniqueness is enforced by the caller since it spans probes.
func mergeAndValidateProbe(r rawProbeConfig, global GlobalConfig) (types.ProbeConfig, error) {
	if r.Type == "" {
		return types.ProbeConfig{}, fmt.Errorf("probe %q: type is required", r.Name)
	}
	if !knownProbeTypes[r.Type] {
		return types.ProbeConfig{}, fmt.Errorf("probe %q: unknown type %q (supported: %s)", r.Name, r.Type, strings.Join(slices.Sorted(maps.Keys(knownProbeTypes)), ", "))
	}
	if err := validateTargets(r); err != nil {
		return types.ProbeConfig{}, fmt.Errorf("probe %q: %w", r.Name, err)
	}
	if r.MaxResolvedIPs < 0 {
		return types.ProbeConfig{}, fmt.Errorf("probe %q: max_resolved_ips must be >= 0", r.Name)
	}

	sourceIP, err := resolveSourceIP(r, global)
	if err != nil {
		return types.ProbeConfig{}, fmt.Errorf("probe %q: %w", r.Name, err)
	}

	interval, packetInterval, timeout, count := mergeTimings(r, global)
	// HTTP sends exactly one request per tick, so the packet timing constraints
	// (packet_interval * count) do not apply to it.
	if r.Type != types.ProbeTypeHTTP {
		if err := validateTimings(interval, packetInterval, timeout, count); err != nil {
			return types.ProbeConfig{}, fmt.Errorf("probe %q: %w", r.Name, err)
		}
	}

	if err := validateLabels(r.Labels); err != nil {
		return types.ProbeConfig{}, fmt.Errorf("probe %q: %w", r.Name, err)
	}

	pc := types.ProbeConfig{
		Name:           r.Name,
		Type:           r.Type,
		Targets:        r.Targets,
		MaxResolvedIPs: r.MaxResolvedIPs,
		SourceIP:       sourceIP,
		Interval:       interval,
		PacketInterval: packetInterval,
		Timeout:        timeout,
		Count:          count,
		Labels:         r.Labels,
	}
	if err := validateProtocolConfig(r, &pc); err != nil {
		return types.ProbeConfig{}, fmt.Errorf("probe %q: %w", r.Name, err)
	}
	return pc, nil
}

// validateTargets validates every target entry for a probe. DNS targets are
// query names, not hosts to connect to, so they are checked syntactically
// (NXDOMAIN targets remain valid) rather than resolved at load time.
func validateTargets(r rawProbeConfig) error {
	if len(r.Targets) == 0 {
		return errors.New("targets is required and must have at least one entry")
	}
	validate := validateOneTarget
	if r.Type == types.ProbeTypeDNS {
		validate = validateDNSQueryName
	}
	for _, t := range r.Targets {
		if err := validate(t); err != nil {
			return err
		}
	}
	return nil
}

// resolveSourceIP determines the effective source IP for a probe: the per-probe
// value takes precedence over the global default; empty means the OS chooses.
// A non-empty value must be a valid IP assigned to a local interface.
func resolveSourceIP(r rawProbeConfig, global GlobalConfig) (string, error) {
	sourceIP := r.SourceIP
	if sourceIP == "" {
		sourceIP = global.SourceIP
	}
	if sourceIP == "" {
		return "", nil
	}
	if net.ParseIP(sourceIP) == nil {
		return "", fmt.Errorf("source_ip %q is not a valid IP address", sourceIP)
	}
	local, err := isLocalIP(sourceIP)
	if err != nil {
		return "", err
	}
	if !local {
		return "", fmt.Errorf("source_ip %q is not assigned to any local interface", sourceIP)
	}
	return sourceIP, nil
}

// mergeTimings overlays the per-probe timing overrides on the global defaults.
func mergeTimings(r rawProbeConfig, global GlobalConfig) (interval, packetInterval, timeout time.Duration, count int) {
	interval = global.Interval
	if r.Interval != nil {
		interval = r.Interval.Duration
	}
	packetInterval = global.PacketInterval
	if r.PacketInterval != nil {
		packetInterval = r.PacketInterval.Duration
	}
	timeout = global.Timeout
	if r.Timeout != nil {
		timeout = r.Timeout.Duration
	}
	count = global.Count
	if r.Count != nil {
		count = *r.Count
	}
	return interval, packetInterval, timeout, count
}

// validateLabels checks that every label key is a valid OTel attribute name and
// that no value contains control characters.
func validateLabels(labels map[string]string) error {
	for k, v := range labels {
		if !labelKeyRE.MatchString(k) {
			return fmt.Errorf("label key %q is not a valid OTel attribute name", k)
		}
		if strings.ContainsAny(v, "\n\r\t") {
			return fmt.Errorf("label %q value contains control characters", k)
		}
	}
	return nil
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
