//go:build integration

package otlp

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/pkg/types"
)

// fakeMetricsServer is a minimal in-process gRPC OTLP metrics receiver.
// It captures every ExportMetricsServiceRequest it receives.
type fakeMetricsServer struct {
	colmetricspb.UnimplementedMetricsServiceServer

	mu       sync.Mutex
	requests []*colmetricspb.ExportMetricsServiceRequest
}

func (s *fakeMetricsServer) Export(
	_ context.Context,
	req *colmetricspb.ExportMetricsServiceRequest,
) (*colmetricspb.ExportMetricsServiceResponse, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

// received returns a copy of all captured requests (safe for concurrent use).
func (s *fakeMetricsServer) received() []*colmetricspb.ExportMetricsServiceRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*colmetricspb.ExportMetricsServiceRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// startFakeReceiver starts a gRPC server on a random localhost port, registers
// the fake metrics service, and returns the server, its address, and a stop
// function.
func startFakeReceiver(t *testing.T) (*fakeMetricsServer, string, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	srv := grpc.NewServer()
	fake := &fakeMetricsServer{}
	colmetricspb.RegisterMetricsServiceServer(srv, fake)

	go func() {
		if err := srv.Serve(lis); err != nil {
			// Serve returns when the server is stopped; that's expected.
			_ = err
		}
	}()

	addr := lis.Addr().String()
	stop := func() { srv.GracefulStop() }
	return fake, addr, stop
}

// findMetric searches the nested ResourceMetrics → ScopeMetrics → Metrics tree
// for the first Metric with the given name and returns it, or nil.
func findMetric(reqs []*colmetricspb.ExportMetricsServiceRequest, name string) *metricspb.Metric {
	for _, req := range reqs {
		for _, rm := range req.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					if m.GetName() == name {
						return m
					}
				}
			}
		}
	}
	return nil
}

// attrValueFromDataPoint looks up the string value of a named attribute in a
// NumberDataPoint's Attributes slice. Returns ("", false) when absent.
func attrValueFromDataPoint(dp *metricspb.NumberDataPoint, key string) (string, bool) {
	for _, kv := range dp.GetAttributes() {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue(), true
		}
	}
	return "", false
}

// TestIntegration_ExportRTTMean starts an in-process gRPC OTLP receiver,
// creates a real Exporter via New(), exports a single StatusSuccess
// ProbeResult, force-flushes the OTel SDK, and asserts that the receiver
// captured the expected metric name and value.
func TestIntegration_ExportRTTMean(t *testing.T) {
	fake, addr, stop := startFakeReceiver(t)
	defer stop()

	ctx := context.Background()

	cfg := config.OTLPExporterConfig{
		Endpoint: addr,
		Protocol: "grpc",
		Insecure: true,
		Batch: config.OTLPBatchConfig{
			// Use short intervals so ForceFlush pushes quickly in tests.
			ExportInterval: 60 * time.Second,
			ExportTimeout:  10 * time.Second,
		},
	}

	e, err := New(ctx, cfg, "test-version", slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result := types.ProbeResult{
		ProbeName:       "ping-gw",
		ProbeType:       "icmp",
		Target:          "192.168.1.1",
		SourceIP:        "0.0.0.0",
		Status:          types.StatusSuccess,
		RTTMinMS:        5.0,
		RTTMeanMS:       10.5,
		RTTMaxMS:        20.0,
		RTTStddevMS:     2.5,
		PacketsSent:     3,
		PacketsReceived: 3,
		PacketLossRatio: 0.0,
	}

	if err := e.Export(ctx, result); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// ForceFlush pushes the recorded gauges to the in-process receiver.
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := e.provider.ForceFlush(flushCtx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	// Give the gRPC call a moment to land (ForceFlush awaits the export
	// round-trip, but we add a short buffer for scheduler timing).
	time.Sleep(100 * time.Millisecond)

	reqs := fake.received()
	if len(reqs) == 0 {
		t.Fatal("receiver got no requests after ForceFlush")
	}

	// --- assert trimon.probe.rtt.mean ---
	const metricName = "trimon.probe.rtt.mean"
	m := findMetric(reqs, metricName)
	if m == nil {
		t.Fatalf("metric %q not found in received requests", metricName)
	}

	gauge := m.GetGauge()
	if gauge == nil {
		t.Fatalf("metric %q: expected Gauge data, got nil", metricName)
	}
	dps := gauge.GetDataPoints()
	if len(dps) == 0 {
		t.Fatalf("metric %q: no data points", metricName)
	}

	dp := dps[0]
	gotValue := dp.GetAsDouble()
	const wantValue = 10.5
	if gotValue != wantValue {
		t.Errorf("metric %q: value = %f, want %f", metricName, gotValue, wantValue)
	}

	// --- assert probe.name attribute ---
	gotProbeName, ok := attrValueFromDataPoint(dp, "probe.name")
	if !ok {
		t.Fatalf("metric %q: attribute probe.name missing", metricName)
	}
	if gotProbeName != result.ProbeName {
		t.Errorf("probe.name: got %q, want %q", gotProbeName, result.ProbeName)
	}

	// --- assert Close returns no error ---
	if err := e.Close(); err != nil {
		t.Errorf("Close: unexpected error: %v", err)
	}
}
