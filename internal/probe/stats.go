package probe

import (
	"math"
	"time"
)

// RTTStats computes round-trip-time summary statistics over samples, returning
// each value in milliseconds. The standard deviation is the population stddev
// (divided by N), matching the convention used by the ICMP pinger. An empty
// slice yields all zeros.
func RTTStats(samples []time.Duration) (minMS, meanMS, maxMS, stddevMS float64) {
	if len(samples) == 0 {
		return 0, 0, 0, 0
	}

	minRTT, maxRTT := samples[0], samples[0]
	var sum time.Duration
	for _, s := range samples {
		if s < minRTT {
			minRTT = s
		}
		if s > maxRTT {
			maxRTT = s
		}
		sum += s
	}

	mean := float64(sum) / float64(len(samples))

	var variance float64
	for _, s := range samples {
		d := float64(s) - mean
		variance += d * d
	}
	variance /= float64(len(samples))

	const msPerNano = 1.0 / 1e6
	return float64(minRTT) * msPerNano,
		mean * msPerNano,
		float64(maxRTT) * msPerNano,
		math.Sqrt(variance) * msPerNano
}
