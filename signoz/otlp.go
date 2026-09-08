package signoz

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gliderlabs/logspout/router"
)

func init() {
	router.AdapterFactories.Register(NewOTLPAdapter, "otlp")
}

// NewOTLPAdapter returns an adapter that speaks OTLP/HTTP with JSON encoding.
//
// Unlike the signoz adapter, this needs no receiver configuration on the
// collector: OTLP is enabled by default on port 4318, and any OpenTelemetry
// collector accepts it, not only SigNoz's.
//
//	otlp://collector:4318
//	otlp+https://ingest.us.signoz.cloud:443
func NewOTLPAdapter(route *router.Route) (router.LogAdapter, error) {
	return newAdapter(route, "otlp", otlpCodec{}, otlpDefaultPath)
}

// otlpCodec encodes records as an OTLP ExportLogsServiceRequest.
//
// The JSON here follows the protobuf JSON mapping, which has two traps: 64-bit
// integers are encoded as strings, and trace/span IDs are hex rather than the
// base64 that standard protobuf JSON would use — OTLP deviates deliberately.
type otlpCodec struct{}

func (otlpCodec) contentType() string { return "application/json" }

// OTLP JSON wire types. Only the fields this adapter populates are modelled.
type (
	otlpExportRequest struct {
		ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
	}
	otlpResourceLogs struct {
		Resource  otlpResource    `json:"resource"`
		ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
	}
	otlpResource struct {
		Attributes []otlpKeyValue `json:"attributes,omitempty"`
	}
	otlpScopeLogs struct {
		Scope      otlpScope       `json:"scope"`
		LogRecords []otlpLogRecord `json:"logRecords"`
	}
	otlpScope struct {
		Name    string `json:"name"`
		Version string `json:"version,omitempty"`
	}
	otlpLogRecord struct {
		TimeUnixNano         string         `json:"timeUnixNano"`
		ObservedTimeUnixNano string         `json:"observedTimeUnixNano,omitempty"`
		SeverityNumber       int            `json:"severityNumber,omitempty"`
		SeverityText         string         `json:"severityText,omitempty"`
		Body                 otlpAnyValue   `json:"body"`
		Attributes           []otlpKeyValue `json:"attributes,omitempty"`
		TraceID              string         `json:"traceId,omitempty"`
		SpanID               string         `json:"spanId,omitempty"`
		Flags                int            `json:"flags,omitempty"`
	}
	otlpKeyValue struct {
		Key   string       `json:"key"`
		Value otlpAnyValue `json:"value"`
	}
	// otlpAnyValue carries exactly one of these fields.
	otlpAnyValue struct {
		StringValue *string          `json:"stringValue,omitempty"`
		BoolValue   *bool            `json:"boolValue,omitempty"`
		IntValue    *string          `json:"intValue,omitempty"`
		DoubleValue *float64         `json:"doubleValue,omitempty"`
		ArrayValue  *otlpArrayValue  `json:"arrayValue,omitempty"`
		KvlistValue *otlpKvlistValue `json:"kvlistValue,omitempty"`
	}
	otlpArrayValue struct {
		Values []otlpAnyValue `json:"values"`
	}
	otlpKvlistValue struct {
		Values []otlpKeyValue `json:"values"`
	}
)

func (otlpCodec) encode(records []LogRecord) ([]byte, error) {
	// Records sharing a resource must be grouped under one resourceLogs entry;
	// a batch spans every container the route matches, so there is usually more
	// than one.
	order := make([]string, 0, 4)
	groups := make(map[string][]LogRecord, 4)
	for _, record := range records {
		key := resourceKey(record.Resources)
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], record)
	}

	request := otlpExportRequest{ResourceLogs: make([]otlpResourceLogs, 0, len(order))}
	for _, key := range order {
		grouped := groups[key]
		entries := make([]otlpLogRecord, 0, len(grouped))
		for _, record := range grouped {
			entries = append(entries, otlpRecord(record))
		}
		request.ResourceLogs = append(request.ResourceLogs, otlpResourceLogs{
			Resource: otlpResource{Attributes: otlpAttributes(grouped[0].Resources)},
			ScopeLogs: []otlpScopeLogs{{
				Scope:      otlpScope{Name: "github.com/pavanputhra/logspout-signoz", Version: Version},
				LogRecords: entries,
			}},
		})
	}
	return json.Marshal(request)
}

