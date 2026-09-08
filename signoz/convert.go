package signoz

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/gliderlabs/logspout/router"
)

// LogRecord is one entry in the array POSTed to SigNoz's httplogreceiver
// (source: json). Field names and types mirror the receiver's own parser:
// attributes and resources accept native JSON types, and `body` is the
// canonical body key (`message` is also accepted by the receiver).
type LogRecord struct {
	Timestamp      int64                  `json:"timestamp"`
	TraceID        string                 `json:"trace_id,omitempty"`
	SpanID         string                 `json:"span_id,omitempty"`
	TraceFlags     int                    `json:"trace_flags,omitempty"`
	SeverityText   string                 `json:"severity_text"`
	SeverityNumber int                    `json:"severity_number"`
	Attributes     map[string]interface{} `json:"attributes,omitempty"`
	Resources      map[string]interface{} `json:"resources,omitempty"`
	Body           string                 `json:"body"`
}

// OpenTelemetry severity numbers.
const (
	sevTrace = 1
	sevDebug = 5
	sevInfo  = 9
	sevWarn  = 13
	sevError = 17
	sevFatal = 21
)

var severityByName = map[string]int{
	"trace":    sevTrace,
	"verbose":  sevTrace,
	"debug":    sevDebug,
	"info":     sevInfo,
	"inf":      sevInfo,
	"notice":   sevInfo + 1,
	"warn":     sevWarn,
	"warning":  sevWarn,
	"wrn":      sevWarn,
	"error":    sevError,
	"err":      sevError,
	"eror":     sevError,
	"critical": sevFatal,
	"crit":     sevFatal,
	"alert":    sevFatal,
	"emerg":    sevFatal,
	"fatal":    sevFatal,
	"panic":    sevFatal,
}

var severityName = map[int]string{
	sevTrace: "trace",
	sevDebug: "debug",
	sevInfo:  "info",
	sevWarn:  "warn",
	sevError: "error",
	sevFatal: "fatal",
}

// levelPattern finds a level word in an unstructured line. Anchored on word
// boundaries so "terror" is not an error, and the *first* match wins by
// position, which makes detection deterministic — v1 ranged over a map, so a
// line containing both INFO and ERROR got a random severity each run.
var levelPattern = regexp.MustCompile(`(?i)\b(trace|debug|info|warn(?:ing)?|error|fatal|panic|critical)\b`)

// Keys understood in structured logs, in priority order. zap, logrus, pino and
// bunyan all use `msg` and `time`/`ts` rather than `message`/`timestamp`, which
// v1 did not recognise at all.
var (
	bodyKeys      = []string{"body", "message", "msg", "log"}
	timestampKeys = []string{"timestamp", "time", "ts", "@timestamp"}
	levelKeys     = []string{"level", "severity", "severity_text", "levelname", "loglevel"}
	serviceKeys   = []string{"service", "service_name", "service.name"}
	envKeys       = []string{"env", "environment", "deployment_environment"}
	traceIDKeys   = []string{"trace_id", "traceId", "trace.id"}
	spanIDKeys    = []string{"span_id", "spanId", "span.id"}
)

// timeLayouts are tried in order for string timestamps.
var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.000Z0700",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999",
	"2006-01-02 15:04:05",
}

