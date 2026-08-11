// Package config loads, validates, and merges trimon's two YAML configuration
// inputs: the ops config file (--config) and the probe config directory (--probes).
//
// The file is split by concern:
//   - config.go     — entry points (Load/parseSources) and the merged Config struct
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
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gtataranni/trimon/pkg/types"
)

// globalFileName is the reserved file inside a probe config directory that may
// carry the `global:` key. No other file in the directory may define globals,
// and this file must not define probes.
const globalFileName = "_global.yaml"

// probeSource is one probe config document plus the file name it came from.
type probeSource struct {
	name string
	data []byte
}

// Config is the validated, merged configuration.
type Config struct {
	Global    GlobalConfig
	Exporters ExportersConfig
	Server    ServerConfig
	Pipeline  PipelineConfig
	Probes    []types.ProbeConfig
	// ProbeFiles lists the probe config file names that were merged, in load order.
	ProbeFiles []string
	// SkippedFiles lists the directory entries that were not merged: dotfiles,
	// subdirectories and non-*.yaml extensions.
	SkippedFiles []string
	// SHA256 fingerprints the probe config: it covers the (name, content) pairs of
	// every merged file, so adds, removes and renames all change it. It tracks the
	// probe config only since that is the only part hot-reloaded.
	SHA256 string
}

// Load reads and validates the ops config file at opsPath and the probe config
// directory at probesDir, whose *.yaml files are merged into one probe set.
//
// Single-read semantics per file: each file is read exactly once into a byte buffer, then
// parsed and validated without any further disk access. Do not introduce additional reads
// of either path anywhere in this call chain.
func Load(opsPath, probesDir string) (*Config, error) {
	opsData, err := os.ReadFile(opsPath)
	if err != nil {
		return nil, fmt.Errorf("reading ops config: %w", err)
	}

	sources, skipped, err := readProbeDir(probesDir)
	if err != nil {
		return nil, err
	}

	cfg, err := parseSources(opsData, sources)
	if err != nil {
		return nil, err
	}
	cfg.ProbeFiles = make([]string, len(sources))
	for i, s := range sources {
		cfg.ProbeFiles[i] = s.name
	}
	cfg.SkippedFiles = skipped
	return cfg, nil
}

// readProbeDir reads every *.yaml file directly inside dir, in lexical order, and
// returns the skipped entries (dotfiles, subdirectories, other extensions) alongside
// them so callers can log what was ignored. An empty result is an error.
func readProbeDir(dir string) (sources []probeSource, skipped []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading probe config dir %s: %w", dir, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".yaml" {
			skipped = append(skipped, name)
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			return nil, nil, fmt.Errorf("reading probe config file %s: %w", filepath.Join(dir, name), readErr)
		}
		sources = append(sources, probeSource{name: name, data: data})
	}
	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("reading probe config dir %s: no *.yaml files found", dir)
	}
	return sources, skipped, nil
}

func parseSources(opsData []byte, sources []probeSource) (*Config, error) {
	ops, err := parseOpsFile(opsData)
	if err != nil {
		return nil, err
	}

	global, probes, err := parseProbeSources(sources)
	if err != nil {
		return nil, err
	}

	return &Config{
		Global:    global,
		Exporters: ops.Exporters,
		Server:    ops.Server,
		Pipeline:  ops.Pipeline,
		Probes:    probes,
		SHA256:    fingerprint(sources),
	}, nil
}

// fingerprint hashes the (name, content) pairs of the probe config sources, so that
// adding, removing or renaming a file changes the result.
func fingerprint(sources []probeSource) string {
	h := sha256.New()
	for _, s := range sources {
		h.Write([]byte(s.name))
		h.Write([]byte{0})
		h.Write(s.data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
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

// defaultGlobal returns the built-in probe defaults applied before any YAML is read.
func defaultGlobal() GlobalConfig {
	return GlobalConfig{
		Interval:       30 * time.Second,
		PacketInterval: 1 * time.Second,
		Timeout:        5 * time.Second,
		Count:          3,
	}
}

// parseProbeSources merges the probe sources into one validated probe set.
// `global:` is only accepted in _global.yaml, which in turn must not declare probes;
// probe names must be unique across all files.
func parseProbeSources(sources []probeSource) (GlobalConfig, []types.ProbeConfig, error) {
	global := defaultGlobal()
	type fragment struct {
		file string
		raws []rawProbeConfig
	}
	var fragments []fragment

	for _, s := range sources {
		if s.name == globalFileName {
			raw := rawProbeFile{Global: global}
			if err := yaml.Unmarshal(s.data, &raw); err != nil {
				return GlobalConfig{}, nil, fmt.Errorf("parsing probe config YAML in %s: %w", s.name, err)
			}
			if raw.Probes != nil {
				return GlobalConfig{}, nil, fmt.Errorf("%s must not contain a probes: key", s.name)
			}
			global = raw.Global
			continue
		}

		var frag rawProbeFragment
		if err := yaml.Unmarshal(s.data, &frag); err != nil {
			return GlobalConfig{}, nil, fmt.Errorf("parsing probe config YAML in %s: %w", s.name, err)
		}
		if frag.Global != nil {
			return GlobalConfig{}, nil, fmt.Errorf("%s: global: is only allowed in %s", s.name, globalFileName)
		}
		fragments = append(fragments, fragment{file: s.name, raws: frag.Probes})
	}

	if err := validateGlobal(global); err != nil {
		return GlobalConfig{}, nil, err
	}

	origin := make(map[string]string)
	for _, f := range fragments {
		for _, r := range f.raws {
			if r.Name == "" {
				continue // reported by mergeAndValidateProbes with its index
			}
			// Same-file duplicates are left to mergeAndValidateProbes, which words them better.
			if prev, ok := origin[r.Name]; ok && prev != f.file {
				return GlobalConfig{}, nil, fmt.Errorf("probe name %q defined in both %s and %s", r.Name, prev, f.file)
			}
			origin[r.Name] = f.file
		}
	}

	var probes []types.ProbeConfig
	for _, f := range fragments {
		pcs, err := mergeAndValidateProbes(f.raws, global)
		if err != nil {
			return GlobalConfig{}, nil, fmt.Errorf("%s: %w", f.file, err)
		}
		probes = append(probes, pcs...)
	}

	return global, probes, nil
}
