package server

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/pkg/types"
)

func newTestServer() *Server {
	return New(":0", "test-version", "abc1234")
}

func TestHealthz(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("body.status: want ok, got %q", body["status"])
	}
}

func TestMetrics(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct == "" {
		t.Error("Content-Type should not be empty for /metrics")
	}
}

func TestMetricsContainsBuildInfo(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "trimon_build_info") {
		t.Error("expected trimon_build_info in /metrics output")
	}
}

func TestReloadNoFunc(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status: want 503, got %d", rr.Code)
	}
}

func TestReloadSuccess(t *testing.T) {
	srv := newTestServer()
	called := false
	srv.SetReloadFunc(func() (*config.Config, error) {
		called = true
		return &config.Config{Probes: []types.ProbeConfig{{Name: "p1"}}}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/reload", bytes.NewReader(nil))
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", rr.Code)
	}
	if !called {
		t.Error("reload func was not called")
	}

	var body map[string]interface{}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if reloaded, _ := body["reloaded"].(bool); !reloaded {
		t.Errorf("body.reloaded: want true, got %v", body["reloaded"])
	}
}

// metricsBody hits /metrics on the given server and returns the response body.
func metricsBody(t *testing.T, srv *Server) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/metrics: want 200, got %d", rr.Code)
	}
	return rr.Body.String()
}

func TestRecordResultMetrics(t *testing.T) {
	const probe = "test-probe"

	cases := []struct {
		name            string
		result          types.ProbeResult
		wantUp          string // substring expected in /metrics output
		wantLossRatio   string
		wantPacketsSent string // empty means skip check
		wantPktsRecv    string // empty means skip check
		wantLossNaN     bool
	}{
		{
			name: "success",
			result: types.ProbeResult{
				ProbeName:       probe,
				Status:          types.StatusSuccess,
				PacketsSent:     4,
				PacketsReceived: 4,
				PacketLossRatio: 0.0,
			},
			wantUp:          `trimon_probe_up{probe_name="test-probe"} 1`,
			wantLossRatio:   `trimon_probe_packet_loss_ratio{probe_name="test-probe"} 0`,
			wantPacketsSent: `trimon_probe_packets_sent_total{probe_name="test-probe"} 4`,
			wantPktsRecv:    `trimon_probe_packets_received_total{probe_name="test-probe"} 4`,
		},
		{
			name: "partial",
			result: types.ProbeResult{
				ProbeName:       probe,
				Status:          types.StatusPartial,
				PacketsSent:     4,
				PacketsReceived: 2,
				PacketLossRatio: 0.5,
			},
			wantUp:        `trimon_probe_up{probe_name="test-probe"} 1`,
			wantLossRatio: `trimon_probe_packet_loss_ratio{probe_name="test-probe"} 0.5`,
		},
		{
			name: "failure",
			result: types.ProbeResult{
				ProbeName:       probe,
				Status:          types.StatusFailure,
				PacketsSent:     4,
				PacketsReceived: 0,
				PacketLossRatio: 1.0,
			},
			wantUp:        `trimon_probe_up{probe_name="test-probe"} 0`,
			wantLossRatio: `trimon_probe_packet_loss_ratio{probe_name="test-probe"} 1`,
		},
		{
			name: "error",
			result: types.ProbeResult{
				ProbeName:       probe,
				Status:          types.StatusError,
				PacketsSent:     0,
				PacketsReceived: 0,
			},
			wantUp:      `trimon_probe_up{probe_name="test-probe"} 0`,
			wantLossNaN: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer()
			srv.RecordResult(tc.result)
			body := metricsBody(t, srv)

			if !strings.Contains(body, tc.wantUp) {
				t.Errorf("probe_up: want substring %q in /metrics output\ngot:\n%s", tc.wantUp, body)
			}

			if tc.wantLossNaN {
				if !strings.Contains(body, `trimon_probe_packet_loss_ratio{probe_name="test-probe"} NaN`) {
					t.Errorf("packet_loss_ratio: want NaN in /metrics output\ngot:\n%s", body)
				}
			} else if tc.wantLossRatio != "" {
				if !strings.Contains(body, tc.wantLossRatio) {
					t.Errorf("packet_loss_ratio: want substring %q in /metrics output\ngot:\n%s", tc.wantLossRatio, body)
				}
			}

			if tc.wantPacketsSent != "" && !strings.Contains(body, tc.wantPacketsSent) {
				t.Errorf("packets_sent: want substring %q in /metrics output\ngot:\n%s", tc.wantPacketsSent, body)
			}
			if tc.wantPktsRecv != "" && !strings.Contains(body, tc.wantPktsRecv) {
				t.Errorf("packets_received: want substring %q in /metrics output\ngot:\n%s", tc.wantPktsRecv, body)
			}
		})
	}
}

func TestRecordResultCounterAccumulation(t *testing.T) {
	const probe = "test-probe"
	srv := newTestServer()

	run := types.ProbeResult{
		ProbeName:       probe,
		Status:          types.StatusSuccess,
		PacketsSent:     4,
		PacketsReceived: 4,
		PacketLossRatio: 0.0,
	}
	srv.RecordResult(run)
	srv.RecordResult(run)

	body := metricsBody(t, srv)

	if !strings.Contains(body, `trimon_probe_packets_sent_total{probe_name="test-probe"} 8`) {
		t.Errorf("packets_sent after two runs: want 8\ngot:\n%s", body)
	}
	if !strings.Contains(body, `trimon_probe_packets_received_total{probe_name="test-probe"} 8`) {
		t.Errorf("packets_received after two runs: want 8\ngot:\n%s", body)
	}
	if !strings.Contains(body, `trimon_probe_runs_total{probe_name="test-probe"} 2`) {
		t.Errorf("runs_total after two runs: want 2\ngot:\n%s", body)
	}

	// Confirm math import is used — NaN sentinel check (compile-time guard).
	_ = math.NaN()
}

