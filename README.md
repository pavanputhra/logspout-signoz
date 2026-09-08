# logspout-signoz

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker Pulls](https://img.shields.io/docker/pulls/pavanputhra/logspout-signoz)](https://hub.docker.com/r/pavanputhra/logspout-signoz)

A [logspout](https://github.com/gliderlabs/logspout) adapter that ships Docker
container logs to [SigNoz](https://signoz.io/) — over OTLP, or over SigNoz's own
HTTP log endpoint.

**Use `pavanputhra/logspout-signoz:v2`.**

> **Already running v1?** v2 configures the destination through the logspout
> route URI instead of `SIGNOZ_LOG_ENDPOINT`. Your existing environment
> variables still work — they log a deprecation warning rather than breaking —
> so you can upgrade first and move the settings into the route afterwards. See
> [Migrating from v1](#migrating-from-v1).

### Which tag to use

| Tag | What it is |
|---|---|
| `v2` | Latest v2.x. **Recommended** — picks up fixes, never a breaking change. |
| `v2.0.0` | An exact release, for reproducible deployments. |
| `latest` | Whatever the newest release is. Moves across major versions. |
| `edge` | Built from `main`. Untagged and unstable; for trying fixes early. |
| `v1` | Frozen final v1 build. Only if you cannot upgrade yet. |

## Why use it

- Sends directly to SigNoz's HTTP log endpoint, self-hosted or Cloud.
- Detects `service.name` from Docker Compose and Swarm labels — usually no
  configuration needed.
- Parses JSON logs from the loggers people actually use (winston, pino, bunyan,
  zap, logrus), mapping levels, timestamps and message bodies to their SigNoz
  equivalents and keeping the rest as typed attributes.
- Detects log levels in plain-text logs.
- Carries `trace_id`/`span_id` through, so logs link to traces in SigNoz.

## Which protocol

Two route schemes, both supported indefinitely. Pick one:

| | `otlp://` | `signoz://` |
|---|---|---|
| Collector setup | **none** — OTLP is on by default (port 4318) | add `httplogreceiver/json`, expose 8082 |
| Works with | any OpenTelemetry collector | SigNoz only |
| Attribute fidelity | full — integers, nested objects, arrays | numbers become floats, nested values become strings |

**New installs should use `otlp://`.** It needs no collector configuration and
keeps log attributes intact. `signoz://` remains fully supported for existing
deployments and is unchanged.

The fidelity difference is in SigNoz's `httplogreceiver`, not in this adapter:
it decodes every JSON number as a float (so a 19-digit id loses precision) and
serialises nested objects to strings. OTLP carries them exactly.

```bash
otlp://otel-collector:4318?env=prod        # recommended
signoz://otel-collector:8082?env=prod      # needs the receiver configured below
```

## Quick start

### 1. Enable the HTTP log receiver in SigNoz

**Only needed for `signoz://` routes.** Skip this entirely if you use `otlp://`.

Self-hosted only; skip this for SigNoz Cloud. In `otel-collector-config.yaml`:

```yaml
receivers:
  httplogreceiver/json:
    endpoint: 0.0.0.0:8082
    source: json

service:
  pipelines:
    logs:
      receivers: [otlp, tcplog/docker, httplogreceiver/json]
      processors: [batch]
      exporters: [clickhouselogsexporter]
```

Expose the port on the collector container:

```yaml
services:
  otel-collector:
    image: signoz/signoz-otel-collector:${OTELCOL_TAG}
    ports:
      - "8082:8082" # SigNoz logs
```

### 2. Run the adapter

Run one per node, mounting the Docker socket:

```bash
docker run -d \
  --name logspout-signoz \
  --volume=/var/run/docker.sock:/var/run/docker.sock \
  --volume=/etc/hostname:/etc/host_hostname:ro \
  --restart=always \
  pavanputhra/logspout-signoz:v2 \
  'otlp://otel-collector:4318?env=prod'
```

Swap the scheme for `signoz://otel-collector:8082?env=prod` to use SigNoz's HTTP
log endpoint instead.

The **route URI is the configuration**: the address is where logs go, and the
query string sets everything else.

### SigNoz Cloud

Use the `+https` transport and pass your ingestion key. logspout drops the path
from a route URI, so the path is given as an option:

```bash
docker run -d \
  --volume=/var/run/docker.sock:/var/run/docker.sock \
  -e SIGNOZ_INGESTION_KEY=<your-key> \
  pavanputhra/logspout-signoz:v2 \
  'otlp+https://ingest.us.signoz.cloud:443?env=prod'
```

`otlp://` already defaults to the `/v1/logs` path that OTLP mandates, so it needs
no `path` option. For the HTTP log endpoint instead:

```bash
  'signoz+https://ingest.us.signoz.cloud:443?path=/logs/json&env=prod'
```

Put the key in `SIGNOZ_INGESTION_KEY` rather than the URI so it does not show up
in `docker ps` output or your shell history.

### docker-compose

```yaml
services:
  logspout-signoz:
    image: pavanputhra/logspout-signoz:v2
    restart: always
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /etc/hostname:/etc/host_hostname:ro
    # Note the list form. See the warning below.
    command: ["otlp://otel-collector:4318?env=prod&filter.labels=logging:signoz"]
    depends_on:
      - otel-collector
```

> **Write `command` as a list, not a string.** Compose parses a string `command`
> with shell-like splitting, so it truncates the route at the first `&`:
>
> ```yaml
> command: 'otlp://otel-collector:4318?env=prod&filter.labels=logging:signoz'
> ```
>
> reaches the container as `otlp://otel-collector:4318?env=prod` — everything
> after the first `&` is silently dropped, so filters and tuning options never
> take effect. Nothing errors; the options simply are not there. The startup
> banner prints the route it actually parsed, which is the quickest way to
> confirm what took effect.

## Configuration

Every option can be set two ways: as a query parameter on the route URI, or as
an environment variable. **The route option wins**, which is what lets one
logspout process feed several destinations.

| Option (URI) | Environment variable | Default | Description |
|---|---|---|---|
| `path` | `SIGNOZ_PATH` | `/v1/logs` for `otlp://`, `/` for `signoz://` | Request path. Rarely needed: each scheme already defaults to the right one. |
| `ingestion_key` | `SIGNOZ_INGESTION_KEY` | — | Sent as the `signoz-ingestion-key` header (SigNoz Cloud). |
| `env` | `SIGNOZ_ENV` | — | Sets the `deployment.environment` resource. |
| `service_name` | `SIGNOZ_SERVICE_NAME` | auto | Forces `service.name` instead of detecting it. |
| `host_name` | `SIGNOZ_HOST_NAME` | `/etc/host_hostname` | Sets the `host.name` resource. |
| `parse_json` | `SIGNOZ_PARSE_JSON` | `true` | Parse JSON log lines into fields. |
| `detect_level` | `SIGNOZ_DETECT_LEVEL` | `true` | Detect the level in plain-text lines. |
| `batch_size` | `SIGNOZ_BATCH_SIZE` | `100` | Records per request. |
| `flush_interval` | `SIGNOZ_FLUSH_INTERVAL` | `5s` | Send a partial batch after this long. |
| `max_buffer` | `SIGNOZ_MAX_BUFFER` | `10000` | Records held while the collector is unreachable. Oldest are dropped beyond this. |
| `timeout` | `SIGNOZ_TIMEOUT` | `10s` | HTTP timeout per attempt. |
| `retry_count` | `SIGNOZ_RETRY_COUNT` | `3` | Retries for network errors, 429 and 5xx. 4xx is never retried. |
| `tls_skip_verify` | `SIGNOZ_TLS_SKIP_VERIFY` | `false` | Skip TLS verification (self-signed certificates). |

Set `DEBUG=1` for verbose logging.

### Choosing which containers to ship

Filtering is handled by logspout itself, using its standard parameters:

```bash
# only containers whose name starts with my-proj-
'signoz://otel-collector:8082?filter.name=my-proj-*'

# only stderr
'signoz://otel-collector:8082?filter.sources=stderr'

# only containers with a matching label
'signoz://otel-collector:8082?filter.labels=logging:signoz'
```

To exclude a container instead, set `LOGSPOUT=ignore` in its environment, or run
logspout with `EXCLUDE_LABEL=<label>` and put that label on the container.

The adapter's own container is not excluded automatically, so if you want to
keep its startup messages out of SigNoz, set `LOGSPOUT=ignore` on it.

### Several destinations

Because the destination comes from the route, one logspout can serve more than
one — including a mix of protocols, which is a low-risk way to compare `otlp://`
against `signoz://` before switching. Separate routes with commas:

```bash
docker run -d --volume=/var/run/docker.sock:/var/run/docker.sock \
  pavanputhra/logspout-signoz:v2 \
  'otlp://collector-a:4318?filter.name=team-a-*,otlp://collector-b:4318?filter.name=team-b-*'
```

### Multi-line logs

Stack traces arrive one line at a time. Chain logspout's `multiline` adapter,
which is built into the image:

```bash
'multiline+otlp://otel-collector:4318?env=prod'
'multiline+signoz://otel-collector:8082?env=prod'
```

Its behaviour is controlled by the `MULTILINE_*` environment variables
documented by [logspout](https://github.com/gliderlabs/logspout/tree/master/adapters/multiline).

## How logs are mapped

### service.name

The first of these that is present:

1. a `service` field in the JSON log line
2. the `service_name` option
3. `com.docker.swarm.service.name`
4. `com.docker.compose.service`
5. the container name
6. the image repository name (`ghcr.io/acme/api:1.4.2` becomes `api`)

### JSON logs

Recognised keys are promoted; everything else becomes an attribute with its JSON
type preserved, so numbers stay numbers in SigNoz.

| Field | Keys accepted |
|---|---|
| Body | `body`, `message`, `msg`, `log` |
| Timestamp | `timestamp`, `time`, `ts`, `@timestamp` |
| Level | `level`, `severity`, `severity_text`, `levelname`, `loglevel` |
| Service | `service`, `service_name`, `service.name` |
| Environment | `env`, `environment`, `deployment_environment` |
| Trace | `trace_id`, `traceId`, `trace.id`, `span_id`, `spanId`, `span.id` |

Timestamps may be RFC3339 strings or numeric epochs in seconds, milliseconds,
microseconds or nanoseconds. Levels may be names (`warn`, `error`, ...) or the
numeric scale used by pino and bunyan (10 trace through 60 fatal).

## Migrating from v1

v1 read its destination from `SIGNOZ_LOG_ENDPOINT` and ignored the route
address. v2 uses the route address, but **`SIGNOZ_LOG_ENDPOINT` still wins if it
is set**, so an existing deployment keeps working and warns. Remove it once you
have moved the address into the route.

| v1 | v2 | Still works in v2? |
|---|---|---|
| `SIGNOZ_LOG_ENDPOINT=http://host:8082` | `signoz://host:8082` | Yes, with a warning |
| `ENV=prod` | `?env=prod` or `SIGNOZ_ENV` | Yes, with a warning |
| `DISABLE_LOG_LEVEL_STRING_MATCH=1` | `?detect_level=false` | Yes, with a warning |
| `DISABLE_JSON_PARSE=1` | `?parse_json=false` | See below |
| `?filter.name=...` handled by the adapter | handled by logspout | Yes — unchanged for users |

`DISABLE_JSON_PARSE` never actually worked in v1: the value was read into a
field that was never used, so JSON parsing always ran. v2 does not start
honouring it silently — it warns and points at `?parse_json=false`.

Other behaviour changes worth knowing about:

- **Timestamps are nanoseconds.** v1 sent whole seconds, so logs within the same
  second could not be ordered.
- **Attributes keep their JSON types.** v1 stringified everything, so numeric
  fields could not be compared numerically in SigNoz.
- **Swarm `service.name` comes from the service label**, not the task label. v1
  used `com.docker.swarm.task.name`, which changes on every restart and created
  a new service in SigNoz each time. Expect existing Swarm dashboards to need
  updating to the stable name.
- **The body key is `body`** rather than `message`. SigNoz accepts both.
- **Image names are reduced to the repository name.**
- The image now also includes logspout's `/health` endpoint, the routes API and
  the `multiline` adapter.

Pin `pavanputhra/logspout-signoz:v1` to stay on v1.

## Building your own image

```bash
docker build -t logspout-signoz .
```

The build compiles logspout with this adapter from the source in the checkout —
no module proxy involved — and cross-compiles, so multi-arch builds do not need
QEMU. Pin a different logspout with `--build-arg LOGSPOUT_VERSION=v3.2.14`.

To add other logspout modules, edit [`custom/modules.go`](custom/modules.go).

To use the adapter from your own logspout build, import it:

```go
package main

import (
    _ "github.com/pavanputhra/logspout-signoz/v2/signoz"
)
```

## Releasing

Image tags are published by [`.github/workflows/release.yml`](.github/workflows/release.yml):

- pushing a git tag `vX.Y.Z` publishes `vX.Y.Z`, `vX` and `latest`
- commits to `main` publish `edge` and a short-SHA tag

`latest` therefore only moves on a deliberate release.

## License

MIT