// convert turns one logspout message into a SigNoz log record.
func (a *Adapter) convert(m *router.Message) LogRecord {
	rec := LogRecord{
		// Nanoseconds. v1 sent seconds; SigNoz's getEpochNano rescales those
		// by digit count so they were accepted, but every log inside the same
		// second collapsed onto one timestamp and lost its ordering.
		Timestamp:      m.Time.UnixNano(),
		SeverityText:   "info",
		SeverityNumber: sevInfo,
		Body:           m.Data,
		Attributes:     map[string]interface{}{},
		Resources:      map[string]interface{}{},
	}

	if service := a.serviceName(m); service != "" {
		rec.Resources["service.name"] = service
	}
	if a.cfg.Env != "" {
		rec.Resources["deployment.environment"] = a.cfg.Env
	}
	if a.cfg.HostName != "" {
		rec.Resources["host.name"] = a.cfg.HostName
	}
	if m.Source != "" {
		rec.Attributes["log.iostream"] = m.Source
	}
	if m.Container != nil {
		if name := strings.TrimPrefix(m.Container.Name, "/"); name != "" {
			rec.Attributes["container.name"] = name
		}
		if id := m.Container.ID; id != "" {
			rec.Attributes["container.id"] = shortID(id)
		}
		if m.Container.Config != nil && m.Container.Config.Image != "" {
			rec.Attributes["container.image.name"] = m.Container.Config.Image
		}
	}

	if a.cfg.ParseJSON {
		if fields, ok := decodeJSONObject(m.Data); ok {
			a.applyJSONFields(&rec, fields)
			return rec
		}
	}

	if a.cfg.DetectLevel {
		if num, name, ok := detectLevel(m.Data); ok {
			rec.SeverityNumber, rec.SeverityText = num, name
		}
	}
	return rec
}

// applyJSONFields folds a parsed JSON log line into the record. Recognised keys
// are promoted to first-class fields; everything else is kept, with its
// original JSON type, as an attribute.
func (a *Adapter) applyJSONFields(rec *LogRecord, fields map[string]interface{}) {
	consumed := map[string]bool{}

	// find returns the first present key from keys. A key is only marked
	// consumed once it has actually been used, so a field we cannot make sense
	// of (a non-string body, a malformed trace_id) still survives as an
	// attribute instead of being silently dropped.
	find := func(keys []string) (string, interface{}, bool) {
		for _, k := range keys {
			if v, ok := fields[k]; ok && v != nil {
				return k, v, true
			}
		}
		return "", nil, false
	}
	takeString := func(keys []string) (string, bool) {
		k, v, ok := find(keys)
		if !ok {
			return "", false
		}
		s, isString := v.(string)
		if !isString || s == "" {
			return "", false
		}
		consumed[k] = true
		return s, true
	}

	if k, v, ok := find(timestampKeys); ok {
		if ts, ok := parseTimestamp(v); ok {
			consumed[k] = true
			rec.Timestamp = ts
		}
	}
	if body, ok := takeString(bodyKeys); ok {
		rec.Body = body
	}
	if k, v, ok := find(levelKeys); ok {
		if num, name, ok := parseLevel(v); ok {
			consumed[k] = true
			rec.SeverityNumber, rec.SeverityText = num, name
		}
	}
	// An explicit OTel severity_number from the producer wins over any mapping.
	if v, ok := fields["severity_number"]; ok {
		if n, ok := toInt(v); ok && n > 0 {
			consumed["severity_number"] = true
			rec.SeverityNumber = int(n)
			if name, known := severityName[normaliseSeverity(int(n))]; known {
				rec.SeverityText = name
			}
		}
	}
	if service, ok := takeString(serviceKeys); ok {
		rec.Resources["service.name"] = service
	}
	if env, ok := takeString(envKeys); ok {
		rec.Resources["deployment.environment"] = env
	}
	if ns, ok := takeString([]string{"namespace"}); ok {
		rec.Resources["namespace"] = ns
	}
	// Trace correlation: SigNoz links logs to traces when these are present.
	// They must be valid hex — the receiver rejects the entire batch otherwise.
	if k, v, ok := find(traceIDKeys); ok {
		if s, isString := v.(string); isString && isHex(s) {
			consumed[k] = true
			rec.TraceID = s
		}
	}
	if k, v, ok := find(spanIDKeys); ok {
		if s, isString := v.(string); isString && isHex(s) {
			consumed[k] = true
			rec.SpanID = s
		}
	}

	for k, v := range fields {
		if consumed[k] {
			continue
		}
		rec.Attributes[k] = v
	}
}

