package signoz

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// initialBackoff is the first retry delay; it doubles up to maxBackoff.
const (
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 30 * time.Second
)

// client posts batches of log records to a SigNoz log endpoint.
type client struct {
	endpoint     string
	ingestionKey string
	retryCount   int
	backoff      time.Duration
	http         *http.Client
}

func newClient(cfg *Config) *client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   cfg.Timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   cfg.Timeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if cfg.TLSSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &client{
		endpoint:     cfg.Endpoint,
		ingestionKey: cfg.IngestionKey,
		retryCount:   cfg.RetryCount,
		backoff:      initialBackoff,
		// v1 used http.Post, i.e. http.DefaultClient, which has no timeout at
		// all: one unreachable collector blocked the sender forever while the
		// buffer grew without bound.
		http: &http.Client{Timeout: cfg.Timeout, Transport: transport},
	}
}

// send delivers a batch, retrying transient failures with exponential backoff.
func (c *client) send(ctx context.Context, records []LogRecord) error {
	if len(records) == 0 {
		return nil
	}
	payload, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("encoding %d records: %w", len(records), err)
	}

	backoff := c.backoff
	if backoff <= 0 {
		backoff = initialBackoff
	}
	var lastErr error
	for attempt := 0; attempt <= c.retryCount; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w (giving up after %d attempts)", lastErr, attempt)
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
		}

		retryable, err := c.post(ctx, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
		debug("retrying batch of", len(records), "records after error:", err)
	}
	return fmt.Errorf("%w (giving up after %d attempts)", lastErr, c.retryCount+1)
}

// post makes one attempt. The bool reports whether retrying could help: a 4xx
// means the payload itself is bad, so resending it would just loop forever.
func (c *client) post(ctx context.Context, payload []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.ingestionKey != "" {
		req.Header.Set("signoz-ingestion-key", c.ingestionKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return true, err
	}
	defer func() {
		// Drain before closing so the connection can be reused.
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return true, fmt.Errorf("signoz returned %s", resp.Status)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("signoz rejected the batch with %s: %s", resp.Status, bytes.TrimSpace(body))
	}
}
