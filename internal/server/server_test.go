package server

import (
	"bytes"
	"encoding/json"
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

func TestRecordResult(t *testing.T) {
	srv := newTestServer()
	r := types.ProbeResult{ProbeName: "test", Status: types.StatusSuccess}
	srv.RecordResult(r) // must not panic

	r2 := types.ProbeResult{ProbeName: "test", Status: types.StatusError}
	srv.RecordResult(r2)
}

