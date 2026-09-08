package signoz

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gliderlabs/logspout/router"
)

func encodeOTLP(t *testing.T, records []LogRecord) map[string]interface{} {
	t.Helper()
	payload, err := otlpCodec{}.encode(records)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	return decoded
}

// Walks to the single log record in a payload built from one resource.
func onlyRecord(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	resourceLogs := payload["resourceLogs"].([]interface{})
	if len(resourceLogs) != 1 {
		t.Fatalf("got %d resourceLogs, want 1", len(resourceLogs))
	}
	scopeLogs := resourceLogs[0].(map[string]interface{})["scopeLogs"].([]interface{})
	records := scopeLogs[0].(map[string]interface{})["logRecords"].([]interface{})
	if len(records) != 1 {
		t.Fatalf("got %d logRecords, want 1", len(records))
	}
	return records[0].(map[string]interface{})
}

func TestOTLPAdapterDefaultsToTheSpecPath(t *testing.T) {
	clearLegacyEnv(t)
	cfg, err := NewConfig(route("collector:4318", nil), otlpDefaultPath)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Endpoint != "http://collector:4318/v1/logs" {
		t.Errorf("Endpoint = %q, want the OTLP path by default", cfg.Endpoint)
	}
}

func TestOTLPAdapterIsRegistered(t *testing.T) {
	if _, found := router.AdapterFactories.Lookup("otlp"); !found {
		t.Error("otlp adapter is not registered")
	}
	if _, found := router.AdapterFactories.Lookup("signoz"); !found {
		t.Error("signoz adapter is no longer registered")
	}
}

// 64-bit fields are strings in protobuf JSON. A number here is rejected by
// strict receivers, so this is the encoding's sharpest edge.
func TestOTLPTimestampsAreStrings(t *testing.T) {
	record := onlyRecord(t, encodeOTLP(t, []LogRecord{{
		Timestamp: 1772362800123456789, Observed: 1772362800999999999, Body: "x",
	}}))

	timestamp, ok := record["timeUnixNano"].(string)
	if !ok {
		t.Fatalf("timeUnixNano = %#v, want a string", record["timeUnixNano"])
	}
	if timestamp != "1772362800123456789" {
		t.Errorf("timeUnixNano = %s", timestamp)
	}
	if observed, ok := record["observedTimeUnixNano"].(string); !ok || observed != "1772362800999999999" {
		t.Errorf("observedTimeUnixNano = %#v, want the read time as a string", record["observedTimeUnixNano"])
	}
}

// OTLP deviates from protobuf JSON here: IDs are hex, not base64.
func TestOTLPTraceIDsAreHexNotBase64(t *testing.T) {
	const traceID = "000000000000000018c51935df0b93b9"
	const spanID = "18c51935df0b93b9"
	record := onlyRecord(t, encodeOTLP(t, []LogRecord{{
		Timestamp: 1, Body: "x", TraceID: traceID, SpanID: spanID,
	}}))

	if record["traceId"] != traceID {
		t.Errorf("traceId = %#v, want the hex string unchanged", record["traceId"])
	}
	if record["spanId"] != spanID {
		t.Errorf("spanId = %#v, want the hex string unchanged", record["spanId"])
	}
}

func TestOTLPBodyIsAnAnyValue(t *testing.T) {
	record := onlyRecord(t, encodeOTLP(t, []LogRecord{{Timestamp: 1, Body: "hello"}}))
	body, ok := record["body"].(map[string]interface{})
	if !ok {
		t.Fatalf("body = %#v, want an AnyValue object", record["body"])
	}
	if body["stringValue"] != "hello" {
		t.Errorf("body.stringValue = %#v", body["stringValue"])
	}
}

func TestOTLPAttributeTypes(t *testing.T) {
	fields, _ := decodeJSONObject(`{"count":42,"ratio":1.5,"ok":true,"name":"x","tags":{"a":1},"list":[1,"two"]}`)
	record := onlyRecord(t, encodeOTLP(t, []LogRecord{{Timestamp: 1, Body: "x", Attributes: fields}}))

	byKey := map[string]map[string]interface{}{}
	for _, item := range record["attributes"].([]interface{}) {
		entry := item.(map[string]interface{})
		byKey[entry["key"].(string)] = entry["value"].(map[string]interface{})
	}

	// int64 is a string in protobuf JSON; double is a real number.
	if got := byKey["count"]["intValue"]; got != "42" {
		t.Errorf("count = %#v, want the string \"42\"", byKey["count"])
	}
	if got := byKey["ratio"]["doubleValue"]; got != 1.5 {
		t.Errorf("ratio = %#v, want the number 1.5", byKey["ratio"])
	}
	if got := byKey["ok"]["boolValue"]; got != true {
		t.Errorf("ok = %#v", byKey["ok"])
	}
	if got := byKey["name"]["stringValue"]; got != "x" {
		t.Errorf("name = %#v", byKey["name"])
	}
	if _, nested := byKey["tags"]["kvlistValue"]; !nested {
		t.Errorf("tags = %#v, want a kvlistValue", byKey["tags"])
	}
	if _, list := byKey["list"]["arrayValue"]; !list {
		t.Errorf("list = %#v, want an arrayValue", byKey["list"])
	}
}

