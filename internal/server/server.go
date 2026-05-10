package server

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/pkg/types"
)

// Metrics holds all self-observability Prometheus instruments.
type Metrics struct {
	BuildInfo           *prometheus.GaugeVec
	ProbeRunsTotal      *prometheus.CounterVec
	ProbeErrorsTotal    *prometheus.CounterVec
	SchedulerGoroutines prometheus.Gauge
	ConfigReloadTotal   prometheus.Counter
}

// Server is the internal HTTP server exposing /healthz, /metrics, /reload, and /config.
type Server struct {
	httpServer *http.Server
	metrics    *Metrics
	reg        *prometheus.Registry
	scheduler  GoroutineGetter

	reloadMu   sync.Mutex
	reloadFunc func() (*config.Config, error)
	currentCfg atomic.Pointer[config.Config]
}

// GoroutineGetter is implemented by the Scheduler to report live worker count.
type GoroutineGetter interface {
	WorkerCount() int
}

// New registers metrics, wires up routes, and returns a ready-to-serve Server.
// Call SetScheduler, SetReloadFunc, and UpdateConfig before Start.
func New(listenAddr string, version, commit string) *Server {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "trimon_build_info",
			Help: "Build metadata.",
		}, []string{"version", "commit", "goversion"}),

		ProbeRunsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trimon_probe_runs_total",
			Help: "Total probe runs, partitioned by probe_name and status.",
		}, []string{"probe_name", "status"}),

		ProbeErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trimon_probe_errors_total",
			Help: "Total probe errors, partitioned by probe_name and error_type.",
		}, []string{"probe_name", "error_type"}),

		SchedulerGoroutines: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "trimon_scheduler_goroutines",
			Help: "Active probe goroutines managed by the scheduler.",
		}),

		ConfigReloadTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "trimon_config_reload_total",
			Help: "Total successful config reloads.",
		}),
	}

	reg.MustRegister(
		m.BuildInfo,
		m.ProbeRunsTotal,
		m.ProbeErrorsTotal,
		m.SchedulerGoroutines,
		m.ConfigReloadTotal,
	)
	m.BuildInfo.WithLabelValues(version, commit, runtime.Version()).Set(1)

	s := &Server{metrics: m, reg: reg}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("POST /reload", s.handleReload)
	mux.HandleFunc("GET /config", s.handleConfig)

	s.httpServer = &http.Server{Addr: listenAddr, Handler: mux}
	return s
}

// SetScheduler wires a scheduler for goroutine-count reporting on /metrics.
func (s *Server) SetScheduler(g GoroutineGetter) { s.scheduler = g }

// SetReloadFunc registers the callback invoked by POST /reload.
func (s *Server) SetReloadFunc(fn func() (*config.Config, error)) { s.reloadFunc = fn }

// UpdateConfig replaces the config served by GET /config.
func (s *Server) UpdateConfig(cfg *config.Config) { s.currentCfg.Store(cfg) }

// Metrics returns the instruments so callers can increment counters.
func (s *Server) Metrics() *Metrics { return s.metrics }

// Start begins listening in the background.
func (s *Server) Start() error {
	go func() { _ = s.httpServer.ListenAndServe() }()
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// RecordResult increments the probe run and error counters for r.
func (s *Server) RecordResult(r types.ProbeResult) {
	s.metrics.ProbeRunsTotal.WithLabelValues(r.ProbeName, string(r.Status)).Inc()
	if r.Status == types.StatusError {
		s.metrics.ProbeErrorsTotal.WithLabelValues(r.ProbeName, "probe_error").Inc()
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.scheduler != nil {
		s.metrics.SchedulerGoroutines.Set(float64(s.scheduler.WorkerCount()))
	}
	promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}).ServeHTTP(w, r)
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
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"reloaded": false,
			"error":    err.Error(),
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return
	}

	writeJSON(w, http.StatusOK, dump)
}

// ── config dump DTO ───────────────────────────────────────────────────────────
//
// Durations are serialised as strings ("10s") so the output is human-readable
// and round-trips back to a valid config file.

type configDump struct {
	Global    globalDump    `json:"global" yaml:"global"`
	Exporters exportersDump `json:"exporters" yaml:"exporters"`
	Server    serverDump    `json:"server" yaml:"server"`
	Probes    []probeDump   `json:"probes" yaml:"probes"`
}

type globalDump struct {
	Interval string `json:"interval" yaml:"interval"`
	Timeout  string `json:"timeout" yaml:"timeout"`
	Count    int    `json:"count" yaml:"count"`
	SourceIP string `json:"source_ip,omitempty" yaml:"source_ip,omitempty"`
}

type exportersDump struct {
	Stdout stdoutDump `json:"stdout" yaml:"stdout"`
}

type stdoutDump struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Format  string `json:"format" yaml:"format"`
}

type serverDump struct {
	Listen string `json:"listen" yaml:"listen"`
}

type probeDump struct {
	Name     string            `json:"name" yaml:"name"`
	Type     string            `json:"type" yaml:"type"`
	Target   string            `json:"target" yaml:"target"`
	SourceIP string            `json:"source_ip" yaml:"source_ip"`
	Interval string            `json:"interval" yaml:"interval"`
	Timeout  string            `json:"timeout" yaml:"timeout"`
	Count    int               `json:"count" yaml:"count"`
	Labels   map[string]string `json:"labels" yaml:"labels"`
}

func toConfigDump(cfg *config.Config) configDump {
	probes := make([]probeDump, len(cfg.Probes))
	for i, p := range cfg.Probes {
		labels := p.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		probes[i] = probeDump{
			Name:     p.Name,
			Type:     p.Type,
			Target:   p.Target,
			SourceIP: p.SourceIP,
			Interval: p.Interval.String(),
			Timeout:  p.Timeout.String(),
			Count:    p.Count,
			Labels:   labels,
		}
	}
	return configDump{
		Global: globalDump{
			Interval: cfg.Global.Interval.String(),
			Timeout:  cfg.Global.Timeout.String(),
			Count:    cfg.Global.Count,
			SourceIP: cfg.Global.SourceIP,
		},
		Exporters: exportersDump{
			Stdout: stdoutDump{
				Enabled: cfg.Exporters.Stdout.Enabled,
				Format:  cfg.Exporters.Stdout.Format,
			},
		},
		Server: serverDump{Listen: cfg.Server.Listen},
		Probes: probes,
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
