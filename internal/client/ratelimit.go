package client

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

const (
	maxRetries     = 5
	baseBackoff    = 1 * time.Second
	maxBackoff     = 30 * time.Second
	jitterFraction = 0.3
)

// RetryConfig holds configuration for retry behavior.
type RetryConfig struct {
	MaxRetries  int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// DefaultRetryConfig returns sensible defaults for retry behavior.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:  maxRetries,
		BaseBackoff: baseBackoff,
		MaxBackoff:  maxBackoff,
	}
}

// RetryWithBackoff executes fn with exponential backoff and jitter on failure.
// It stops retrying when fn returns nil or the max retries are exhausted.
func RetryWithBackoff(cfg RetryConfig, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		if attempt == cfg.MaxRetries {
			break
		}

		backoff := computeBackoff(attempt, cfg.BaseBackoff, cfg.MaxBackoff)
		fmt.Printf("  Retry %d/%d in %s (error: %v)\n", attempt+1, cfg.MaxRetries, backoff.Round(time.Millisecond), lastErr)
		time.Sleep(backoff)
	}

	return fmt.Errorf("failed after %d retries: %w", cfg.MaxRetries, lastErr)
}

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
