package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"

	"github.com/gtataranni/trimon/internal/config"
)

// healthBufferThreshold is the fraction of pipeline buffer capacity at which /healthz returns 503.
const healthBufferThreshold = 0.9

// Server is the internal HTTP server exposing /healthz, /metrics, /reload, and /config.
type Server struct {
	httpServer     *http.Server
	metricsHandler http.Handler
	logger         *slog.Logger

	reloadMu      sync.Mutex
	reloadFunc    func() (*config.Config, error)
	currentCfg    atomic.Pointer[config.Config]
	healthChecker func() float64
}

// New wires up routes and returns a ready-to-serve Server.
// Call SetMetricsHandler, SetReloadFunc, and UpdateConfig before Start.
func New(listenAddr string) *Server {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("POST /reload", s.handleReload)
	mux.HandleFunc("GET /config", s.handleConfig)

	s.httpServer = &http.Server{Addr: listenAddr, Handler: mux}
	return s
}

// SetMetricsHandler registers the handler that serves GET /metrics.
// Must be called before Start.
func (s *Server) SetMetricsHandler(h http.Handler) { s.metricsHandler = h }

// SetReloadFunc registers the callback invoked by POST /reload.
func (s *Server) SetReloadFunc(fn func() (*config.Config, error)) { s.reloadFunc = fn }

// UpdateConfig replaces the config served by GET /config.
func (s *Server) UpdateConfig(cfg *config.Config) { s.currentCfg.Store(cfg) }

// SetHealthChecker registers a function that returns the current pipeline buffer usage
// as a ratio (0.0–1.0). When usage exceeds healthBufferThreshold, /healthz returns 503.
func (s *Server) SetHealthChecker(fn func() float64) { s.healthChecker = fn }

// SetLogger registers the structured logger used for handler-side errors.
// If never called, the server logs are silently discarded.
func (s *Server) SetLogger(l *slog.Logger) {
	if l != nil {
		s.logger = l
	}
}

// Start binds the listening socket synchronously, then serves in the background.
// Returning an error means the bind failed (e.g. port in use); the caller should treat this as fatal.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("http server bind %s: %w", s.httpServer.Addr, err)
	}
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("http server stopped unexpectedly", "error", err)
		}
	}()
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if s.healthChecker != nil {
		if usage := s.healthChecker(); usage > healthBufferThreshold {
			writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"status":       "degraded",
				"reason":       "results buffer near capacity",
				"buffer_usage": usage,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metricsHandler != nil {
		s.metricsHandler.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleReload(w http.ResponseWriter, _ *http.Request) {
	if s.reloadFunc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reload not configured"})
		return
	}

	// Serialise concurrent reload requests — don't apply two configs simultaneously.
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	newCfg, err := s.reloadFunc()
	if err != nil {
		// Real error stays in the server log; the client gets a generic
		// message so we don't leak filesystem paths, DNS resolver output,
		// or possibly sensitive error messages.
		s.logger.Error("config reload failed", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"reloaded": false,
			"error":    "configuration invalid; see server logs",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"reloaded": true,
		"probes":   len(newCfg.Probes),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentCfg.Load()
	if cfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "config not yet loaded"})
		return
	}

	dump := toConfigDump(cfg)

	if r.Header.Get("Accept") == "application/x-yaml" {
		b, err := yaml.Marshal(dump)
		if err != nil {
			// Don't echo the marshal error — it could leak struct internals.
			s.logger.Error("config yaml marshal failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // best-effort write to client; headers already sent, nothing to do on failure
		w.Write(b)
		return
	}

	writeJSON(w, http.StatusOK, dump)
}

// ── config dump DTO ───────────────────────────────────────────────────────────
//
// Durations are serialised as strings ("10s") so the output is human-readable and
// mirrors probe config syntax. The dump is the *merged effective* probe config, so it
// carries both `global:` and `probes:` in one document — split it across `_global.yaml`
// and a fragment to feed it back in as a probe config directory (see ADR-0008).
// Ops fields (exporters, server, pipeline) are intentionally omitted.

type configDump struct {
	Global globalDump  `json:"global" yaml:"global"`
	Probes []probeDump `json:"probes" yaml:"probes"`
}

type globalDump struct {
	Interval       string `json:"probe_every" yaml:"probe_every"`
	PacketInterval string `json:"packet_interval" yaml:"packet_interval"`
	Timeout        string `json:"timeout" yaml:"timeout"`
	Count          int    `json:"count" yaml:"count"`
	SourceIP       string `json:"source_ip,omitempty" yaml:"source_ip,omitempty"`
}

type probeDump struct {
	Name           string            `json:"name" yaml:"name"`
	Type           string            `json:"type" yaml:"type"`
	Targets        []string          `json:"targets" yaml:"targets"`
	MaxResolvedIPs int               `json:"max_resolved_ips,omitempty" yaml:"max_resolved_ips,omitempty"`
	SourceIP       string            `json:"source_ip" yaml:"source_ip"`
	Interval       string            `json:"probe_every" yaml:"probe_every"`
	PacketInterval string            `json:"packet_interval" yaml:"packet_interval"`
	Timeout        string            `json:"timeout" yaml:"timeout"`
	Count          int               `json:"count" yaml:"count"`
	Labels         map[string]string `json:"labels" yaml:"labels"`
}

func toConfigDump(cfg *config.Config) configDump {
	probes := make([]probeDump, len(cfg.Probes))
	for i, p := range cfg.Probes {
		labels := p.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		probes[i] = probeDump{
			Name:           p.Name,
			Type:           p.Type,
			Targets:        p.Targets,
			MaxResolvedIPs: p.MaxResolvedIPs,
			SourceIP:       p.SourceIP,
			Interval:       p.Interval.String(),
			PacketInterval: p.PacketInterval.String(),
			Timeout:        p.Timeout.String(),
			Count:          p.Count,
			Labels:         labels,
		}
	}
	return configDump{
		Global: globalDump{
			Interval:       cfg.Global.Interval.String(),
			PacketInterval: cfg.Global.PacketInterval.String(),
			Timeout:        cfg.Global.Timeout.String(),
			Count:          cfg.Global.Count,
			SourceIP:       cfg.Global.SourceIP,
		},
		Probes: probes,
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	//nolint:errcheck // best-effort write to client; headers already sent, nothing to do on failure
	enc.Encode(v)
}
