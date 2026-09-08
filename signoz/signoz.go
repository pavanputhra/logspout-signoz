// Package signoz provides a logspout adapter that ships Docker container logs
// to SigNoz over HTTP.
//
// Routes are configured the logspout way — the destination comes from the route
// address and settings come from the route's query string, with environment
// variables as process-wide defaults:
//
//	signoz://otel-collector:8082?env=prod
//	signoz+https://ingest.us.signoz.cloud:443?path=/logs/json
//
// Container filtering is logspout's job, not the adapter's: filter.name,
// filter.id, filter.labels and filter.sources are applied by the router before
// a message ever reaches Stream.
package signoz

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/gliderlabs/logspout/router"
)

func init() {
	router.AdapterFactories.Register(NewSignozAdapter, "signoz")
}

// Version identifies this adapter to collectors that record an instrumentation
// scope.
const Version = "2.1.0"

// Adapter streams logspout messages to a log collector.
type Adapter struct {
	name   string
	route  *router.Route
	cfg    *Config
	client *client

	queue    chan []LogRecord
	wg       sync.WaitGroup
	dropped  int
	dropOnce sync.Once
}

// NewSignozAdapter returns an adapter that posts to SigNoz's httplogreceiver.
func NewSignozAdapter(route *router.Route) (router.LogAdapter, error) {
	return newAdapter(route, "signoz", signozCodec{}, signozDefaultPath)
}

// newAdapter builds an adapter for one protocol. Everything except the wire
// format and the default path is shared.
func newAdapter(route *router.Route, name string, wire codec, defaultPath string) (router.LogAdapter, error) {
	cfg, err := NewConfig(route, defaultPath)
	if err != nil {
		return nil, err
	}
	log.Printf("%s: routing to %s (batch %d, flush %s)", name, cfg.Endpoint, cfg.BatchSize, cfg.FlushInterval)
	return &Adapter{
		name:   name,
		route:  route,
		cfg:    cfg,
		client: newClient(cfg, wire),
		// Bounded: at most MaxBuffer records may be waiting to be sent, so a
		// collector outage costs a known amount of memory instead of growing
		// until the container is OOM-killed.
		queue: make(chan []LogRecord, queueDepth(cfg)),
	}, nil
}

func queueDepth(cfg *Config) int {
	depth := cfg.MaxBuffer / cfg.BatchSize
	if depth < 1 {
		return 1
	}
	return depth
}

// Stream consumes the log stream until it closes, batching records and handing
// them to a sender goroutine.
//
// Batching and sending are deliberately separate: the loop below must never
// block on HTTP, because logspout's pump delivers messages synchronously and a
// slow adapter stalls every other route on the same container.
func (a *Adapter) Stream(logstream chan *router.Message) {
	ticker := time.NewTicker(a.cfg.FlushInterval)
	defer ticker.Stop()

	a.wg.Add(1)
	go a.sendLoop()

	batch := make([]LogRecord, 0, a.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		a.enqueue(batch)
		batch = make([]LogRecord, 0, a.cfg.BatchSize)
	}

	for {
		select {
		case message, ok := <-logstream:
			if !ok {
				// The route is closing. v1 returned here and lost whatever was
				// buffered, leaving its ticker goroutine running forever.
				flush()
				close(a.queue)
				a.wg.Wait()
				debug("route closed, sender drained")
				return
			}
			batch = append(batch, a.convert(message))
			if len(batch) >= a.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// enqueue hands a batch to the sender, dropping the oldest queued batch if the
// sender has fallen behind. Losing the oldest logs is better than blocking the
// pump or growing without limit.
func (a *Adapter) enqueue(batch []LogRecord) {
	select {
	case a.queue <- batch:
		return
	default:
	}

	select {
	case old := <-a.queue:
		a.dropped += len(old)
		a.dropOnce.Do(func() {
			log.Printf("%s: sender is behind, dropping oldest logs "+
				"(max_buffer=%d); further drops are logged only with DEBUG set", a.name, a.cfg.MaxBuffer)
		})
		debug("dropped", len(old), "records; total dropped", a.dropped)
	default:
	}

	select {
	case a.queue <- batch:
	default:
		a.dropped += len(batch)
		debug("dropped", len(batch), "records; total dropped", a.dropped)
	}
}

// sendLoop posts queued batches until the queue is closed.
func (a *Adapter) sendLoop() {
	defer a.wg.Done()
	for batch := range a.queue {
		ctx, cancel := context.WithTimeout(context.Background(), a.sendDeadline())
		if err := a.client.send(ctx, batch); err != nil {
			// Errors go through log.Println, never fmt.Println, and carry no
			// per-flush chatter: logspout collects its own container's stdout,
			// so anything printed here comes back around as a log line.
			log.Printf("%s: dropping %d records: %v", a.name, len(batch), err)
		}
		cancel()
	}
}

// sendDeadline bounds one batch including its retries.
func (a *Adapter) sendDeadline() time.Duration {
	return a.cfg.Timeout*time.Duration(a.cfg.RetryCount+1) + 30*time.Second
}
