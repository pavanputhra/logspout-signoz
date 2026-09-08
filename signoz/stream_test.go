package signoz

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gliderlabs/logspout/router"
)

// collector is a stand-in for SigNoz's httplogreceiver.
type collector struct {
	*httptest.Server
	mu       sync.Mutex
	batches  [][]map[string]interface{}
	requests []*http.Request
	status   atomic.Int32
	hold     chan struct{}
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.status.Store(http.StatusOK)
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.hold != nil {
			<-c.hold
		}
		body, _ := io.ReadAll(r.Body)
		var batch []map[string]interface{}
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Errorf("collector got invalid JSON: %v", err)
		}
		c.mu.Lock()
		c.batches = append(c.batches, batch)
		c.requests = append(c.requests, r)
		c.mu.Unlock()
		w.WriteHeader(int(c.status.Load()))
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) received() [][]map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]map[string]interface{}{}, c.batches...)
}

func (c *collector) records() []map[string]interface{} {
	var all []map[string]interface{}
	for _, batch := range c.received() {
		all = append(all, batch...)
	}
	return all
}

func (c *collector) lastRequest() *http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return nil
	}
	return c.requests[len(c.requests)-1]
}

func adapterFor(t *testing.T, c *collector, options map[string]string) *Adapter {
	t.Helper()
	clearLegacyEnv(t)
	if options == nil {
		options = map[string]string{}
	}
	adapter, err := NewSignozAdapter(route(strings.TrimPrefix(c.URL, "http://"), options))
	if err != nil {
		t.Fatalf("NewSignozAdapter: %v", err)
	}
	return adapter.(*Adapter)
}

// runStream feeds messages, closes the stream, and waits for Stream to return.
// Stream drains its sender before returning, so no sleeps are needed.
func runStream(t *testing.T, a *Adapter, messages ...*router.Message) {
	t.Helper()
	logstream := make(chan *router.Message)
	done := make(chan struct{})
	go func() {
		a.Stream(logstream)
		close(done)
	}()
	for _, m := range messages {
		logstream <- m
	}
	close(logstream)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Stream did not return after the log stream closed")
	}
}

// v1 returned from Stream without flushing, so whatever sat in the buffer when
// a route closed was lost, and its ticker goroutine outlived the route.
func TestStreamFlushesOnClose(t *testing.T) {
	c := newCollector(t)
	a := adapterFor(t, c, map[string]string{"batch_size": "1000", "flush_interval": "1h"})

	runStream(t, a, message("one"), message("two"), message("three"))

	if got := len(c.records()); got != 3 {
		t.Errorf("collector received %d records, want 3 flushed on close", got)
	}
}

func TestStreamSendsWhenBatchIsFull(t *testing.T) {
	c := newCollector(t)
	a := adapterFor(t, c, map[string]string{"batch_size": "2", "flush_interval": "1h"})

	runStream(t, a, message("1"), message("2"), message("3"), message("4"))

	batches := c.received()
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2 of size 2", len(batches))
	}
	for i, b := range batches {
		if len(b) != 2 {
			t.Errorf("batch %d has %d records, want 2", i, len(b))
		}
	}
}

func TestStreamFlushesOnInterval(t *testing.T) {
	c := newCollector(t)
	a := adapterFor(t, c, map[string]string{"batch_size": "1000", "flush_interval": "50ms"})

	logstream := make(chan *router.Message)
	done := make(chan struct{})
	go func() {
		a.Stream(logstream)
		close(done)
	}()
	logstream <- message("tick")

	deadline := time.After(10 * time.Second)
	for len(c.records()) == 0 {
		select {
		case <-deadline:
			t.Fatal("nothing was flushed on the interval")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(logstream)
	<-done
}

// The pump delivers messages synchronously, so a slow collector must not stall
// the adapter — that would back up every route sharing the container.
func TestStreamDoesNotBlockOnSlowCollector(t *testing.T) {
	c := newCollector(t)
	c.hold = make(chan struct{})
	a := adapterFor(t, c, map[string]string{"batch_size": "1", "flush_interval": "1h", "max_buffer": "100"})

	logstream := make(chan *router.Message)
	done := make(chan struct{})
	go func() {
		a.Stream(logstream)
		close(done)
	}()

	// The first message is in-flight and the collector is wedged; the adapter
	// must still accept more.
	accepted := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			logstream <- message("burst")
		}
		close(accepted)
	}()

	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("Stream blocked while the collector was slow")
	}

	close(c.hold)
	close(logstream)
	<-done
}