// decodeJSONObject reports whether s is a JSON *object* and returns its fields.
// Numbers are decoded with json.Number so integer IDs do not come out as
// 1.234e+06 once re-encoded.
func decodeJSONObject(s string) (map[string]interface{}, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var fields map[string]interface{}
	if err := dec.Decode(&fields); err != nil {
		return nil, false
	}
	return fields, true
}

// parseLevel maps a level value to an OTel severity. Handles strings and the
// numeric scales used by pino and bunyan (10 trace .. 60 fatal).
func parseLevel(v interface{}) (int, string, bool) {
	switch value := v.(type) {
	case string:
		name := strings.ToLower(strings.TrimSpace(value))
		if num, ok := severityByName[name]; ok {
			return num, canonicalName(num), true
		}
		return 0, "", false
	default:
		n, ok := toInt(v)
		if !ok {
			return 0, "", false
		}
		return numericLevel(n)
	}
}

// numericLevel maps pino/bunyan style numeric levels onto OTel severities.
func numericLevel(n int64) (int, string, bool) {
	switch {
	case n <= 0:
		return 0, "", false
	case n <= 10:
		return sevTrace, "trace", true
	case n <= 20:
		return sevDebug, "debug", true
	case n <= 30:
		return sevInfo, "info", true
	case n <= 40:
		return sevWarn, "warn", true
	case n <= 50:
		return sevError, "error", true
	default:
		return sevFatal, "fatal", true
	}
}

// detectLevel finds the earliest level word in an unstructured line.
func detectLevel(s string) (int, string, bool) {
	m := levelPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, "", false
	}
	num, ok := severityByName[strings.ToLower(m[1])]
	if !ok {
		return 0, "", false
	}
	return num, canonicalName(num), true
}

func canonicalName(num int) string {
	if name, ok := severityName[normaliseSeverity(num)]; ok {
		return name
	}
	return "info"
}

// normaliseSeverity rounds an OTel severity number down to its band base
// (INFO2=10 -> INFO=9) so it can be named.
func normaliseSeverity(n int) int {
	switch {
	case n >= 21:
		return sevFatal
	case n >= 17:
		return sevError
	case n >= 13:
		return sevWarn
	case n >= 9:
		return sevInfo
	case n >= 5:
		return sevDebug
	default:
		return sevTrace
	}
}

// parseTimestamp accepts RFC3339-ish strings and numeric epochs in seconds,
// milliseconds, microseconds or nanoseconds, returning nanoseconds.
func parseTimestamp(v interface{}) (int64, bool) {
	switch value := v.(type) {
	case string:
		for _, layout := range timeLayouts {
			if t, err := time.Parse(layout, value); err == nil {
				return t.UnixNano(), true
			}
		}
		return 0, false
	default:
		n, ok := toInt(v)
		if !ok || n <= 0 {
			return 0, false
		}
		return epochToNano(n), true
	}
}

// epochToNano scales an epoch to nanoseconds based on its magnitude.
func epochToNano(n int64) int64 {
	switch {
	case n >= 1e16: // nanoseconds
		return n
	case n >= 1e13: // microseconds
		return n * 1e3
	case n >= 1e10: // milliseconds
		return n * 1e6
	default: // seconds
		return n * 1e9
	}
}

func toInt(v interface{}) (int64, bool) {
	switch value := v.(type) {
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return n, true
		}
		if f, err := value.Float64(); err == nil {
			return int64(f), true
		}
		return 0, false
	case float64:
		return int64(value), true
	case int:
		return int64(value), true
	case int64:
		return value, true
	}
	return 0, false
}

func isHex(s string) bool {
	if s == "" || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// serviceName resolves service.name for a message: an explicit route option
// wins, otherwise it is derived from the container's labels.
func (a *Adapter) serviceName(m *router.Message) string {
	if a.cfg.ServiceName != "" {
		return a.cfg.ServiceName
	}
	return containerServiceName(m)
}