func otlpRecord(record LogRecord) otlpLogRecord {
	observed := record.Observed
	if observed == 0 {
		observed = record.Timestamp
	}
	return otlpLogRecord{
		// fixed64 in protobuf, therefore a string in JSON. A number here is
		// rejected by strict receivers.
		TimeUnixNano:         strconv.FormatInt(record.Timestamp, 10),
		ObservedTimeUnixNano: strconv.FormatInt(observed, 10),
		SeverityNumber:       record.SeverityNumber,
		SeverityText:         record.SeverityText,
		Body:                 otlpString(record.Body),
		Attributes:           otlpAttributes(record.Attributes),
		TraceID:              record.TraceID,
		SpanID:               record.SpanID,
		Flags:                record.TraceFlags,
	}
}

// resourceKey builds a stable identity for a resource map so records can be
// grouped without depending on Go's randomised map order.
func resourceKey(resources map[string]interface{}) string {
	if len(resources) == 0 {
		return ""
	}
	keys := make([]string, 0, len(resources))
	for key := range resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&builder, "%s=%v\x00", key, resources[key])
	}
	return builder.String()
}

func otlpAttributes(values map[string]interface{}) []otlpKeyValue {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	attributes := make([]otlpKeyValue, 0, len(keys))
	for _, key := range keys {
		attributes = append(attributes, otlpKeyValue{Key: key, Value: otlpValue(values[key])})
	}
	return attributes
}

// otlpValue maps a decoded JSON value onto AnyValue, preserving its type.
func otlpValue(value interface{}) otlpAnyValue {
	switch typed := value.(type) {
	case nil:
		return otlpString("")
	case string:
		return otlpString(typed)
	case bool:
		return otlpAnyValue{BoolValue: &typed}
	case json.Number:
		if whole, err := typed.Int64(); err == nil {
			text := strconv.FormatInt(whole, 10)
			return otlpAnyValue{IntValue: &text}
		}
		if real, err := typed.Float64(); err == nil {
			return otlpAnyValue{DoubleValue: &real}
		}
		return otlpString(typed.String())
	case int:
		text := strconv.Itoa(typed)
		return otlpAnyValue{IntValue: &text}
	case int64:
		text := strconv.FormatInt(typed, 10)
		return otlpAnyValue{IntValue: &text}
	case float64:
		return otlpAnyValue{DoubleValue: &typed}
	case []interface{}:
		values := make([]otlpAnyValue, 0, len(typed))
		for _, item := range typed {
			values = append(values, otlpValue(item))
		}
		return otlpAnyValue{ArrayValue: &otlpArrayValue{Values: values}}
	case map[string]interface{}:
		return otlpAnyValue{KvlistValue: &otlpKvlistValue{Values: otlpAttributes(typed)}}
	default:
		return otlpString(fmt.Sprintf("%v", typed))
	}
}

func otlpString(value string) otlpAnyValue {
	return otlpAnyValue{StringValue: &value}
}

// otlpPartialSuccess is the body a collector returns alongside a 200 when it
// accepted the request but dropped records from it.
type otlpPartialSuccess struct {
	PartialSuccess struct {
		RejectedLogRecords json.Number `json:"rejectedLogRecords"`
		ErrorMessage       string      `json:"errorMessage"`
	} `json:"partialSuccess"`
}

// inspect turns a partial success into an error. Treating a 200 as delivery, as
// a naive status check would, silently loses whatever the collector rejected.
func (otlpCodec) inspect(body []byte) error {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var response otlpPartialSuccess
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		// An unreadable body on a 2xx is not worth failing a delivered batch.
		debug("could not parse the collector response:", err)
		return nil
	}
	rejected, err := response.PartialSuccess.RejectedLogRecords.Int64()
	if err != nil || rejected <= 0 {
		return nil
	}
	message := response.PartialSuccess.ErrorMessage
	if message == "" {
		message = "no reason given"
	}
	return fmt.Errorf("collector rejected %d of the records: %s", rejected, message)
}
