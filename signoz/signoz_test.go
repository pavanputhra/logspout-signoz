package signoz

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/logspout/router"
)

//func TestContains(t *testing.T) {
//	tests := []struct {
//		slice []string
//		item  string
//		want  bool
//	}{
//		{[]string{"foo", "bar"}, "foo", true},
//		{[]string{"foo", "bar"}, "baz", false},
//		{[]string{}, "baz", false},
//	}
//
//	for _, tt := range tests {
//		t.Run(tt.item, func(t *testing.T) {
//			got := contains(tt.slice, tt.item)
//			if got != tt.want {
//				t.Errorf("contains(%v, %q) = %v; want %v", tt.slice, tt.item, got, tt.want)
//			}
//		})
//	}
//}

//func TestParseJSON(t *testing.T) {
//	tests := []struct {
//		input string
//		want  interface{}
//	}{
//		{`{"key": "value"}`, map[string]interface{}{"key": "value"}},
//		{`invalid json`, nil},
//	}
//
//	for _, tt := range tests {
//		t.Run(tt.input, func(t *testing.T) {
//			got := parseJSON(tt.input)
//			if got == nil && tt.want != nil {
//				t.Errorf("parseJSON(%q) = nil; want %v", tt.input, tt.want)
//			} else if got != nil && tt.want == nil {
//				t.Errorf("parseJSON(%q) = %v; want nil", tt.input, got)
//			} else if !reflect.DeepEqual(got, tt.want) {
//				t.Errorf("parseJSON(%q) = %v; want %v", tt.input, got, tt.want)
//			}
//		})
//	}
//}

func TestNewSignozAdapter(t *testing.T) {
	route := &router.Route{} // Mock route

	os.Setenv("ENV", "test")
	defer os.Unsetenv("ENV")

	adapterLog, err := NewSignozAdapter(route)
	adapter := adapterLog.(*Adapter)
	if err != nil {
		t.Fatalf("NewSignozAdapter() error = %v", err)
	}

	if adapter.env != "test" {
		t.Errorf("NewSignozAdapter() env = %v; want test", adapter.env)
	}
	if !adapter.autoParseJson {
		t.Errorf("NewSignozAdapter() autoParseJson = %v; want true", adapter.autoParseJson)
	}
	if !adapter.autoLogLevelStringMatch {
		t.Errorf("NewSignozAdapter() autoLogLevelStringMatch = %v; want true", adapter.autoLogLevelStringMatch)
	}
}

