package smoke

import (
	"math"
	"strconv"
	"strings"
)

// Prometheus text-exposition parsing helpers, shared by the smoke assertions.
// These are tag-free (no `smoke` build tag) so they compile and are unit-tested
// as part of `go test ./...`; only the network-touching tests in smoke_test.go
// carry the tag.

// sample is one parsed Prometheus text series: a metric name, its label set, and
// its value (NaN when the line reads "NaN").
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// parsePromText parses Prometheus text exposition into samples. It is a smoke
// helper, not a spec-complete parser: trimon's label values contain no commas,
// quotes, or escapes, so a direct split is sufficient and dependency-free.
func parsePromText(body string) []sample {
	var out []sample
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name := line
		labels := map[string]string{}
		rest := ""

		if i := strings.IndexByte(line, '{'); i >= 0 {
			j := strings.IndexByte(line, '}')
			if j < i {
				continue
			}
			name = line[:i]
			labels = parseLabels(line[i+1 : j])
			rest = strings.TrimSpace(line[j+1:])
		} else if i := strings.IndexByte(line, ' '); i >= 0 {
			name = line[:i]
			rest = strings.TrimSpace(line[i+1:])
		}

		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		out = append(out, sample{name: name, labels: labels, value: parseValue(fields[0])})
	}
	return out
}

func parseLabels(s string) map[string]string {
	labels := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		labels[kv[:eq]] = strings.Trim(kv[eq+1:], `"`)
	}
	return labels
}

// parseValue parses a Prometheus value token. strconv.ParseFloat natively
// handles "NaN", "+Inf", and "-Inf"; anything unparseable degrades to NaN.
func parseValue(tok string) float64 {
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// ofType returns the samples named `name` carrying probe_type == typ.
func ofType(samples []sample, name, typ string) []sample {
	var out []sample
	for _, s := range samples {
		if s.name == name && s.labels["probe_type"] == typ {
			out = append(out, s)
		}
	}
	return out
}

// anyValue reports whether any sample has the exact value v.
func anyValue(samples []sample, v float64) bool {
	for _, s := range samples {
		if s.value == v {
			return true
		}
	}
	return false
}

// anyNonNaN reports whether any sample has a non-NaN value.
func anyNonNaN(samples []sample) bool {
	for _, s := range samples {
		if !math.IsNaN(s.value) {
			return true
		}
	}
	return false
}
