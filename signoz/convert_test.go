package signoz

import (
	"encoding/json"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/logspout/router"
)

func testAdapter(t *testing.T, cfg *Config) *Adapter {
	t.Helper()
	if cfg == nil {
		cfg = &Config{ParseJSON: true, DetectLevel: true}
	}
	return &Adapter{cfg: cfg}
}

func message(data string) *router.Message {
	return &router.Message{
		Container: &docker.Container{
			ID:   "abcdef0123456789",
			Name: "/app_web_1",
			Config: &docker.Config{
				Image:  "ghcr.io/acme/api:1.4.2",
				Labels: map[string]string{},
			},
		},
		Data:   data,
		Source: "stdout",
		Time:   time.Date(2026, 3, 1, 12, 0, 0, 123456789, time.UTC),
	}
}

func TestConvertTimestampIsNanoseconds(t *testing.T) {
	rec := testAdapter(t, nil).convert(message("plain line"))
	want := time.Date(2026, 3, 1, 12, 0, 0, 123456789, time.UTC).UnixNano()
	if rec.Timestamp != want {
		t.Errorf("Timestamp = %d, want %d (nanoseconds, not seconds)", rec.Timestamp, want)
	}
}

// v1 only understood "message" and "timestamp", so logs from zap, logrus, pino
// and bunyan had their whole JSON blob shipped as the body.
func TestConvertRecognisesCommonLoggerKeys(t *testing.T) {
	tests := []struct {
		name string
		data string
		body string
		sev  string
	}{
		{"winston", `{"level":"info","message":"hello","timestamp":"2026-03-01T10:00:00Z"}`, "hello", "info"},
		{"logrus", `{"level":"warning","msg":"hello","time":"2026-03-01T10:00:00Z"}`, "hello", "warn"},
		{"zap", `{"level":"error","msg":"hello","ts":1772362800}`, "hello", "error"},
		{"pino", `{"level":40,"msg":"hello","time":1772362800000}`, "hello", "warn"},
		{"bunyan", `{"level":50,"msg":"hello","time":"2026-03-01T10:00:00Z"}`, "hello", "error"},
		{"elastic", `{"log.level":"debug","message":"hello","@timestamp":"2026-03-01T10:00:00Z"}`, "hello", "info"},
		{"docker", `{"log":"hello","time":"2026-03-01T10:00:00Z"}`, "hello", "info"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := testAdapter(t, nil).convert(message(tt.data))
			if rec.Body != tt.body {
				t.Errorf("Body = %q, want %q", rec.Body, tt.body)
			}
			if rec.SeverityText != tt.sev {
				t.Errorf("SeverityText = %q, want %q", rec.SeverityText, tt.sev)
			}
		})
	}
}