func TestStream(t *testing.T) {
	// Create a mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify content type
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		// Read and parse the request body
		var receivedLogs []LogMessage
		if err := json.NewDecoder(r.Body).Decode(&receivedLogs); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
			return
		}

		// Verify we received logs
		if len(receivedLogs) == 0 {
			t.Error("Expected to receive logs, got empty array")
			return
		}

		expectedLogs := 7
		if len(receivedLogs) != expectedLogs {
			t.Errorf("Expected to receive %d logs, got %d", expectedLogs, len(receivedLogs))
			return
		}

		logMessage1 := receivedLogs[0]
		if logMessage1.Timestamp <= 0 {
			t.Errorf("Expected timestamp: > 0, got: %d", logMessage1.Timestamp)
		}

		logMessage2 := receivedLogs[1]
		if logMessage2.Resources["service.name"] != "serviceImage" {
			t.Errorf("Expected service.name: serviceImage, got: %s", logMessage2.Resources["service.name"])
		}

		logMessage3 := receivedLogs[2]
		if logMessage3.Resources["service.name"] == "serviceImage" {
			t.Errorf("service.name should be serviceImage")
		}

		logMessage4 := receivedLogs[3]
		if logMessage4.Resources["deployment.environment"] != "jsonEnv" {
			t.Errorf("Expected deployment.environment: jsonEnv, got: %s", logMessage4.Resources["deployment.environment"])
		}

		logMessage5 := receivedLogs[4]
		if logMessage5.SeverityText != "warn" {
			t.Errorf("Expected severity_text: warn, got: %s", logMessage5.SeverityText)
		}
		if logMessage5.SeverityNumber != 13 {
			t.Errorf("Expected severity_number: 13, got: %d", logMessage5.SeverityNumber)
		}

		logMessage6 := receivedLogs[5]
		if logMessage6.SeverityText != "debug" {
			t.Errorf("Expected severity_text: debug, got: %s", logMessage6.SeverityText)
		}
		if logMessage6.SeverityNumber != 5 {
			t.Errorf("Expected severity_number: 5, got: %d", logMessage6.SeverityNumber)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Set the mock server URL as the endpoint
	os.Setenv("SIGNOZ_LOG_ENDPOINT", server.URL)
	defer os.Unsetenv("SIGNOZ_LOG_ENDPOINT")

	route := &router.Route{}
	adapter, err := NewSignozAdapter(route)
	if err != nil {
		t.Fatalf("NewSignozAdapter() error = %v", err)
	}

	logStream := make(chan *router.Message, 1)

	go adapter.Stream(logStream)

	jsonWithoutTime := &router.Message{
		Container: &docker.Container{
			ID: "test",
			Config: &docker.Config{
				Labels: map[string]string{
					"com.docker.compose.service": "serviceLabel",
				},
			},
		},
		Data: `{"level": "info", "message": "JSON message without time", "foo": "bar"}`,
		Time: time.Now(),
	}

	jsonWithLabel := &router.Message{
		Container: &docker.Container{
			ID: "test",
			Config: &docker.Config{
				Labels: map[string]string{
					"com.docker.compose.service": "serviceLabel",
				},
			},
		},
		Data: fmt.Sprintf(`{"timestamp": "%s", "level": "info", "message": "JSON message info", "foo": "bar"}`, time.Now().Format(time.RFC3339)),
		Time: time.Now(),
	}

	jsonWithoutLabel := &router.Message{
		Container: &docker.Container{
			ID: "test",
			Config: &docker.Config{
				Image:  "serviceImage",
				Labels: map[string]string{},
			},
		},
		Data: fmt.Sprintf(`{"timestamp": "%s", "level": "fatal", "message": "JSON message fatal", "foo": "zoo"}`, time.Now().Format(time.RFC3339)),
		Time: time.Now(),
	}

	jsonWithEnv := &router.Message{
		Container: &docker.Container{
			ID: "test",
			Config: &docker.Config{
				Image:  "serviceImage",
				Labels: map[string]string{},
			},
		},
		Data: fmt.Sprintf(`{"timestamp": "%s", "env": "jsonEnv", "level": "info", "message": "JSON message env", "foo": "zoo1"}`, time.Now().Format(time.RFC3339)),
		Time: time.Now(),
	}

	messageWithLabel := &router.Message{
		Container: &docker.Container{
			ID: "test",
			Config: &docker.Config{
				Labels: map[string]string{
					"com.docker.compose.service": "serviceLabel",
				},
			},
		},
		Data: "[WARN] String that is not JSON",
		Time: time.Now(),
	}

	messageWithoutLabel := &router.Message{
		Container: &docker.Container{
			ID: "test",
			Config: &docker.Config{
				Image:  "serviceImage",
				Labels: map[string]string{},
			},
		},
		Data: "[DEBUG] String that is not JSON",
		Time: time.Now(),
	}

	// Add a test message that should crash the adapter due to non-string "level"
	jsonLogWithIntLevel := &router.Message{
		Container: &docker.Container{
			ID: "test",
			Config: &docker.Config{
				Image:  "serviceImage",
				Labels: map[string]string{},
			},
		},
		Data: `{"level":40,"time":"2025-04-20T20:40:16.188Z","pid":1}`,
		Time: time.Now(),
	}

	// Send a valid JSON log message
	logStream <- jsonWithoutTime
	logStream <- jsonWithoutLabel
	logStream <- jsonWithLabel
	logStream <- jsonWithEnv
	logStream <- messageWithLabel
	logStream <- messageWithoutLabel
	logStream <- jsonLogWithIntLevel

	close(logStream)

	// Wait for a bit to allow the ticker to trigger and send logs
	time.Sleep(8 * time.Second)
}

