package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

// baseCfg returns a ProbeConfig pointed at the given httptest server.
func baseCfg(ts *httptest.Server, overrides ...func(*types.ProbeConfig)) types.ProbeConfig {
	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())
	cfg := types.ProbeConfig{
		Name:           "test",
		Type:           "http",
		Targets:        []string{u.Hostname()},
		Count:          1,
		PacketInterval: 50 * time.Millisecond,
		Timeout:        5 * time.Second,
		Interval:       30 * time.Second,
		HTTP: &types.HTTPConfig{
			Scheme:          "http",
			Port:            port,
			Path:            "/",
			Method:          "GET",
			FollowRedirects: true,
		},
	}
	for _, fn := range overrides {
		fn(&cfg)
	}
	return cfg
}

func TestHTTPSuccess(t *testing.T) {
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	results := New(baseCfg(ts)).Run(context.Background())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (error: %s)", r.Status, r.ErrorMsg)
	}
	if r.PacketsSent != 1 || r.PacketsReceived != 1 {
		t.Errorf("packets: sent=%d recv=%d, want 1/1", r.PacketsSent, r.PacketsReceived)
	}
	if r.PacketLossRatio != 0 {
		t.Errorf("PacketLossRatio: got %f, want 0", r.PacketLossRatio)
	}
	if r.DurationMS <= 0 {
		t.Errorf("DurationMS: got %f, want > 0", r.DurationMS)
	}
}

func TestHTTPDurationSetOnFailedStatus(t *testing.T) {
	// DurationMS is set even when the status code doesn't match, because we
	// still received a response — the timing is valid.
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(503)
	}))
	defer ts.Close()

	cfg := baseCfg(ts, func(c *types.ProbeConfig) {
		c.HTTP.ExpectedStatus = 200
	})
	results := New(cfg).Run(context.Background())
	r := results[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
	if r.DurationMS <= 0 {
		t.Errorf("DurationMS: got %f, want > 0 even on status mismatch", r.DurationMS)
	}
}

func TestHTTPDurationZeroOnNetworkError(t *testing.T) {
	// When client.Do fails (server closed), DurationMS stays 0.
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {}))
	ts.Close() // shut down immediately

	results := New(baseCfg(ts)).Run(context.Background())
	r := results[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
	if r.DurationMS != 0 {
		t.Errorf("DurationMS: got %f, want 0 when no response received", r.DurationMS)
	}
}

func TestHTTPStatusMismatch(t *testing.T) {
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(404)
	}))
	defer ts.Close()

	cfg := baseCfg(ts, func(c *types.ProbeConfig) {
		c.HTTP.ExpectedStatus = 200
	})
	results := New(cfg).Run(context.Background())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
	if r.PacketsSent != 1 || r.PacketsReceived != 0 {
		t.Errorf("packets: sent=%d recv=%d, want 1/0", r.PacketsSent, r.PacketsReceived)
	}
}

func TestHTTPAny2xx(t *testing.T) {
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(201)
	}))
	defer ts.Close()

	cfg := baseCfg(ts, func(c *types.ProbeConfig) {
		c.HTTP.ExpectedStatus = 0 // any 2xx
	})
	results := New(cfg).Run(context.Background())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess", results[0].Status)
	}
}

func TestHTTPRedirectFollowed(t *testing.T) {
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/" {
			nethttp.Redirect(w, r, "/dest", nethttp.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	cfg := baseCfg(ts, func(c *types.ProbeConfig) {
		c.HTTP.FollowRedirects = true
	})
	results := New(cfg).Run(context.Background())
	if results[0].Status != types.StatusSuccess {
		t.Errorf("redirect followed: got %s, want StatusSuccess", results[0].Status)
	}
}

func TestHTTPRedirectNotFollowed(t *testing.T) {
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		nethttp.Redirect(w, r, "/dest", nethttp.StatusFound)
	}))
	defer ts.Close()

	cfg := baseCfg(ts, func(c *types.ProbeConfig) {
		c.HTTP.FollowRedirects = false
		c.HTTP.ExpectedStatus = 302
	})
	results := New(cfg).Run(context.Background())
	if results[0].Status != types.StatusSuccess {
		t.Errorf("redirect not followed: got %s, want StatusSuccess (302 match)", results[0].Status)
	}
}

func TestHTTPResolveError(t *testing.T) {
	cfg := types.ProbeConfig{
		Name:           "test",
		Type:           "http",
		Targets:        []string{"invalid..hostname"},
		Count:          1,
		PacketInterval: 50 * time.Millisecond,
		Timeout:        5 * time.Second,
		Interval:       30 * time.Second,
		HTTP: &types.HTTPConfig{
			Scheme: "http", Path: "/", Method: "GET", FollowRedirects: true,
		},
	}
	results := New(cfg).Run(context.Background())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != types.StatusError {
		t.Errorf("Status: got %s, want StatusError", r.Status)
	}
	if r.ErrorType != "resolve_error" {
		t.Errorf("ErrorType: got %q, want resolve_error", r.ErrorType)
	}
	if r.PacketsSent != 0 {
		t.Errorf("PacketsSent must be 0 on resolve error, got %d", r.PacketsSent)
	}
	if r.DurationMS != 0 {
		t.Errorf("DurationMS must be 0 on resolve error, got %f", r.DurationMS)
	}
}

func TestHTTPNoRTTFields(t *testing.T) {
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	results := New(baseCfg(ts)).Run(context.Background())
	r := results[0]
	if r.RTTMinMS != 0 || r.RTTMeanMS != 0 || r.RTTMaxMS != 0 || r.RTTStddevMS != 0 {
		t.Errorf("RTT fields must be zero for HTTP (single-request model): min=%f mean=%f max=%f stddev=%f",
			r.RTTMinMS, r.RTTMeanMS, r.RTTMaxMS, r.RTTStddevMS)
	}
}

func TestStatusMatchHelper(t *testing.T) {
	tests := []struct {
		expected, actual int
		want             bool
	}{
		{0, 200, true},
		{0, 201, true},
		{0, 299, true},
		{0, 300, false},
		{0, 404, false},
		{200, 200, true},
		{200, 201, false},
		{404, 404, true},
		{404, 200, false},
	}
	for _, tt := range tests {
		got := statusMatch(tt.expected, tt.actual)
		if got != tt.want {
			t.Errorf("statusMatch(%d, %d) = %v, want %v", tt.expected, tt.actual, got, tt.want)
		}
	}
}

func TestHTTPProbeType(t *testing.T) {
	ts := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	results := New(baseCfg(ts)).Run(context.Background())
	if results[0].ProbeType != "http" {
		t.Errorf("ProbeType: got %q, want http", results[0].ProbeType)
	}
}
