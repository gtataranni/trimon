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
// A single source with an empty name means single-file mode (--probes points at
// a plain file): globals and probes may share the document and the fingerprint
// is the raw file bytes, preserving pre-directory behaviour.
type probeSource struct {
	name string
	data []byte
}

func (s probeSource) isSingleFile() bool { return s.name == "" }

// Config is the validated, merged configuration.
type Config struct {
	Global    GlobalConfig
	Exporters ExportersConfig
	Server    ServerConfig
	Pipeline  PipelineConfig
	Probes    []types.ProbeConfig
	// ProbeFiles lists the probe config file names that were merged, in load order.
	ProbeFiles []string
	// SHA256 fingerprints the probe config. In single-file mode it is the SHA-256
	// of the raw file bytes; in directory mode it covers the (name, content) pairs
	// of every merged file, so adds, removes and renames all change it.
	// It tracks the probe config only since that is the only part hot-reloaded.
	SHA256 string
}

// Load reads and validates the ops config at opsPath and the probe config at probePath.
// probePath may be a plain file or a directory of *.yaml probe fragments that are merged.
//
// Single-read semantics per file: each file is read exactly once into a byte buffer, then
// parsed and validated without any further disk access. Do not introduce additional reads
// of either path anywhere in this call chain.
func Load(opsPath, probePath string) (*Config, error) {
	opsData, err := os.ReadFile(opsPath)
	if err != nil {
		return nil, fmt.Errorf("reading ops config: %w", err)
	}

	info, err := os.Stat(probePath)
	if err != nil {
		return nil, fmt.Errorf("reading probe config: %w", err)
	}

	var sources []probeSource
	probeFiles := []string{filepath.Base(probePath)}
	if info.IsDir() {
		if sources, err = readProbeDir(probePath); err != nil {
			return nil, err
		}
		probeFiles = make([]string, len(sources))
		for i, s := range sources {
			probeFiles[i] = s.name
		}
	} else {
		probeData, readErr := os.ReadFile(probePath)
		if readErr != nil {
			return nil, fmt.Errorf("reading probe config: %w", readErr)
		}
		sources = []probeSource{{data: probeData}}
	}

	cfg, err := parseSources(opsData, sources)
	if err != nil {
		return nil, err
	}
	cfg.ProbeFiles = probeFiles
	return cfg, nil
}

// readProbeDir reads every *.yaml file directly inside dir, in lexical order.
// Dotfiles, subdirectories and other extensions are skipped; an empty result is an error.
func readProbeDir(dir string) ([]probeSource, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading probe config dir %s: %w", dir, err)
	}

	var sources []probeSource
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".yaml" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			return nil, fmt.Errorf("reading probe config dir %s: %w", dir, readErr)
		}
		sources = append(sources, probeSource{name: name, data: data})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("reading probe config dir %s: no *.yaml files found", dir)
	}
	return sources, nil
}

// parse parses a single-file probe config. It is the single-file entry point kept
// for callers (and tests) that hold the probe document in memory.
func parse(opsData, probeData []byte) (*Config, error) {
	return parseSources(opsData, []probeSource{{data: probeData}})
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

// fingerprint hashes the probe config sources. Single-file mode hashes the raw bytes;
// directory mode hashes the (name, content) pairs so renames are visible.
func fingerprint(sources []probeSource) string {
	if len(sources) == 1 && sources[0].isSingleFile() {
		sum := sha256.Sum256(sources[0].data)
		return hex.EncodeToString(sum[:])
	}
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
// In directory mode `global:` is only accepted in _global.yaml, which in turn must
// not declare probes; probe names must be unique across all files.
func parseProbeSources(sources []probeSource) (GlobalConfig, []types.ProbeConfig, error) {
	if len(sources) == 1 && sources[0].isSingleFile() {
		raw := rawProbeFile{Global: defaultGlobal()}
		if err := yaml.Unmarshal(sources[0].data, &raw); err != nil {
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
			if prev, ok := origin[r.Name]; ok {
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
