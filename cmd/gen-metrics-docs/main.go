// Command gen-metrics-docs regenerates the generated instrument table in
// docs/metrics.md from the exporter's live instrument inventory. It is the
// single writer of that block; the surrounding prose is preserved.
//
// Run it from anywhere in the module tree:
//
//	go run ./cmd/gen-metrics-docs
//
// Running it a second time with no instrument changes is a no-op.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/internal/exporter/otlp"
	"github.com/gtataranni/trimon/internal/metricsdoc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-metrics-docs:", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	docPath := filepath.Join(root, "docs", "metrics.md")

	exp, err := otlp.New(context.Background(), config.OTLPExporterConfig{Enabled: false},
		"0.0.0", "dev", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return fmt.Errorf("build exporter: %w", err)
	}
	defer func() { _ = exp.Close() }()

	table, err := metricsdoc.RenderTable(exp.Instruments())
	if err != nil {
		return fmt.Errorf("render table: %w", err)
	}

	doc, err := os.ReadFile(docPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", docPath, err)
	}
	out, err := metricsdoc.Splice(doc, table)
	if err != nil {
		return fmt.Errorf("splice %s: %w", docPath, err)
	}
	if err := os.WriteFile(docPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", docPath, err)
	}
	return nil
}

// moduleRoot walks up from the working directory to the directory containing
// go.mod, so the generator works regardless of where it is invoked from.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from working directory upward")
		}
		dir = parent
	}
}