// A wedged collector must cost bounded memory, not grow until the container is
// OOM-killed as v1's unbounded slice did.
func TestStreamDropsOldestWhenQueueIsFull(t *testing.T) {
	c := newCollector(t)
	c.hold = make(chan struct{})
	a := adapterFor(t, c, map[string]string{"batch_size": "1", "max_buffer": "5", "flush_interval": "1h"})

	logstream := make(chan *router.Message)
	done := make(chan struct{})
	go func() {
		a.Stream(logstream)
		close(done)
	}()
	for i := 0; i < 50; i++ {
		logstream <- message("flood")
	}

	close(c.hold)
	close(logstream)
	<-done

	if a.dropped == 0 {
		t.Error("expected records to be dropped once the queue filled")
	}
	if got := len(c.records()); got > 50 {
		t.Errorf("collector received %d records, more than were sent", got)
	}
}

// The payload has to match what SigNoz's httplogreceiver (source: json) parses.
func TestPayloadShapeMatchesSigNozReceiver(t *testing.T) {
	c := newCollector(t)
	a := adapterFor(t, c, map[string]string{"batch_size": "1", "env": "prod"})

	runStream(t, a, message(`{"level":"error","msg":"boom","order_id":7}`))

	records := c.records()
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	rec := records[0]

	if _, ok := rec["body"].(string); !ok {
		t.Errorf("body = %#v, want a string", rec["body"])
	}
	if rec["body"] != "boom" {
		t.Errorf("body = %v, want boom", rec["body"])
	}
	if _, ok := rec["timestamp"].(float64); !ok {
		t.Errorf("timestamp = %#v, want a number", rec["timestamp"])
	}
	if rec["severity_text"] != "error" || rec["severity_number"] != float64(17) {
		t.Errorf("severity = %v/%v, want error/17", rec["severity_text"], rec["severity_number"])
	}
	attrs, ok := rec["attributes"].(map[string]interface{})
	if !ok {
		t.Fatalf("attributes = %#v, want a map", rec["attributes"])
	}
	if attrs["order_id"] != float64(7) {
		t.Errorf("order_id = %#v, want the number 7", attrs["order_id"])
	}
	resources, ok := rec["resources"].(map[string]interface{})
	if !ok {
		t.Fatalf("resources = %#v, want a map", rec["resources"])
	}
	if resources["deployment.environment"] != "prod" {
		t.Errorf("deployment.environment = %v", resources["deployment.environment"])
	}
	if resources["service.name"] != "app_web_1" {
		t.Errorf("service.name = %v", resources["service.name"])
	}

	if ct := c.lastRequest().Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestIngestionKeyHeaderIsSent(t *testing.T) {
	c := newCollector(t)
	a := adapterFor(t, c, map[string]string{"batch_size": "1", "ingestion_key": "secret-key"})

	runStream(t, a, message("hello"))

	if got := c.lastRequest().Header.Get("signoz-ingestion-key"); got != "secret-key" {
		t.Errorf("signoz-ingestion-key = %q, want secret-key", got)
	}
}

func TestNoIngestionKeyHeaderWhenUnset(t *testing.T) {
	c := newCollector(t)
	a := adapterFor(t, c, map[string]string{"batch_size": "1"})

	runStream(t, a, message("hello"))

	if _, present := c.lastRequest().Header["Signoz-Ingestion-Key"]; present {
		t.Error("an empty ingestion key should not be sent as a header")
	}
}
