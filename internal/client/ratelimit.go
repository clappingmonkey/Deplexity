package client

import (
	"math"
	"math/rand/v2"
	"time"
)

const (
	maxRetries     = 5
	baseBackoff    = 1 * time.Second
	maxBackoff     = 30 * time.Second
	jitterFraction = 0.3
)

// computeBackoff calculates exponential backoff with jitter.
func computeBackoff(attempt int, base, max time.Duration) time.Duration {
	backoff := float64(base) * math.Pow(2, float64(attempt))
	if backoff > float64(max) {
		backoff = float64(max)
	}

	// Add jitter: +/- jitterFraction of the backoff
	jitter := backoff * jitterFraction * (2*rand.Float64() - 1)
	backoff += jitter

	if backoff < 0 {
		backoff = float64(base)
	}

	return time.Duration(backoff)
}