// Issue #10's crash log was a pino line; numeric levels used to be ignored.
func TestConvertNumericLevels(t *testing.T) {
	for _, tt := range []struct {
		level int
		want  string
		num   int
	}{{10, "trace", 1}, {20, "debug", 5}, {30, "info", 9}, {40, "warn", 13}, {50, "error", 17}, {60, "fatal", 21}} {
		rec := testAdapter(t, nil).convert(message(`{"level":` + itoa(tt.level) + `,"msg":"x"}`))
		if rec.SeverityText != tt.want || rec.SeverityNumber != tt.num {
			t.Errorf("level %d = %s/%d, want %s/%d", tt.level, rec.SeverityText, rec.SeverityNumber, tt.want, tt.num)
		}
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// v1 stringified every attribute with %v, so numbers and booleans arrived in
// SigNoz as strings and could not be filtered numerically.
func TestConvertKeepsAttributeTypes(t *testing.T) {
	rec := testAdapter(t, nil).convert(message(`{"msg":"x","count":42,"ratio":1.5,"ok":true,"tags":{"a":1}}`))

	encoded, err := json.Marshal(rec.Attributes)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["count"] != float64(42) {
		t.Errorf("count = %#v, want number 42", got["count"])
	}
	if got["ratio"] != 1.5 {
		t.Errorf("ratio = %#v, want number 1.5", got["ratio"])
	}
	if got["ok"] != true {
		t.Errorf("ok = %#v, want boolean true", got["ok"])
	}
	if _, isMap := got["tags"].(map[string]interface{}); !isMap {
		t.Errorf("tags = %#v, want nested object", got["tags"])
	}
}

func TestConvertLargeIntegerAttributeKeepsPrecision(t *testing.T) {
	rec := testAdapter(t, nil).convert(message(`{"msg":"x","order_id":9007199254740993}`))
	encoded, err := json.Marshal(rec.Attributes["order_id"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// float64 would round this to ...992, so decoding must use json.Number.
	if want := "9007199254740993"; string(encoded) != want {
		t.Errorf("order_id = %s, want %s", encoded, want)
	}
}

func TestConvertPromotesKnownFieldsOutOfAttributes(t *testing.T) {
	rec := testAdapter(t, nil).convert(message(
		`{"msg":"x","level":"info","time":"2026-03-01T10:00:00Z","service":"billing","env":"prod","namespace":"ns","foo":"bar"}`))

	for _, key := range []string{"msg", "level", "time", "service", "env", "namespace"} {
		if _, dup := rec.Attributes[key]; dup {
			t.Errorf("%q was promoted but also left in attributes", key)
		}
	}
	if rec.Attributes["foo"] != "bar" {
		t.Errorf("unknown key foo was not kept as an attribute")
	}
	if rec.Resources["service.name"] != "billing" {
		t.Errorf("service.name = %v, want billing", rec.Resources["service.name"])
	}
	if rec.Resources["deployment.environment"] != "prod" {
		t.Errorf("deployment.environment = %v, want prod", rec.Resources["deployment.environment"])
	}
	if rec.Resources["namespace"] != "ns" {
		t.Errorf("namespace = %v, want ns", rec.Resources["namespace"])
	}
}

func TestConvertTraceCorrelation(t *testing.T) {
	rec := testAdapter(t, nil).convert(message(
		`{"msg":"x","trace_id":"000000000000000018c51935df0b93b9","span_id":"18c51935df0b93b9"}`))
	if rec.TraceID != "000000000000000018c51935df0b93b9" {
		t.Errorf("TraceID = %q", rec.TraceID)
	}
	if rec.SpanID != "18c51935df0b93b9" {
		t.Errorf("SpanID = %q", rec.SpanID)
	}
}

func TestConvertRejectsNonHexTraceID(t *testing.T) {
	rec := testAdapter(t, nil).convert(message(`{"msg":"x","trace_id":"not-hex"}`))
	if rec.TraceID != "" {
		t.Errorf("TraceID = %q, want empty (SigNoz rejects the whole batch on a bad trace_id)", rec.TraceID)
	}
	if rec.Attributes["trace_id"] == nil {
		t.Errorf("an unusable trace_id should still survive as an attribute")
	}
}

// v1 ranged over a map to string-match levels, so a line containing two level
// words got a different severity on each run.
func TestDetectLevelIsDeterministicAndFirstMatchWins(t *testing.T) {
	const line = "ERROR while handling INFO request"
	for i := 0; i < 50; i++ {
		rec := testAdapter(t, nil).convert(message(line))
		if rec.SeverityText != "error" {
			t.Fatalf("iteration %d: SeverityText = %q, want error", i, rec.SeverityText)
		}
	}
}

func TestDetectLevelUsesWordBoundaries(t *testing.T) {
	rec := testAdapter(t, nil).convert(message("a terrorist plot was infowarred"))
	if rec.SeverityText != "info" {
		t.Errorf("SeverityText = %q, want the info default (no level word present)", rec.SeverityText)
	}
}

// v1 skipped level detection entirely for JSON that was valid but not an object.
func TestDetectLevelRunsForNonObjectJSON(t *testing.T) {
	for _, data := range []string{`[1,2,3] ERROR happened`, `42 ERROR happened`, `"ERROR happened"`} {
		rec := testAdapter(t, nil).convert(message(data))
		if rec.SeverityText != "error" {
			t.Errorf("%q: SeverityText = %q, want error", data, rec.SeverityText)
		}
	}
}

func TestParseJSONCanBeDisabled(t *testing.T) {
	a := testAdapter(t, &Config{ParseJSON: false, DetectLevel: true})
	data := `{"level":"error","msg":"hello"}`
	rec := a.convert(message(data))
	if rec.Body != data {
		t.Errorf("Body = %q, want the raw line %q", rec.Body, data)
	}
}

func TestDetectLevelCanBeDisabled(t *testing.T) {
	a := testAdapter(t, &Config{ParseJSON: true, DetectLevel: false})
	rec := a.convert(message("[ERROR] boom"))
	if rec.SeverityText != "info" {
		t.Errorf("SeverityText = %q, want the untouched info default", rec.SeverityText)
	}
}

func TestEpochToNano(t *testing.T) {
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	want := base.UnixNano()
	seconds := base.Unix()
	for _, in := range []int64{seconds, seconds * 1e3, seconds * 1e6, seconds * 1e9} {
		if got := epochToNano(in); got != want {
			t.Errorf("epochToNano(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestExplicitSeverityNumberWins(t *testing.T) {
	rec := testAdapter(t, nil).convert(message(`{"msg":"x","level":"info","severity_number":17}`))
	if rec.SeverityNumber != 17 || rec.SeverityText != "error" {
		t.Errorf("got %s/%d, want error/17", rec.SeverityText, rec.SeverityNumber)
	}
}

func TestConvertContainerMetadata(t *testing.T) {
	rec := testAdapter(t, nil).convert(message("hello"))
	if rec.Attributes["container.name"] != "app_web_1" {
		t.Errorf("container.name = %v", rec.Attributes["container.name"])
	}
	if rec.Attributes["container.id"] != "abcdef012345" {
		t.Errorf("container.id = %v, want the short id", rec.Attributes["container.id"])
	}
	if rec.Attributes["log.iostream"] != "stdout" {
		t.Errorf("log.iostream = %v", rec.Attributes["log.iostream"])
	}
}
