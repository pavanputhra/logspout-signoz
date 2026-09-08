package signoz

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gliderlabs/logspout/router"
)

// Config holds everything an Adapter needs to run one route.
//
// Every field is resolved in this order: route option (from the route URI
// query string), then environment variable, then the default. This matches the
// convention used by logspout's own syslog adapter, and it is what makes a
// single logspout process able to serve several signoz:// routes at once.
type Config struct {
	Endpoint      string
	IngestionKey  string
	Env           string
	HostName      string
	ServiceName   string
	ParseJSON     bool
	DetectLevel   bool
	BatchSize     int
	FlushInterval time.Duration
	MaxBuffer     int
	Timeout       time.Duration
	RetryCount    int
	TLSSkipVerify bool
}

const (
	defaultPath          = "/"
	defaultBatchSize     = 100
	defaultFlushInterval = 5 * time.Second
	defaultMaxBuffer     = 10000
	defaultTimeout       = 10 * time.Second
	defaultRetryCount    = 3

	// hostHostnameFile is the conventional mount point for the host's
	// /etc/hostname (-v /etc/hostname:/etc/host_hostname:ro). Inside a
	// container os.Hostname() is just the container ID, so this is the only
	// reliable way to label logs with the machine they came from.
	hostHostnameFile = "/etc/host_hostname"
)

var deprecationOnce sync.Map

// deprecated warns once per process for each legacy setting still in use.
func deprecated(old, replacement string) {
	if _, seen := deprecationOnce.LoadOrStore(old, true); seen {
		return
	}
	log.Printf("signoz: %s is deprecated and will be removed in v3; use %s", old, replacement)
}

// debug logs only when DEBUG is set, matching logspout's own convention.
//
// Anything written to stdout here is itself collected by logspout (it does not
// exclude its own container), shipped to SigNoz, and can feed back into the
// next batch. Per-message or per-flush logging must never be unconditional.
func debug(v ...interface{}) {
	if os.Getenv("DEBUG") != "" {
		log.Println(append([]interface{}{"signoz:"}, v...)...)
	}
}

