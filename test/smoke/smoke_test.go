//go:build smoke

// End-to-end smoke assertions. These run against an already-running dev-stack
// (trimon + OTel Collector) — start it with scripts/smoke.sh (`make smoke`),
// which builds the real Linux binary in a container, waits for readiness, then
// invokes this suite. The tests are pure HTTP clients, so they compile and run
// on any host; only the daemon under test is Linux-only.
//
// Endpoints are overridable via env so the same suite can target a remote stack:
//
//	TRIMON_BASE_URL     base URL of the trimon HTTP server (default http://localhost:8080)
//	OTELCOL_METRICS_URL collector Prometheus endpoint      (default http://localhost:8889/metrics)
//	SMOKE_TIMEOUT       per-assertion budget, Go duration  (default 120s)
//
// The Prometheus text-parsing helpers live in parse.go (tag-free, unit-tested in
// parse_test.go); this file holds only the network-touching assertions.
package smoke

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	defaultBaseURL      = "http://localhost:8080"
	defaultCollectorURL = "http://localhost:8889/metrics"
	defaultBudget       = 120 * time.Second
	pollInterval        = 3 * time.Second
)

// protocolChecks maps each probe type the demo exercises to a metric that is
// non-NaN only for that protocol family when the probe is up. Together with the
// universal probe_up==1 reachability check this proves every protocol path runs
// end-to-end and emits its distinctive metric (see docs/metrics.md):
//   - icmp/dns measure RTT           → trimon_probe_rtt_mean_milliseconds
//   - tcp/udp  measure port state    → trimon_probe_port_open
//   - http     measures req duration → trimon_probe_duration_milliseconds
var protocolChecks = []struct {
	probeType   string
	fingerprint string // metric expected non-NaN for ≥1 up series of this type
}{
	{"icmp", "trimon_probe_rtt_mean_milliseconds"},
	{"tcp", "trimon_probe_port_open"},
	{"udp", "trimon_probe_port_open"},
	{"dns", "trimon_probe_rtt_mean_milliseconds"},
	{"http", "trimon_probe_duration_milliseconds"},
}

func baseURL() string {
	if v := os.Getenv("TRIMON_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultBaseURL
}

func collectorURL() string {
	if v := os.Getenv("OTELCOL_METRICS_URL"); v != "" {
		return v
	}
	return defaultCollectorURL
}

func budget(t *testing.T) time.Duration {
	t.Helper()
	v := os.Getenv("SMOKE_TIMEOUT")
	if v == "" {
		return defaultBudget
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("invalid SMOKE_TIMEOUT %q: %v", v, err)
	}
	return d
}

// httpGet fetches a URL and returns its status code and body. A transport error
// is reported as status 0 with the error text as the body, so callers polling
// for readiness can treat a not-yet-listening endpoint like any other miss.
func httpGet(url string) (int, string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err.Error()
	}
	return resp.StatusCode, string(body)
}

// eventually polls fn until it returns nil or the budget elapses, reporting the
// last error via t.Fatalf on timeout.
func eventually(t *testing.T, what string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(budget(t))
	var last error
	for {
		if last = fn(); last == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: condition not met within %s: %v", what, budget(t), last)
		}
		time.Sleep(pollInterval)
	}
}

// TestSmokeHealthz asserts the trimon HTTP server is live and /healthz returns 200.
func TestSmokeHealthz(t *testing.T) {
	url := baseURL() + "/healthz"
	eventually(t, "GET "+url, func() error {
		code, body := httpGet(url)
		if code != http.StatusOK {
			return fmt.Errorf("status %d (body: %.120q)", code, body)
		}
		return nil
	})
}

// TestSmokeConfigEndpoint asserts the /config management endpoint serves the
// active config (proves config load + the HTTP API surface beyond /metrics).
func TestSmokeConfigEndpoint(t *testing.T) {
	url := baseURL() + "/config"
	code, body := httpGet(url)
	if code != http.StatusOK {
		t.Fatalf("GET %s: status %d (body: %.120q)", url, code, body)
	}
	if !strings.Contains(body, "probe") {
		t.Fatalf("GET %s: body does not look like a trimon config (got %.200q)", url, body)
	}
}

// TestSmokeProbeMetrics is the core check: it polls trimon's own /metrics until
// every demo protocol (icmp, tcp, udp, dns, http) reports at least one reachable
// target (probe_up==1) and emits its distinctive non-NaN metric. /metrics is the
// authoritative, scrape-time view of the recorded instruments, so it is fresher
// than the collector re-export checked separately below.
func TestSmokeProbeMetrics(t *testing.T) {
	url := baseURL() + "/metrics"
	eventually(t, "all protocols report on "+url, func() error {
		code, body := httpGet(url)
		if code != http.StatusOK {
			return fmt.Errorf("status %d", code)
		}
		samples := parsePromText(body)

		for _, c := range protocolChecks {
			ups := ofType(samples, "trimon_probe_up", c.probeType)
			if len(ups) == 0 {
				return fmt.Errorf("%s: no trimon_probe_up series yet", c.probeType)
			}
			if !anyValue(ups, 1) {
				return fmt.Errorf("%s: no reachable target (all probe_up==0)", c.probeType)
			}

			fps := ofType(samples, c.fingerprint, c.probeType)
			if !anyNonNaN(fps) {
				return fmt.Errorf("%s: %s has no non-NaN value yet", c.probeType, c.fingerprint)
			}
		}
		return nil
	})
}

// TestSmokeOTLPCollector asserts the OTLP push path works end-to-end: trimon's
// probe results reach the OTel Collector, which re-exposes them as Prometheus
// text on :8889. The collector pushes on its own interval, so this can lag
// /metrics by one export cycle — hence the polling budget.
func TestSmokeOTLPCollector(t *testing.T) {
	url := collectorURL()
	eventually(t, "trimon_probe_up re-exposed by collector on "+url, func() error {
		code, body := httpGet(url)
		if code != http.StatusOK {
			return fmt.Errorf("status %d", code)
		}
		samples := parsePromText(body)
		var ups int
		for _, s := range samples {
			if s.name == "trimon_probe_up" {
				ups++
			}
		}
		if ups == 0 {
			return fmt.Errorf("collector has no trimon_probe_up series yet")
		}
		return nil
	})
}
