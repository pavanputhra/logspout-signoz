#!/bin/sh
# Emits every log shape the adapter claims to understand.
#
# Repeats forever rather than emitting once. logspout attaches with
# backlog=false for containers that already exist when it starts, so a one-shot
# burst is lost unless the producer happens to start last. Looping makes the
# harness independent of container start order.
while true; do
  echo 'plain unstructured line'
  echo '[ERROR] bracketed level marker'
  echo 'level=warn msg="logfmt style, not json"'
  echo 'a terrorist plot infowarred'                                   # must NOT match a level
  echo '{"level":"info","message":"winston style","timestamp":"2026-03-01T10:00:00Z","userId":7}'
  echo '{"level":40,"msg":"pino style","time":1772362800000,"pid":1,"order_id":9007199254740993}'
  echo '{"level":50,"msg":"bunyan style","time":"2026-03-01T10:00:00Z","v":0}'
  echo '{"level":"error","msg":"zap style","ts":1772362800.123,"caller":"main.go:42"}'
  echo '{"level":"warning","msg":"logrus style","time":"2026-03-01T10:00:00Z"}'
  echo '{"log":"docker json-file style","time":"2026-03-01T10:00:00Z"}'
  echo '{"level":"debug","msg":"trace correlated","trace_id":"000000000000000018c51935df0b93b9","span_id":"18c51935df0b93b9"}'
  echo '{"level":"info","msg":"typed attrs","count":42,"ratio":1.5,"ok":true,"tags":{"a":1},"list":[1,"two"]}'
  echo '{"level":"info","msg":"service override","service":"billing-svc","env":"staging"}'
  echo '{"level":"info","msg":"bad trace id","trace_id":"not-hex-at-all"}'
  echo '{"msg":"no level field at all"}'
  echo '[1,2,3] ERROR non-object json with a level word'
  echo '{"level":"info","msg":"unicode ✅ emoji 🚀 and \"quotes\" and \\ backslash"}'
  echo "{\"level\":\"info\",\"msg\":\"$(awk 'BEGIN{while(i++<4000)printf "x"}')\"}"
  echo 'not json { but starts like it'
  echo '' 
  echo 'FATAL something exploded'
  echo 'stderr diagnostic line' >&2
  echo '{"level":"error","msg":"stderr structured"}' >&2
  # Signals the healthcheck: logspout must not start until this container is
  # genuinely running, or it can lose it to the race described in README.md.
  touch /tmp/ready
  sleep 10
done