// option resolves a single setting: route option, then env var, then default.
func option(route *router.Route, name, envName, dfault string) string {
	if route != nil {
		if v, ok := route.Options[name]; ok && v != "" {
			return v
		}
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return dfault
}

func boolOption(route *router.Route, name, envName string, dfault bool) (bool, error) {
	raw := option(route, name, envName, "")
	if raw == "" {
		return dfault, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("option %q: %q is not a boolean", name, raw)
	}
	return v, nil
}

func intOption(route *router.Route, name, envName string, dfault int) (int, error) {
	raw := option(route, name, envName, "")
	if raw == "" {
		return dfault, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("option %q: %q is not a number", name, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("option %q: must be greater than 0, got %d", name, v)
	}
	return v, nil
}

func durationOption(route *router.Route, name, envName string, dfault time.Duration) (time.Duration, error) {
	raw := option(route, name, envName, "")
	if raw == "" {
		return dfault, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("option %q: %q is not a duration (try 5s, 500ms, 1m)", name, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("option %q: must be greater than 0, got %s", name, v)
	}
	return v, nil
}

// NewConfig builds a Config from a route plus the environment.
func NewConfig(route *router.Route) (*Config, error) {
	endpoint, err := resolveEndpoint(route)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Endpoint:     endpoint,
		IngestionKey: option(route, "ingestion_key", "SIGNOZ_INGESTION_KEY", ""),
		Env:          resolveEnv(route),
		HostName:     resolveHostName(route),
		ServiceName:  option(route, "service_name", "SIGNOZ_SERVICE_NAME", ""),
	}

	if cfg.ParseJSON, err = boolOption(route, "parse_json", "SIGNOZ_PARSE_JSON", true); err != nil {
		return nil, err
	}
	// Empty counts as unset, matching logspout's own getopt: "DISABLE_X=" in a
	// compose file is not an opt-in.
	if os.Getenv("DISABLE_JSON_PARSE") != "" {
		// v1 read this into a field it then never used, so JSON parsing always
		// ran. Honouring it now would silently change behaviour on upgrade for
		// anyone who set it, so warn and keep parsing unless they opt out the
		// new way.
		deprecated("DISABLE_JSON_PARSE", "?parse_json=false (it never took effect in v1)")
	}

	if cfg.DetectLevel, err = boolOption(route, "detect_level", "SIGNOZ_DETECT_LEVEL", true); err != nil {
		return nil, err
	}
	if os.Getenv("DISABLE_LOG_LEVEL_STRING_MATCH") != "" {
		deprecated("DISABLE_LOG_LEVEL_STRING_MATCH", "?detect_level=false")
		if route == nil || route.Options["detect_level"] == "" {
			if os.Getenv("SIGNOZ_DETECT_LEVEL") == "" {
				cfg.DetectLevel = false
			}
		}
	}

	if cfg.BatchSize, err = intOption(route, "batch_size", "SIGNOZ_BATCH_SIZE", defaultBatchSize); err != nil {
		return nil, err
	}
	if cfg.MaxBuffer, err = intOption(route, "max_buffer", "SIGNOZ_MAX_BUFFER", defaultMaxBuffer); err != nil {
		return nil, err
	}
	if cfg.FlushInterval, err = durationOption(route, "flush_interval", "SIGNOZ_FLUSH_INTERVAL", defaultFlushInterval); err != nil {
		return nil, err
	}
	if cfg.Timeout, err = durationOption(route, "timeout", "SIGNOZ_TIMEOUT", defaultTimeout); err != nil {
		return nil, err
	}
	if cfg.RetryCount, err = intOption(route, "retry_count", "SIGNOZ_RETRY_COUNT", defaultRetryCount); err != nil {
		return nil, err
	}
	if cfg.TLSSkipVerify, err = boolOption(route, "tls_skip_verify", "SIGNOZ_TLS_SKIP_VERIFY", false); err != nil {
		return nil, err
	}

	if cfg.MaxBuffer < cfg.BatchSize {
		return nil, fmt.Errorf("max_buffer (%d) must be at least batch_size (%d)", cfg.MaxBuffer, cfg.BatchSize)
	}
	return cfg, nil
}

// resolveEndpoint builds the destination URL from the route, which is where
// logspout puts it: `signoz://host:port` gives route.Address, and the
// `+transport` suffix gives the scheme (`signoz+https://...`).
//
// logspout's AddFromURI keeps only u.Host, discarding any path, so the path has
// to arrive as an option: `?path=/logs/json` for SigNoz Cloud.
func resolveEndpoint(route *router.Route) (string, error) {
	var fromRoute string
	if route != nil && route.Address != "" {
		scheme := "http"
		if route.Adapter != "" {
			scheme = route.AdapterTransport("http")
		}
		switch scheme {
		case "http", "https":
		default:
			return "", fmt.Errorf("unsupported transport %q: use signoz:// or signoz+https://", scheme)
		}

		path := option(route, "path", "SIGNOZ_PATH", defaultPath)
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		fromRoute = scheme + "://" + route.Address + path
	}

	// v1 read the endpoint from SIGNOZ_LOG_ENDPOINT and ignored the route
	// address entirely. The v1 README even documented an example where the two
	// disagree, so the env var has to keep winning or upgrading would silently
	// redirect those users' logs.
	if legacy := os.Getenv("SIGNOZ_LOG_ENDPOINT"); legacy != "" {
		deprecated("SIGNOZ_LOG_ENDPOINT", "the route address, e.g. signoz://otel-collector:8082")
		if fromRoute != "" && !sameEndpoint(legacy, fromRoute) {
			log.Printf("signoz: SIGNOZ_LOG_ENDPOINT=%s overrides the route address (%s); "+
				"drop the env var to use the route", legacy, fromRoute)
		}
		if _, err := url.Parse(legacy); err != nil {
			return "", fmt.Errorf("SIGNOZ_LOG_ENDPOINT %q is not a valid URL: %w", legacy, err)
		}
		return legacy, nil
	}

	if fromRoute == "" {
		return "", fmt.Errorf("no destination: use signoz://host:port")
	}
	return fromRoute, nil
}

func sameEndpoint(a, b string) bool {
	ua, erra := url.Parse(a)
	ub, errb := url.Parse(b)
	if erra != nil || errb != nil {
		return a == b
	}
	pathA, pathB := ua.Path, ub.Path
	if pathA == "" {
		pathA = "/"
	}
	if pathB == "" {
		pathB = "/"
	}
	return ua.Scheme == ub.Scheme && ua.Host == ub.Host && pathA == pathB
}

func resolveEnv(route *router.Route) string {
	if v := option(route, "env", "SIGNOZ_ENV", ""); v != "" {
		return v
	}
	if v := os.Getenv("ENV"); v != "" {
		deprecated("ENV", "SIGNOZ_ENV or ?env=prod")
		return v
	}
	return ""
}

func resolveHostName(route *router.Route) string {
	if v := option(route, "host_name", "SIGNOZ_HOST_NAME", ""); v != "" {
		return v
	}
	if b, err := os.ReadFile(hostHostnameFile); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name
		}
	}
	return ""
}
