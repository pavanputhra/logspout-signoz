# Changelog

## v2.1.0

Adds OTLP as a second, additive protocol. Nothing about `signoz://` changes, and
no configuration needs updating.

### Added

- **`otlp://` and `otlp+https://` routes** speaking OTLP/HTTP with JSON
  encoding. OTLP is enabled by default on every OpenTelemetry collector, so
  these routes need no receiver configuration — no `httplogreceiver/json`, no
  port 8082 — and work against any OTel backend, not only SigNoz. The path
  defaults to the `/v1/logs` the specification mandates.
- Attributes keep full fidelity over OTLP. SigNoz's `httplogreceiver` decodes
  every JSON number as a float and serialises nested objects to strings, so a
  19-digit id loses precision and nested values arrive as text; OTLP carries
  integers, nested objects and arrays exactly.
- A partial success from a collector — a 200 whose body reports rejected
  records — is treated as a failure. Reading only the status code would drop
  those records silently.
- Records are grouped by resource into `resourceLogs`, so a batch spanning
  several containers is encoded the way the protocol intends.

### Changed

- The deprecation notice for v1 environment variables no longer promises
  removal in a future major version. They stay supported; there is no planned
  release that drops them.
- The README recommends `otlp://` for new installs and documents that a
  docker-compose `command` written as a string is truncated at the first `&`,
  which silently discards filters and tuning options.

## v2.0.0

Breaking release. Configuration now follows logspout's conventions: the
destination comes from the route address and settings come from the route's
query string, with environment variables as process-wide defaults.

v1 environment variables still work and log a deprecation notice, so an
existing deployment keeps shipping logs after upgrading. They stay supported;
there is no planned release that removes them.

### Breaking

- The Go module path is now `github.com/pavanputhra/logspout-signoz/v2`. Custom
  builds must update the import in `modules.go`.
- Timestamps are sent in nanoseconds instead of whole seconds, so logs within
  one second are now ordered correctly.
- Attributes keep their JSON types instead of being stringified, so numeric
  fields can be compared numerically in SigNoz.
- `service.name` for Swarm comes from `com.docker.swarm.service.name` instead of
  `com.docker.swarm.task.name`. The task label changes on every restart, which
  created a new service in SigNoz each time. Swarm dashboards may need updating.
- `service.name` derived from an image is now the repository name, so it no
  longer changes with every tag bump.
- The log body is sent as `body` rather than `message`. SigNoz accepts both.
- `latest` now moves only when a git tag is pushed; `main` publishes `edge`.

### Added

- SigNoz Cloud support: `signoz+https://` routes and a `signoz-ingestion-key`
  header via `ingestion_key` / `SIGNOZ_INGESTION_KEY`.
- Per-route configuration, so one logspout process can feed several
  destinations with different settings.
- `trace_id` and `span_id` pass-through, linking logs to traces in SigNoz.
- Recognises the keys real loggers emit — `msg`, `time`, `ts`, `@timestamp`,
  `log`, `severity`, `levelname` — alongside the ones v1 knew.
- Numeric log levels from pino and bunyan (10 trace through 60 fatal).
- Numeric epoch timestamps in seconds, milliseconds, microseconds or
  nanoseconds.
- `host.name` from a mounted `/etc/host_hostname`, plus `container.name`,
  `container.id`, `container.image.name` and `log.iostream` attributes.
- Tunable delivery: `batch_size`, `flush_interval`, `max_buffer`, `timeout`,
  `retry_count`.
- Invalid options now stop the route at startup with a clear message instead of
  being silently ignored.
- The image includes logspout's `/health` endpoint, the routes API and the
  `multiline` adapter (`multiline+signoz://...`) for stack traces.

### Fixed

- **The route address was ignored.** v1 always read `SIGNOZ_LOG_ENDPOINT`, so
  `signoz://host:8082` was decorative and two routes could not have different
  destinations.
- **Container filtering could never fire.** v1 re-implemented `filter.name`,
  `filter.id`, `filter.labels` and `filter.sources` by reading
  `route.Options`, but logspout parses those into the `Route` itself and never
  puts them in `Options`, so the adapter's copy was dead code with different
  matching rules. Filtering is logspout's job and now stays there. (#4)
- **No HTTP timeout.** v1 used `http.Post`, i.e. `http.DefaultClient`, so one
  unreachable collector blocked delivery indefinitely while the buffer grew
  without bound. There is now a timeout, bounded buffering, and retries with
  exponential backoff for network errors, 429 and 5xx. A 4xx is reported once
  and dropped rather than retried forever.
- **Buffered logs were lost when a route closed**, and the flush goroutine and
  its ticker leaked. Stream now flushes and drains before returning.
- **Log spam on every flush.** v1 printed to stdout each time it sent a batch;
  because logspout collects its own container's logs, that output was shipped
  to SigNoz and could feed back into the next batch. Only errors are logged now,
  with details behind `DEBUG`. (#9, #5)
- **Level detection was nondeterministic**: v1 ranged over a map, so a line
  containing both `INFO` and `ERROR` got a different severity on each run, and
  substrings matched inside words. Detection is now anchored on word boundaries
  and the first level word wins.
- Level detection was skipped entirely for lines that were valid JSON but not an
  object.
- `DISABLE_JSON_PARSE` was documented but had no effect: the value was read into
  a field that was never used. It is now deprecated in favour of
  `?parse_json=false`, which does work.
- Large integers in JSON logs are no longer rounded through `float64`.
- Any 2xx response is accepted; v1 required exactly 200.

### Build

- The image is built from the source in the checkout instead of being fetched
  from the Go module proxy. v1 inherited gliderlabs/logspout's `ONBUILD`
  triggers, which resolved this module through the proxy — a fresh commit could
  be built against stale cached code, which is what `versoin_bump.md` worked
  around. That file is gone.
- The logspout version is pinned (`LOGSPOUT_VERSION`, default `v3.2.14`) rather
  than tracking `:master`.
- Multi-arch images cross-compile from the build platform and the runtime stage
  contains no `RUN`, so builds no longer run under QEMU emulation.
- CI runs `gofmt`, `go vet` and `go test -race` and builds the image on pull
  requests. v1 only built on pushes to `main` and ran no tests.
