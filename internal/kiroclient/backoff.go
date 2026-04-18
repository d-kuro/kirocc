package kiroclient

import (
	"context"
	"io"
	"math/rand/v2"
	"time"
)

const upstreamBodyLimit = 8192

// backoffDelay returns exponential backoff delay with ±25% jitter.
func backoffDelay(attempt int) time.Duration {
	base := baseRetryDelay << attempt
	jitter := time.Duration(rand.Int64N(int64(base)/2)) - base/4
	return base + jitter
}

// readLimitedBody reads up to n bytes from body and closes it.
func readLimitedBody(body io.ReadCloser, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(body, n))
	_ = body.Close()
	return string(b)
}

// retryWait waits for the given delay, respecting ctx cancellation.
func retryWait(ctx context.Context, delay time.Duration) error {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
