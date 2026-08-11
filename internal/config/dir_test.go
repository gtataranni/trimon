package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func src(name, data string) probeSource {
	return probeSource{name: name, data: []byte(data)}
}

func TestParseSourcesMergesFragments(t *testing.T) {
	tests := []struct {
		name    string
		sources []probeSource
		want    []string // probe names, in order
	}{
		{
			name: "two fragments merged",
			sources: []probeSource{
				src("a.yaml", "probes:\n  - name: a1\n    type: icmp\n    targets: [\"127.0.0.1\"]\n"),
				src("b.yaml", "probes:\n  - name: b1\n    type: icmp\n    targets: [\"127.0.0.2\"]\n"),
			},
			want: []string{"a1", "b1"},
		},
		{
			name: "fragment without probes key",
			sources: []probeSource{
				src("a.yaml", "probes:\n  - name: a1\n    type: icmp\n    targets: [\"127.0.0.1\"]\n"),
				src("empty.yaml", "# nothing here\n"),
			},
			want: []string{"a1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseSources([]byte(minValidOpsYAML), tt.sources)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.Probes) != len(tt.want) {
				t.Fatalf("want %d probes, got %d", len(tt.want), len(cfg.Probes))
			}
			for i, name := range tt.want {
				if cfg.Probes[i].Name != name {
					t.Errorf("probe[%d]: want %q, got %q", i, name, cfg.Probes[i].Name)
				}
			}
		})
	}
}

