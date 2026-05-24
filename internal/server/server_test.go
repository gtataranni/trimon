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