func TestOTLPLargeIntegerKeepsPrecision(t *testing.T) {
	fields, _ := decodeJSONObject(`{"order_id":9007199254740993}`)
	payload, err := otlpCodec{}.encode([]LogRecord{{Timestamp: 1, Body: "x", Attributes: fields}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(payload), `"intValue":"9007199254740993"`) {
		t.Errorf("large integer lost precision: %s", payload)
	}
}

// A batch spans every container the route matches, so records must be grouped
// by resource rather than emitted as one resourceLogs entry per record.
func TestOTLPGroupsRecordsByResource(t *testing.T) {
	api := map[string]interface{}{"service.name": "api"}
	web := map[string]interface{}{"service.name": "web"}
	payload := encodeOTLP(t, []LogRecord{
		{Timestamp: 1, Body: "a", Resources: api},
		{Timestamp: 2, Body: "b", Resources: web},
		{Timestamp: 3, Body: "c", Resources: api},
	})

	groups := payload["resourceLogs"].([]interface{})
	if len(groups) != 2 {
		t.Fatalf("got %d resourceLogs, want 2 (one per distinct resource)", len(groups))
	}
	counts := map[string]int{}
	for _, group := range groups {
		entry := group.(map[string]interface{})
		attributes := entry["resource"].(map[string]interface{})["attributes"].([]interface{})
		name := attributes[0].(map[string]interface{})["value"].(map[string]interface{})["stringValue"].(string)
		scope := entry["scopeLogs"].([]interface{})[0].(map[string]interface{})
		counts[name] = len(scope["logRecords"].([]interface{}))
	}
	if counts["api"] != 2 || counts["web"] != 1 {
		t.Errorf("grouping = %v, want api:2 web:1", counts)
	}
}

func TestOTLPGroupingIsStableAcrossRuns(t *testing.T) {
	records := []LogRecord{
		{Timestamp: 1, Body: "a", Resources: map[string]interface{}{"service.name": "api", "host.name": "n1"}},
		{Timestamp: 2, Body: "b", Resources: map[string]interface{}{"service.name": "api", "host.name": "n1"}},
	}
	first, _ := otlpCodec{}.encode(records)
	for i := 0; i < 50; i++ {
		again, _ := otlpCodec{}.encode(records)
		if string(again) != string(first) {
			t.Fatalf("encoding is not deterministic at iteration %d", i)
		}
	}
}

func TestOTLPScopeIsIdentified(t *testing.T) {
	payload := encodeOTLP(t, []LogRecord{{Timestamp: 1, Body: "x"}})
	scope := payload["resourceLogs"].([]interface{})[0].(map[string]interface{})["scopeLogs"].([]interface{})[0].(map[string]interface{})["scope"].(map[string]interface{})
	if scope["name"] != "github.com/pavanputhra/logspout-signoz" {
		t.Errorf("scope.name = %#v", scope["name"])
	}
	if scope["version"] != Version {
		t.Errorf("scope.version = %#v, want %s", scope["version"], Version)
	}
}

// A 200 carrying partialSuccess means records were dropped. Reporting delivery
// there would lose them silently.
func TestOTLPPartialSuccessIsAnError(t *testing.T) {
	err := otlpCodec{}.inspect([]byte(`{"partialSuccess":{"rejectedLogRecords":"5","errorMessage":"bad severity"}}`))
	if err == nil {
		t.Fatal("expected an error when records were rejected")
	}
	if !strings.Contains(err.Error(), "5") || !strings.Contains(err.Error(), "bad severity") {
		t.Errorf("error = %v, want the count and reason", err)
	}
}

func TestOTLPFullSuccessBodies(t *testing.T) {
	for _, body := range []string{"", "{}", `{"partialSuccess":{}}`, `{"partialSuccess":{"rejectedLogRecords":"0"}}`} {
		if err := (otlpCodec{}).inspect([]byte(body)); err != nil {
			t.Errorf("inspect(%q) = %v, want nil", body, err)
		}
	}
}

func TestOTLPUnparseableBodyIsNotAFailure(t *testing.T) {
	if err := (otlpCodec{}).inspect([]byte("<html>proxy blurb</html>")); err != nil {
		t.Errorf("a 2xx with an unreadable body should not fail a delivered batch: %v", err)
	}
}

func TestSignozCodecUnchanged(t *testing.T) {
	payload, err := signozCodec{}.encode([]LogRecord{{Timestamp: 7, Body: "x", SeverityText: "info"}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var records []map[string]interface{}
	if err := json.Unmarshal(payload, &records); err != nil {
		t.Fatalf("want a flat array for the SigNoz receiver: %v", err)
	}
	if records[0]["timestamp"] != float64(7) {
		t.Errorf("timestamp = %#v, want a bare number for SigNoz", records[0]["timestamp"])
	}
	if _, leaked := records[0]["Observed"]; leaked {
		t.Error("the observed timestamp must not leak into the SigNoz payload")
	}
}
