package signoz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, url string, retries int) *client {
	t.Helper()
	return &client{
		endpoint:   url,
		retryCount: retries,
		backoff:    time.Millisecond,
		http:       &http.Client{Timeout: 5 * time.Second},
	}
}

func TestClientRetriesServerErrors(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := testClient(t, srv.URL, 3).send(context.Background(), []LogRecord{{Body: "x"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("made %d attempts, want 3", got)
	}
}

func TestClientRetriesRateLimits(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := testClient(t, srv.URL, 3).send(context.Background(), []LogRecord{{Body: "x"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("made %d attempts, want 2", got)
	}
}

// A 4xx means the payload is bad. Retrying it would loop until the deadline and
// still fail, so it must be reported once and dropped.
func TestClientDoesNotRetryClientErrors(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "timestamp must be a uint64 nanoseconds since Unix epoch", http.StatusBadRequest)
	}))
	defer srv.Close()

	err := testClient(t, srv.URL, 3).send(context.Background(), []LogRecord{{Body: "x"}})
	if err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("made %d attempts, want exactly 1 (no retry on 4xx)", got)
	}
}

// v1 accepted only 200; SigNoz Cloud may answer 202.
func TestClientAcceptsAny2xx(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		if err := testClient(t, srv.URL, 0).send(context.Background(), []LogRecord{{Body: "x"}}); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
		srv.Close()
	}
}

func TestClientGivesUpAfterRetryCount(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if err := testClient(t, srv.URL, 2).send(context.Background(), []LogRecord{{Body: "x"}}); err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("made %d attempts, want 3 (initial + 2 retries)", got)
	}
}

func TestClientHonoursContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL, 100)
	c.backoff = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := c.send(ctx, []LogRecord{{Body: "x"}}); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("send took %s; it should stop at the context deadline", elapsed)
	}
}

func TestClientEmptyBatchIsANoOp(t *testing.T) {
	c := testClient(t, "http://127.0.0.1:1", 0)
	if err := c.send(context.Background(), nil); err != nil {
		t.Errorf("empty batch should not be sent: %v", err)
	}
}