func TestParseSourcesGlobalFile(t *testing.T) {
	sources := []probeSource{
		src(globalFileName, "global:\n  probe_every: 10s\n  timeout: 4s\n  count: 2\n"),
		src("edge.yaml", "probes:\n  - name: e1\n    type: icmp\n    targets: [\"127.0.0.1\"]\n"),
	}
	cfg, err := parseSources([]byte(minValidOpsYAML), sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.Interval != 10*time.Second {
		t.Errorf("global probe_every: want 10s, got %v", cfg.Global.Interval)
	}
	if cfg.Probes[0].Interval != 10*time.Second {
		t.Errorf("probe interval: want 10s, got %v", cfg.Probes[0].Interval)
	}
	if cfg.Probes[0].Count != 2 {
		t.Errorf("probe count: want 2, got %d", cfg.Probes[0].Count)
	}
	// packet_interval is absent from _global.yaml, so the built-in default must survive.
	if cfg.Global.PacketInterval != 1*time.Second {
		t.Errorf("global packet_interval: want 1s, got %v", cfg.Global.PacketInterval)
	}
	if cfg.Probes[0].PacketInterval != 1*time.Second {
		t.Errorf("probe packet_interval: want 1s, got %v", cfg.Probes[0].PacketInterval)
	}
}

func TestParseSourcesDefaultsWithoutGlobalFile(t *testing.T) {
	cfg, err := parseSources([]byte(minValidOpsYAML), []probeSource{
		src("a.yaml", "probes:\n  - name: a1\n    type: icmp\n    targets: [\"127.0.0.1\"]\n"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Global.Interval != 30*time.Second || cfg.Global.Count != 3 {
		t.Errorf("want built-in defaults, got %+v", cfg.Global)
	}
	if cfg.Probes[0].Timeout != 5*time.Second {
		t.Errorf("probe timeout: want 5s, got %v", cfg.Probes[0].Timeout)
	}
}

func TestParseSourcesErrors(t *testing.T) {
	tests := []struct {
		name    string
		sources []probeSource
		wantErr string
	}{
		{
			name: "probes in _global.yaml",
			sources: []probeSource{
				src(globalFileName, "global:\n  probe_every: 10s\nprobes: []\n"),
			},
			wantErr: "must not contain a probes: key",
		},
		{
			name: "global in fragment",
			sources: []probeSource{
				src("edge.yaml", "global:\n  probe_every: 10s\nprobes: []\n"),
			},
			wantErr: "global: is only allowed in _global.yaml",
		},
		{
			name: "duplicate probe name across files",
			sources: []probeSource{
				src("a.yaml", "probes:\n  - name: dup\n    type: icmp\n    targets: [\"127.0.0.1\"]\n"),
				src("b.yaml", "probes:\n  - name: dup\n    type: icmp\n    targets: [\"127.0.0.2\"]\n"),
			},
			wantErr: `probe name "dup" defined in both a.yaml and b.yaml`,
		},
		{
			name: "duplicate probe name within one file",
			sources: []probeSource{
				src("a.yaml", "probes:\n  - name: dup\n    type: icmp\n    targets: [\"127.0.0.1\"]\n  - name: dup\n    type: icmp\n    targets: [\"127.0.0.2\"]\n"),
			},
			wantErr: `a.yaml: probe name "dup" is not unique`,
		},
		{
			name: "invalid probe names its file",
			sources: []probeSource{
				src("edge.yaml", "probes:\n  - name: bad\n    type: nope\n    targets: [\"127.0.0.1\"]\n"),
			},
			wantErr: "edge.yaml:",
		},
		{
			name: "invalid global timings",
			sources: []probeSource{
				src(globalFileName, "global:\n  probe_every: 1s\n  timeout: 5s\n"),
				src("edge.yaml", "probes: []\n"),
			},
			wantErr: "global.timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSources([]byte(minValidOpsYAML), tt.sources)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	a := src("a.yaml", "probes: []\n")
	b := src("b.yaml", "probes: []\n")

	base := fingerprint([]probeSource{a, b})
	if base != fingerprint([]probeSource{a, b}) {
		t.Error("fingerprint is not stable")
	}
	if base == fingerprint([]probeSource{a}) {
		t.Error("removing a file must change the fingerprint")
	}
	if base == fingerprint([]probeSource{a, b, src("c.yaml", "probes: []\n")}) {
		t.Error("adding a file must change the fingerprint")
	}
	if base == fingerprint([]probeSource{a, src("renamed.yaml", string(b.data))}) {
		t.Error("renaming a file must change the fingerprint")
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestReadProbeDir(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"b.yaml":          "probes: []\n",
		"a.yaml":          "probes: []\n",
		"ignored.yml":     "probes: []\n",
		"ignored.txt":     "nope\n",
		".hidden.yaml":    "probes: []\n",
		"sub/nested.yaml": "probes: []\n",
	})

	sources, err := readProbeDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := make([]string, len(sources))
	for i, s := range sources {
		got[i] = s.name
	}
	want := []string{"a.yaml", "b.yaml"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestReadProbeDirEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := readProbeDir(dir); err == nil {
		t.Fatal("expected error for empty dir, got nil")
	}
	writeFiles(t, dir, map[string]string{"only.txt": "nope\n"})
	if _, err := readProbeDir(dir); err == nil {
		t.Fatal("expected error for dir with no *.yaml files, got nil")
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	opsPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(opsPath, []byte(minValidOpsYAML), 0o644); err != nil {
		t.Fatalf("write ops: %v", err)
	}

	probeDir := filepath.Join(dir, "probes.d")
	writeFiles(t, probeDir, map[string]string{
		globalFileName: "global:\n  probe_every: 20s\n  timeout: 4s\n  count: 2\n",
		"edge.yaml":    "probes:\n  - name: e1\n    type: icmp\n    targets: [\"127.0.0.1\"]\n",
		"core.yaml":    "probes:\n  - name: c1\n    type: icmp\n    targets: [\"127.0.0.2\"]\n",
	})

	cfg, err := Load(opsPath, probeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Probes) != 2 {
		t.Fatalf("want 2 probes, got %d", len(cfg.Probes))
	}
	if cfg.Global.Interval != 20*time.Second {
		t.Errorf("global probe_every: want 20s, got %v", cfg.Global.Interval)
	}
}

func TestLoadMissingProbePath(t *testing.T) {
	dir := t.TempDir()
	opsPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(opsPath, []byte(minValidOpsYAML), 0o644); err != nil {
		t.Fatalf("write ops: %v", err)
	}
	if _, err := Load(opsPath, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("expected error for missing probe path, got nil")
	}
}
