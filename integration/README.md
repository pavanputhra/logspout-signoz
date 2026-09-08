# Integration harness

Runs the adapter against **real collectors** and checks what actually arrives.
This is what backed the v2.1.0 release claims; unit tests cover encoding, this
covers the whole path from a container's stdout to a collector's output.

Note: `test/` is in `.gitignore`, so nothing here is committed.

## What it stands up

| Service | Role |
|---|---|
| `otelcol` | `opentelemetry-collector-contrib`, OTLP receiver on 4318, writes `out/otlp-received.json` |
| `signozcol` | `signoz-otel-collector`, both `httplogreceiver` (8082) and OTLP (4318) |
| `logspout-otlp` | `otlp://otelcol:4318` |
| `logspout-signoz` | `signoz://signozcol:8082` |
| `logspout-otlp-signoz` | `otlp://signozcol:4318` — OTLP against SigNoz's own distribution |
| `producer` | labelled `logtest=yes`, emits every log shape in `produce.sh` |
| `ignored` | unlabelled — must **not** be collected |

## Run

```sh
# from the repository root
docker build -t logspout-signoz:itest .

cd test/integration
mkdir -p out && chmod 777 out
docker compose up -d
sleep 30

python3 verify.py out/otlp-received.json        "OTLP -> contrib"          otlp
python3 verify.py out/signoz-otlp-received.json "OTLP -> signoz"           otlp
python3 verify.py out/signoz-received.json      "signoz:// -> signoz"      signoz
```

`verify.py` exits non-zero if any assertion fails. The third argument names the
receiver: three assertions are marked n/a for `signoz` because SigNoz's
`httplogreceiver` decodes JSON numbers as floats and stringifies nested values,
which no adapter change can fix on that path.

## Swarm

```sh
docker swarm init --advertise-addr 127.0.0.1
docker stack deploy -c swarm-stack.yaml swarmtest
sleep 25

# force task recreation; service.name must stay stable across it
docker service update --force --detach swarmtest_api
sleep 30
```

Then confirm every record still reports one `service.name` (`swarmtest_api`)
while `container.name` shows several task containers. v1 used the per-task
label, so each restart created a new service in SigNoz.

## Tear down

```sh
docker stack rm swarmtest && sleep 12
docker swarm leave --force
docker compose down -v --remove-orphans
```

## Start order matters

Every logspout service waits for the producers to report healthy, and that
gating is load-bearing rather than tidiness.

logspout's pump gives up on a container permanently if, at the moment it
inspects it, the container is not yet `State.Running` — `client.Logs` returns
immediately, the inspect says "not running", and the pump marks it dead and
exits. A container that starts in the same instant as logspout can therefore be
dropped for the lifetime of the process.

Observed directly while building this harness: `logspout-otlp` collected nothing
for minutes while a sibling logspout started one second later collected fine,
and recreating `logspout-otlp` alone fixed it.

## Gotcha this harness caught

`command:` must be a **list**. Compose parses a string command with shell-like
splitting and truncates the route at the first `&`, silently dropping filters
and tuning options:

```yaml
command: 'otlp://otelcol:4318?env=x&filter.labels=logtest:yes'   # becomes ?env=x
command: ["otlp://otelcol:4318?env=x&filter.labels=logtest:yes"] # correct
```