func TestContainerFiltering(t *testing.T) {
	tests := []struct {
		name          string
		routeOptions  map[string]string
		message       *router.Message
		shouldProcess bool
	}{
		{
			name: "Filter by container ID",
			routeOptions: map[string]string{
				"filter.id": "test-container-id",
			},
			message: &router.Message{
				Container: &docker.Container{
					ID: "test-container-id",
					Config: &docker.Config{
						Labels: map[string]string{},
					},
				},
				Source: "stdout",
			},
			shouldProcess: true,
		},
		{
			name: "Filter by container name with *foo pattern",
			routeOptions: map[string]string{
				"filter.name": "*_db",
			},
			message: &router.Message{
				Container: &docker.Container{
					Name: "postgres_db",
					Config: &docker.Config{
						Labels: map[string]string{},
					},
				},
				Source: "stdout",
			},
			shouldProcess: true,
		},
		{
			name: "Filter by container name with foo* pattern",
			routeOptions: map[string]string{
				"filter.name": "test_*",
			},
			message: &router.Message{
				Container: &docker.Container{
					Name: "test_container",
					Config: &docker.Config{
						Labels: map[string]string{},
					},
				},
				Source: "stdout",
			},
			shouldProcess: true,
		},
		{
			name: "Filter by container name with *foo pattern should reject non-matching",
			routeOptions: map[string]string{
				"filter.name": "*_db",
			},
			message: &router.Message{
				Container: &docker.Container{
					Name: "postgres_app",
					Config: &docker.Config{
						Labels: map[string]string{},
					},
				},
				Source: "stdout",
			},
			shouldProcess: false,
		},
		{
			name: "Filter by container name with foo* pattern should reject non-matching",
			routeOptions: map[string]string{
				"filter.name": "test_*",
			},
			message: &router.Message{
				Container: &docker.Container{
					Name: "other_container",
					Config: &docker.Config{
						Labels: map[string]string{},
					},
				},
				Source: "stdout",
			},
			shouldProcess: false,
		},
		{
			name: "Filter by container name with leading slash",
			routeOptions: map[string]string{
				"filter.name": "*_db",
			},
			message: &router.Message{
				Container: &docker.Container{
					Name: "/postgres_db",
					Config: &docker.Config{
						Labels: map[string]string{},
					},
				},
				Source: "stdout",
			},
			shouldProcess: true,
		},
		{
			name: "Filter by sources",
			routeOptions: map[string]string{
				"filter.sources": "stdout,stderr",
			},
			message: &router.Message{
				Container: &docker.Container{
					Config: &docker.Config{
						Labels: map[string]string{},
					},
				},
				Source: "stdout",
			},
			shouldProcess: true,
		},
		{
			name: "Filter by labels",
			routeOptions: map[string]string{
				"filter.labels": "app:test_*,env:prod",
			},
			message: &router.Message{
				Container: &docker.Container{
					Config: &docker.Config{
						Labels: map[string]string{
							"app": "test_app",
							"env": "prod",
						},
					},
				},
				Source: "stdout",
			},
			shouldProcess: true,
		},
		{
			name: "Multiple filters",
			routeOptions: map[string]string{
				"filter.name":    "test_*",
				"filter.sources": "stdout",
				"filter.labels":  "app:test_*",
			},
			message: &router.Message{
				Container: &docker.Container{
					Name: "test_container",
					Config: &docker.Config{
						Labels: map[string]string{
							"app": "test_app",
						},
					},
				},
				Source: "stdout",
			},
			shouldProcess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := &router.Route{
				Options: tt.routeOptions,
			}

			adapter, err := NewSignozAdapter(route)
			if err != nil {
				t.Fatalf("NewSignozAdapter() error = %v", err)
			}

			shouldProcess := adapter.(*Adapter).shouldProcessMessage(tt.message)
			if shouldProcess != tt.shouldProcess {
				t.Errorf("shouldProcessMessage() = %v, want %v", shouldProcess, tt.shouldProcess)
			}
		})
	}
}
