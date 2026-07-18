package appinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type HTTPHealthChecker struct {
	client       *http.Client
	pollInterval time.Duration
}

func NewHTTPHealthChecker(client *http.Client, pollInterval time.Duration) *HTTPHealthChecker {
	if client == nil {
		client = http.DefaultClient
	}
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	return &HTTPHealthChecker{client: client, pollInterval: pollInterval}
}

func (h *HTTPHealthChecker) Wait(ctx context.Context, url string) error {
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := h.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned %s", resp.Status)
		} else {
			lastErr = err
		}

		timer := time.NewTimer(h.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}
