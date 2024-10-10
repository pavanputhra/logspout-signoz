package signoz

import (
	"fmt"
	docker "github.com/fsouza/go-dockerclient"
	"os"
	"testing"
	"time"

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
		Data: fmt.Sprintf(`{"level": "info", "message": "JSON message without time", "foo": "bar"}`),
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

	// Send a valid JSON log message
	logStream <- jsonWithoutTime
	logStream <- jsonWithoutLabel
	logStream <- jsonWithLabel
	logStream <- jsonWithEnv
	logStream <- messageWithLabel
	logStream <- messageWithoutLabel

	close(logStream)

	// Wait for a bit to allow the ticker to trigger and send logs
	time.Sleep(8 * time.Second)

}
