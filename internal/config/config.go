// Package config loads, validates, and merges trimon's two YAML configuration
// files: the ops config (--config) and the probe config (--probes).
//
// The file is split by concern:
//   - config.go     — entry points (Load/parse) and the merged Config struct
//   - schema.go     — YAML shapes (public config structs + raw decode structs)
//   - validate.go   — global/probe validation and the merge pipeline
//   - protocols.go  — per-protocol (HTTP/TCP/UDP/DNS) config validation
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gtataranni/trimon/pkg/types"
)

// Config is the validated, merged configuration.
type Config struct {
	Global    GlobalConfig
	Exporters ExportersConfig
	Server    ServerConfig
	Pipeline  PipelineConfig
	Probes    []types.ProbeConfig
	// SHA256 is the hex-encoded SHA-256 of the raw probe config file bytes.
	// It tracks the probe file only since that is the only file hot-reloaded.
	SHA256 string
}

// Load reads and validates the ops config at opsPath and the probe config at probePath.
//
// Single-read semantics per file: each file is read exactly once into a byte buffer, then
// parsed and validated without any further disk access. Do not introduce additional reads
// of either path anywhere in this call chain.
func Load(opsPath, probePath string) (*Config, error) {
	opsData, err := os.ReadFile(opsPath)
	if err != nil {
		return nil, fmt.Errorf("reading ops config: %w", err)
	}
	probeData, err := os.ReadFile(probePath)
	if err != nil {
		return nil, fmt.Errorf("reading probe config: %w", err)
	}
	return parse(opsData, probeData)
}

func parse(opsData, probeData []byte) (*Config, error) {
	ops, err := parseOpsFile(opsData)
	if err != nil {
		return nil, err
	}

	global, probes, err := parseProbeFile(probeData)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(probeData)

	return &Config{
		Global:    global,
		Exporters: ops.Exporters,
		Server:    ops.Server,
		Pipeline:  ops.Pipeline,
		Probes:    probes,
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

func parseOpsFile(data []byte) (*rawOpsFile, error) {
	var raw rawOpsFile

	raw.Server.Listen = "127.0.0.1:8080"
	raw.Exporters.Stdout.Format = "json"
	raw.Exporters.OTLP.Protocol = "grpc"
	raw.Exporters.OTLP.Batch.ExportInterval = 30 * time.Second
	raw.Exporters.OTLP.Batch.ExportTimeout = 10 * time.Second
	raw.Exporters.OTLP.Retry.Enabled = true
	raw.Exporters.OTLP.Retry.MaxElapsedTime = 300 * time.Second
	raw.Exporters.OTLP.ShutdownTimeout = 10 * time.Second
	raw.Pipeline.BufferSize = 1000

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing ops config YAML: %w", err)
	}

	if raw.Exporters.Stdout.Format != "json" && raw.Exporters.Stdout.Format != "text" {
		return nil, fmt.Errorf("exporters.stdout.format must be \"json\" or \"text\", got %q", raw.Exporters.Stdout.Format)
	}
	if err := validateOTLP(raw.Exporters.OTLP); err != nil {
		return nil, err
	}
	if raw.Pipeline.BufferSize <= 0 {
		return nil, errors.New("pipeline.buffer_size must be positive")
	}

	return &raw, nil
}

func parseProbeFile(data []byte) (GlobalConfig, []types.ProbeConfig, error) {
	var raw rawProbeFile

	raw.Global.Interval = 30 * time.Second
	raw.Global.PacketInterval = 1 * time.Second
	raw.Global.Timeout = 5 * time.Second
	raw.Global.Count = 3

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return GlobalConfig{}, nil, fmt.Errorf("parsing probe config YAML: %w", err)
	}

	if err := validateGlobal(raw.Global); err != nil {
		return GlobalConfig{}, nil, err
	}

	probes, err := mergeAndValidateProbes(raw.Probes, raw.Global)
	if err != nil {
		return GlobalConfig{}, nil, err
	}

	return raw.Global, probes, nil
}
