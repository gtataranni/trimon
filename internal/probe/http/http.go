package http

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gtataranni/trimon/internal/probe"
	"github.com/gtataranni/trimon/pkg/types"
)

// Prober implements probe.Prober for HTTP/HTTPS.
// Each probe tick sends exactly one request; DurationMS records wall-clock time
// from request start to body drain. Count and PacketInterval from ProbeConfig
// are not used — the scheduler cadence (Interval) controls probe frequency.
type Prober struct {
	cfg     types.ProbeConfig
	clients map[string]*nethttp.Client // keyed by FQDN ("" for bare IPs); read-only after New
}

func New(cfg types.ProbeConfig) *Prober {
	p := &Prober{
		cfg:     cfg,
		clients: make(map[string]*nethttp.Client, len(cfg.Targets)),
	}
	hcfg := cfg.HTTP
	for _, target := range cfg.Targets {
		var fqdn string
		if net.ParseIP(target) == nil {
			fqdn = target
		}
		if _, exists := p.clients[fqdn]; exists {
			continue
		}
		tlsCfg := &tls.Config{}
		if fqdn != "" {
			tlsCfg.ServerName = fqdn
		}
		dialer := &net.Dialer{}
		if cfg.SourceIP != "" {
			dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(cfg.SourceIP)}
		}
		client := &nethttp.Client{
			Timeout: cfg.Timeout,
			Transport: &nethttp.Transport{
				TLSClientConfig: tlsCfg,
				DialContext:     dialer.DialContext,
			},
		}
		if !hcfg.FollowRedirects {
			client.CheckRedirect = func(*nethttp.Request, []*nethttp.Request) error {
				return nethttp.ErrUseLastResponse
			}
		}
		p.clients[fqdn] = client
	}
	return p
}

func (p *Prober) Name() string { return p.cfg.Name }
func (p *Prober) Type() string { return "http" }

func (p *Prober) Run(ctx context.Context) []types.ProbeResult {
	items := probe.ExpandTargets(ctx, p.cfg.Targets, p.cfg.MaxResolvedIPs)
	if len(items) == 0 {
		return nil
	}
	results := make([]types.ProbeResult, len(items))
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(idx int, wi probe.WorkItem) {
			defer wg.Done()
			results[idx] = p.probeOne(ctx, wi)
		}(i, item)
	}
	wg.Wait()
	return results
}

func (p *Prober) probeOne(ctx context.Context, wi probe.WorkItem) types.ProbeResult {
	cfg := p.cfg
	hcfg := cfg.HTTP

	result := types.ProbeResult{
		Timestamp: time.Now().UTC(),
		ProbeName: cfg.Name,
		Target:    wi.IP,
		FQDN:      wi.FQDN,
		SourceIP:  cfg.SourceIP,
		ProbeType: "http",
		Labels:    cfg.Labels,
	}

	if wi.FQDN != "" && wi.IP == wi.FQDN {
		result.Status = types.StatusError
		result.ErrorType = "resolve_error"
		result.ErrorMsg = fmt.Sprintf("resolve target %q: lookup failed", wi.FQDN)
		return result
	}

	// Build URL: always connect to the resolved IP; Host header / SNI use FQDN.
	var host string
	if hcfg.Port != 0 {
		host = net.JoinHostPort(wi.IP, strconv.Itoa(hcfg.Port))
	} else {
		host = net.JoinHostPort(wi.IP, "")
		host = strings.TrimSuffix(host, ":")
	}
	targetURL := hcfg.Scheme + "://" + host + hcfg.Path

	client := p.clients[wi.FQDN]

	req, err := nethttp.NewRequestWithContext(ctx, hcfg.Method, targetURL, nil)
	if err != nil {
		result.Status = types.StatusError
		result.ErrorType = "init_error"
		result.ErrorMsg = fmt.Sprintf("build request for %q: %v", targetURL, err)
		return result
	}
	if wi.FQDN != "" {
		req.Host = wi.FQDN
	}

	result.PacketsSent = 1

	start := time.Now()
	resp, doErr := client.Do(req)
	if doErr != nil {
		result.PacketsReceived = 0
		result.PacketLossRatio = 1.0
		result.ErrorMsg = doErr.Error()
		if ctx.Err() != nil {
			result.Status = types.StatusError
			result.ErrorType = "cancelled"
		} else {
			result.Status = types.StatusFailure
			result.ErrorType = "network_error"
		}
		return result
	}
	//nolint:errcheck // draining the body for connection reuse; copy errors are not actionable
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	result.DurationMS = float64(time.Since(start).Nanoseconds()) / 1e6

	if statusMatch(hcfg.ExpectedStatus, resp.StatusCode) {
		result.PacketsReceived = 1
		result.PacketLossRatio = 0
		result.Status = types.StatusSuccess
	} else {
		result.PacketsReceived = 0
		result.PacketLossRatio = 1.0
		result.Status = types.StatusFailure
	}

	// TLS expiry check (HTTPS only, on a status-matching response).
	// Cert expiry is reflected in the result status only — it is deliberately
	// not emitted as a label.
	if hcfg.Scheme == "https" && result.PacketsReceived == 1 &&
		resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		notAfter := resp.TLS.PeerCertificates[0].NotAfter
		if time.Until(notAfter) < 0 {
			result.Status = types.StatusFailure
		} else if hcfg.TLSExpiryWarningDays > 0 {
			daysLeft := int(time.Until(notAfter).Hours() / 24)
			if daysLeft <= hcfg.TLSExpiryWarningDays {
				result.Status = types.StatusPartial
			}
		}
	}

	return result
}

func statusMatch(expected, actual int) bool {
	if expected == 0 {
		return actual >= 200 && actual < 300
	}
	return actual == expected
}
