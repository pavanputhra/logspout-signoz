# logspout-signoz

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A minimalistic adapter for [logspout](https://github.com/gliderlabs/logspout) to send notifications to [SigNoz](https://signoz.io/) using http(s) endpoint.

### Why do I need this?

Let's say you are running your application using docker or docker compose. You want to send logs to
SigNoz, then you can use this adapter to send logs to SigNoz.

### What features does it provide?

1. Direct post to signoz http endpoint. So this adapter can send more detailed logs.
1. Auto detect service name, so no special configuration needed.
   1. For JSON logs, picks name from JSON service field.
   1. Otherwise pick service name from docker-compose service name.
   1. Otherwise use docker image name as service name
1. Auto detect env name, so no special configuration needed
   1. For JSON logs, picks name from JSON env field.
   1. Otherwise pick env from logspout-signoz env variable ENV.
1. Auto parse JSON logs.
   1. Map well known JSON log attribute to appropriate Signoz log payload fields. e.g `level` to `SeverityText`, etc
   1. Pack other JSON attribute to into attributes key of Signoz log payload.

### How to use it?

First enable http log receiver by adding following to `otel-collector-config.yaml`

1. Add `httplogreceiver/json` to `receivers` section
1. Add `httplogreceiver/json` to `service.pipelines.logs.receivers` section

```yaml
receivers:
  httplogreceiver/json:
    endpoint: 0.0.0.0:8082
    source: json

...

service:
   pipelines:
      logs:
         receivers: [otlp, tcplog/docker, httplogreceiver/json]
         processors: [batch]
         exporters: [clickhouselogsexporter]
```

Open the port 8082 in your otel-collector container as follows:

```yaml
services:
   otel-collector:
   image: signoz/signoz-otel-collector:${OTELCOL_TAG:-0.102.10}
   container_name: signoz-otel-collector
   ports:
      - "8082:8082" # SigNoz logs
```

Then run the logspout-signoz container with the following command: 
(Run this on each node where you want to collect logs)

```bash
docker run -d \
        --volume=/var/run/docker.sock:/var/run/docker.sock \
        -e 'SIGNOZ_LOG_ENDPOINT=http://1.2.3.4:8082' \
        -e 'ENV=prod' \
        pavanputhra/logspout-signoz \
        signoz://localhost:8082
```

### Configuration options

You can use the following environment variables to configure the adapter:

- `SIGNOZ_LOG_ENDPOINT`: The URL of the SigNoz log endpoint. Default: `http://localhost:8082`
- `ENV`: The environment name.
- `DISABLE_JSON_PARSE`: Any string value will disable JSON parsing and sends the JSON log as it is.
- `DISABLE_LOG_LEVEL_STRING_MATCH`: For non-JSON logs, this adapter tries to detect log level by trying to search string
   "ERROR", "INFO", etc. and map it to Signoz log severity. Assigining any string value to this env var will disable 
   detection of log level.


### How to build and run it?

Follow the instructions to build your own [logspout image](https://github.com/gliderlabs/logspout/tree/master/custom) including this module.
In a nutshell, copy the contents of the `custom` folder and add the following import line above others in `modules.go`:
```go
import (
  _ "github.com/pavanputhra/logspout-signoz/signoz"
  ...
)
```

If you'd like to select a particular version create the following `Dockerfile`:
```
ARG VERSION
FROM gliderlabs/logspout:$VERSION

ONBUILD COPY ./build.sh /src/build.sh
ONBUILD COPY ./modules.go /src/modules.go
```

Then build your image with: `docker build --no-cache --pull --force-rm --build-arg VERSION=v3.2.11 -f dockerfile -t logspout:v3.2.11 .`


## Logspout configuration options

You can use the standard logspout filters to filter container names and output types:
