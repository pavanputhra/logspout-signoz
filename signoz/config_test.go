package signoz

import (
	"strings"
	"testing"
	"time"

	"github.com/gliderlabs/logspout/router"
)

// clearLegacyEnv removes the v1 environment so a test sees defaults.
func clearLegacyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SIGNOZ_LOG_ENDPOINT", "ENV", "DISABLE_JSON_PARSE", "DISABLE_LOG_LEVEL_STRING_MATCH",
		"SIGNOZ_ENV", "SIGNOZ_PATH", "SIGNOZ_INGESTION_KEY", "SIGNOZ_PARSE_JSON",
		"SIGNOZ_DETECT_LEVEL", "SIGNOZ_BATCH_SIZE", "SIGNOZ_FLUSH_INTERVAL",
		"SIGNOZ_MAX_BUFFER", "SIGNOZ_TIMEOUT", "SIGNOZ_RETRY_COUNT",
		"SIGNOZ_SERVICE_NAME", "SIGNOZ_HOST_NAME", "SIGNOZ_TLS_SKIP_VERIFY",
	} {
		t.Setenv(key, "")
	}
}

func route(address string, options map[string]string) *router.Route {
	if options == nil {
		options = map[string]string{}
	}
	return &router.Route{Adapter: "signoz", Address: address, Options: options}
}

// The central v1 defect: the route address was ignored in favour of an env var.
func TestEndpointComesFromRouteAddress(t *testing.T) {
	clearLegacyEnv(t)
	cfg, err := NewConfig(route("otel-collector:8082", nil))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Endpoint != "http://otel-collector:8082/" {
		t.Errorf("Endpoint = %q, want http://otel-collector:8082/", cfg.Endpoint)
	}
}

func TestEndpointHTTPSTransportAndPath(t *testing.T) {
	clearLegacyEnv(t)
	r := route("ingest.us.signoz.cloud:443", map[string]string{"path": "/logs/json"})
	r.Adapter = "signoz+https"

	cfg, err := NewConfig(r)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Endpoint != "https://ingest.us.signoz.cloud:443/logs/json" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
}

func TestEndpointRejectsUnknownTransport(t *testing.T) {
	clearLegacyEnv(t)
	r := route("host:8082", nil)
	r.Adapter = "signoz+udp"
	if _, err := NewConfig(r); err == nil {
		t.Fatal("expected an error for signoz+udp")
	}
}

func TestEndpointRequiresADestination(t *testing.T) {
	clearLegacyEnv(t)
	if _, err := NewConfig(route("", nil)); err == nil {
		t.Fatal("expected an error when no address and no legacy env var are set")
	}
}

// The v1 README documented an example where SIGNOZ_LOG_ENDPOINT and the route
// address disagree. Existing users are on :latest, so the env var has to keep
// winning or upgrading would silently redirect their logs.
func TestLegacyEndpointEnvStillWins(t *testing.T) {
	clearLegacyEnv(t)
	t.Setenv("SIGNOZ_LOG_ENDPOINT", "http://1.2.3.4:8082")

	cfg, err := NewConfig(route("localhost:8082", nil))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Endpoint != "http://1.2.3.4:8082" {
		t.Errorf("Endpoint = %q, want the legacy env var to win", cfg.Endpoint)
	}
}

func TestLegacyEnvVarStillSetsEnvironment(t *testing.T) {
	clearLegacyEnv(t)
	t.Setenv("ENV", "prod")

	cfg, err := NewConfig(route("host:8082", nil))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Env != "prod" {
		t.Errorf("Env = %q, want prod", cfg.Env)
	}
}

func TestRouteOptionBeatsEnvVar(t *testing.T) {
	clearLegacyEnv(t)
	t.Setenv("SIGNOZ_ENV", "from-env")

	cfg, err := NewConfig(route("host:8082", map[string]string{"env": "from-route"}))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Env != "from-route" {
		t.Errorf("Env = %q, want the route option to win", cfg.Env)
	}
}

func TestDefaults(t *testing.T) {
	clearLegacyEnv(t)
	cfg, err := NewConfig(route("host:8082", nil))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if !cfg.ParseJSON || !cfg.DetectLevel {
		t.Error("JSON parsing and level detection should default to on")
	}
	if cfg.BatchSize != defaultBatchSize || cfg.MaxBuffer != defaultMaxBuffer {
		t.Errorf("batch/buffer defaults = %d/%d", cfg.BatchSize, cfg.MaxBuffer)
	}
	if cfg.FlushInterval != defaultFlushInterval || cfg.Timeout != defaultTimeout {
		t.Errorf("flush/timeout defaults = %s/%s", cfg.FlushInterval, cfg.Timeout)
	}
}

func TestTuningOptions(t *testing.T) {
	clearLegacyEnv(t)
	cfg, err := NewConfig(route("host:8082", map[string]string{
		"batch_size":     "10",
		"flush_interval": "250ms",
		"max_buffer":     "100",
		"timeout":        "2s",
		"retry_count":    "1",
		"parse_json":     "false",
	}))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.BatchSize != 10 || cfg.MaxBuffer != 100 || cfg.RetryCount != 1 {
		t.Errorf("got batch=%d buffer=%d retry=%d", cfg.BatchSize, cfg.MaxBuffer, cfg.RetryCount)
	}
	if cfg.FlushInterval != 250*time.Millisecond || cfg.Timeout != 2*time.Second {
		t.Errorf("got flush=%s timeout=%s", cfg.FlushInterval, cfg.Timeout)
	}
	if cfg.ParseJSON {
		t.Error("parse_json=false was ignored")
	}
}

// v1 accepted anything and silently misbehaved; bad config should stop the
// route at creation, where logspout reports it.
func TestInvalidOptionsAreRejected(t *testing.T) {
	clearLegacyEnv(t)
	for name, options := range map[string]map[string]string{
		"non-numeric batch_size": {"batch_size": "lots"},
		"zero batch_size":        {"batch_size": "0"},
		"bad duration":           {"flush_interval": "5 seconds"},
		"bad bool":               {"parse_json": "maybe"},
		"buffer below batch":     {"batch_size": "100", "max_buffer": "10"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewConfig(route("host:8082", options)); err == nil {
				t.Errorf("expected an error for %v", options)
			}
		})
	}
}

// v1 read DISABLE_JSON_PARSE into a field it never used, so JSON parsing always
// ran. Honouring it now would change behaviour under people on :latest.
func TestLegacyDisableJSONParseStaysInert(t *testing.T) {
	clearLegacyEnv(t)
	t.Setenv("DISABLE_JSON_PARSE", "true")

	cfg, err := NewConfig(route("host:8082", nil))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if !cfg.ParseJSON {
		t.Error("DISABLE_JSON_PARSE never worked in v1; it must not start working silently")
	}
}

func TestLegacyDisableLevelMatchIsHonoured(t *testing.T) {
	clearLegacyEnv(t)
	t.Setenv("DISABLE_LOG_LEVEL_STRING_MATCH", "true")

	cfg, err := NewConfig(route("host:8082", nil))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.DetectLevel {
		t.Error("DISABLE_LOG_LEVEL_STRING_MATCH worked in v1 and must keep working")
	}
}

// End-to-end through logspout's own URI parser: this is the shape the adapter
// actually receives in production.
func TestRouteURIWiring(t *testing.T) {
	clearLegacyEnv(t)
	const uri = "signoz://otel-collector:8082?env=prod&batch_size=25&filter.name=my-proj-*"

	if err := router.Routes.AddFromURI(uri); err != nil {
		t.Fatalf("AddFromURI: %v", err)
	}
	routes, _ := router.Routes.GetAll()
	var got *router.Route
	for _, r := range routes {
		if r.Address == "otel-collector:8082" {
			got = r
		}
	}
	if got == nil {
		t.Fatal("route was not registered")
	}
	// Deliberately not removed: RouteManager.Remove sends on an unbuffered
	// closer channel that only a running route reads, so it would deadlock
	// here. The route is inert in a test binary.

	cfg, err := NewConfig(got)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Endpoint != "http://otel-collector:8082/" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.Env != "prod" || cfg.BatchSize != 25 {
		t.Errorf("env = %q, batch = %d", cfg.Env, cfg.BatchSize)
	}

	// Filtering belongs to logspout: it lands on the Route, never in Options,
	// which is why the v1 adapter's own filter code could never fire.
	if got.FilterName != "my-proj-*" {
		t.Errorf("FilterName = %q, want my-proj-*", got.FilterName)
	}
	if got.Options["filter.name"] != "" {
		t.Errorf("filter.name leaked into Options as %q", got.Options["filter.name"])
	}
	if !got.MatchContainer("abc123", "my-proj-web-1", nil) {
		t.Error("logspout should route a matching container")
	}
	if got.MatchContainer("abc123", "other-db-1", nil) {
		t.Error("logspout should filter out a non-matching container")
	}
}

func TestHostNameFromMountedFile(t *testing.T) {
	clearLegacyEnv(t)
	cfg, err := NewConfig(route("host:8082", map[string]string{"host_name": "node-1"}))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.HostName != "node-1" {
		t.Errorf("HostName = %q", cfg.HostName)
	}
}

func TestEndpointNormalisesPathWithoutLeadingSlash(t *testing.T) {
	clearLegacyEnv(t)
	cfg, err := NewConfig(route("host:8082", map[string]string{"path": "logs/json"}))
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if !strings.HasSuffix(cfg.Endpoint, "/logs/json") {
		t.Errorf("Endpoint = %q, want a normalised path", cfg.Endpoint)
	}
}
