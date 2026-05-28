package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/pkg/types"
)

func newTestServer() *Server {
	s := New(":0")
	s.SetMetricsHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
	}))
	return s
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

func TestHealthzDegradedWhenBufferSaturated(t *testing.T) {
	srv := newTestServer()
	srv.SetHealthChecker(func() float64 { return 0.95 })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status: want 503, got %d", rr.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("body.status: want degraded, got %q", body["status"])
	}
	if _, ok := body["buffer_usage"]; !ok {
		t.Error("body.buffer_usage should be present in degraded response")
	}
}

func TestHealthzOKWhenCheckerBelowThreshold(t *testing.T) {
	srv := newTestServer()
	srv.SetHealthChecker(func() float64 { return 0.5 })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", rr.Code)
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

func TestMetricsNoHandler(t *testing.T) {
	srv := New(":0")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", rr.Code)
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

func TestReloadErrorIsSanitized(t *testing.T) {
	srv := newTestServer()
	const sensitive = "open /etc/trimon/secret.yaml: permission denied"
	srv.SetReloadFunc(func() (*config.Config, error) {
		return nil, errors.New(sensitive)
	})

	req := httptest.NewRequest(http.MethodPost, "/reload", bytes.NewReader(nil))
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), sensitive) {
		t.Errorf("response body leaks internal error: %s", rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if reloaded, _ := body["reloaded"].(bool); reloaded {
		t.Errorf("body.reloaded: want false, got %v", body["reloaded"])
	}
	msg, _ := body["error"].(string)
	if msg == "" || strings.Contains(msg, "/etc/") {
		t.Errorf("body.error should be a generic message, got %q", msg)
	}
}

func TestConfigEndpointShape(t *testing.T) {
	srv := newTestServer()
	srv.UpdateConfig(&config.Config{
		Global: config.GlobalConfig{
			Interval:       30 * 1e9,
			PacketInterval: 1 * 1e9,
			Timeout:        5 * 1e9,
			Count:          3,
		},
		Probes: []types.ProbeConfig{{Name: "lo", Type: "icmp", Targets: []string{"127.0.0.1"}}},
	})

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	// Must contain probe fields.
	if _, ok := body["global"]; !ok {
		t.Error("body should contain 'global' key")
	}
	if _, ok := body["probes"]; !ok {
		t.Error("body should contain 'probes' key")
	}

	// Must not contain ops fields.
	if _, ok := body["exporters"]; ok {
		t.Error("body must not contain 'exporters' key")
	}
	if _, ok := body["server"]; ok {
		t.Error("body must not contain 'server' key")
	}
	if _, ok := body["pipeline"]; ok {
		t.Error("body must not contain 'pipeline' key")
	}
}

func TestMetricsHandlerDelegates(t *testing.T) {
	srv := New(":0")
	const sentinel = "# HELP trimon_build_info Build metadata."
	srv.SetMetricsHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(sentinel))
	}))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), sentinel) {
		t.Errorf("expected sentinel in /metrics body\ngot: %s", rr.Body.String())
	}
}
