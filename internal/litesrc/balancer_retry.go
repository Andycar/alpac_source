package litesrc

// Verbatim mirror of httpapi/balancer_retry.go (shared-drawer copy: the
// original stays for the in-monolith sources).

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// balancerDoWithRetry wraps client.Do with retry on transient CDN errors
// (502, 503, 504, connection reset). It drains and closes the failed response
// body before each retry.
//
// maxRetries is the number of *additional* attempts (0 = no retries, just one try).
// Delays between retries: 200ms, 500ms, 1s.
func balancerDoWithRetry(ctx context.Context, client *http.Client, req *http.Request, maxRetries int) (*http.Response, error) {
	delays := [3]time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			d := delays[0]
			if attempt-1 < len(delays) {
				d = delays[attempt-1]
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
			// Re-create body for retry if possible (nil body = GET).
			if req.Body != nil && req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				req.Body = body
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			if isRetryableNetError(err) && attempt < maxRetries {
				log.Debug().Err(err).Int("attempt", attempt+1).Str("url", req.URL.String()).Msg("balancer: retryable net error")
				lastErr = err
				continue
			}
			return nil, err
		}

		if isRetryableBalancerStatus(resp.StatusCode) && attempt < maxRetries {
			log.Debug().Int("status", resp.StatusCode).Int("attempt", attempt+1).Str("url", req.URL.String()).Msg("balancer: retryable status")
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			lastErr = &retryableStatusErr{code: resp.StatusCode}
			continue
		}

		return resp, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, context.DeadlineExceeded
}

func isRetryableBalancerStatus(code int) bool {
	return code == 502 || code == 503 || code == 504
}

func isRetryableNetError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if asOpErr(err, &opErr) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "i/o timeout")
}

// asOpErr walks the error chain to find *net.OpError.
func asOpErr(err error, target **net.OpError) bool {
	for err != nil {
		if e, ok := err.(*net.OpError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

type retryableStatusErr struct {
	code int
}

func (e *retryableStatusErr) Error() string {
	return "retryable HTTP status: " + http.StatusText(e.code)
}
