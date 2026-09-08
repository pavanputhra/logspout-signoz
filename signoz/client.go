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

// codec is the wire format a client speaks. The HTTP machinery — timeouts,
// retries, backoff, auth — is shared; only encoding and the notion of a
// successful response differ between SigNoz's JSON receiver and OTLP.
type codec interface {
	contentType() string
	encode(records []LogRecord) ([]byte, error)
	// inspect reports a logical failure behind a 2xx response, such as OTLP
	// returning partialSuccess with rejected records.
	inspect(body []byte) error
}

// client posts batches of log records to a log endpoint.
type client struct {
	endpoint     string
	ingestionKey string
	retryCount   int
	backoff      time.Duration
	codec        codec
	http         *http.Client
}

func newClient(cfg *Config, wire codec) *client {
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
		codec:        wire,
		// v1 used http.Post, i.e. http.DefaultClient, which has no timeout at
		// all: one unreachable collector blocked the sender forever while the
		// buffer grew without bound.
		http: &http.Client{Timeout: cfg.Timeout, Transport: transport},
	}
}

// signozCodec is the payload SigNoz's httplogreceiver (source: json) accepts:
// a flat array of log records.
type signozCodec struct{}

func (signozCodec) contentType() string { return "application/json" }

func (signozCodec) encode(records []LogRecord) ([]byte, error) { return json.Marshal(records) }

func (signozCodec) inspect([]byte) error { return nil }

// send delivers a batch, retrying transient failures with exponential backoff.
func (c *client) send(ctx context.Context, records []LogRecord) error {
	if len(records) == 0 {
		return nil
	}
	payload, err := c.codec.encode(records)
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
	req.Header.Set("Content-Type", c.codec.contentType())
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

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// A 2xx is not necessarily a full success: OTLP reports rejected
		// records in the response body.
		return false, c.codec.inspect(body)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return true, fmt.Errorf("collector returned %s", resp.Status)
	default:
		return false, fmt.Errorf("collector rejected the batch with %s: %s", resp.Status, bytes.TrimSpace(body))
	}
}
