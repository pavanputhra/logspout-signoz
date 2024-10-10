package signoz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/gliderlabs/logspout/router"
)

var logLevelMap = map[string]int{
	"TRACE":   1,
	"DEBUG":   5,
	"INFO":    9,
	"WARN":    13,
	"WARNING": 13,
	"ERROR":   17,
	"FATAL":   21,
}

var standardJsonAttributeKeys = []string{"timestamp", "level", "message", "service", "namespace", "env", "environment"}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func init() {
	router.AdapterFactories.Register(NewSignozAdapter, "signoz")
}

var funcs = template.FuncMap{
	"toJSON": func(value interface{}) string {
		bytes, err := json.Marshal(value)
		if err != nil {
			log.Println("error marshaling to JSON: ", err)
			return "null"
		}
		return string(bytes)
	},
}

func parseJSON(s string) interface{} {
	var result interface{} // This can hold any valid JSON structure
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil // If JSON is invalid, return nil
	}
	return result // Return the parsed JSON
}

// NewSignozAdapter returns a configured signoz.Adapter
func NewSignozAdapter(route *router.Route) (router.LogAdapter, error) {

	autoParseJson := true
	if _, exists := os.LookupEnv("DISABLE_JSON_PARSE"); exists {
		autoParseJson = false
	}

	autoLogLevelStringMatch := true
	if _, exists := os.LookupEnv("DISABLE_LOG_LEVEL_STRING_MATCH"); exists {
		autoLogLevelStringMatch = false
	}

	envValue, exists := os.LookupEnv("ENV")
	if !exists {
		envValue = ""
	}
	return &Adapter{
		route:                   route,
		autoParseJson:           autoParseJson,
		autoLogLevelStringMatch: autoLogLevelStringMatch,
		env:                     envValue,
	}, nil
}

// Adapter is a simple adapter that streams log output to a connection without any templating
type Adapter struct {
	//conn  net.Conn
	route                   *router.Route
	autoParseJson           bool
	autoLogLevelStringMatch bool
	env                     string
}

type LogMessage struct {
	Timestamp int `json:"timestamp"`
	//TraceID        string            `json:"trace_id"`
	//SpanID         string            `json:"span_id"`
	//TraceFlags     int               `json:"trace_flags"`
	SeverityText   string            `json:"severity_text"`
	SeverityNumber int               `json:"severity_number"`
	Attributes     map[string]string `json:"attributes"`
	Resources      map[string]string `json:"resources"`
	Message        string            `json:"message"`
}

func (a *Adapter) Stream(logstream chan *router.Message) {
	var buffer []LogMessage
	var mu sync.Mutex
	ticker := time.NewTicker(5 * time.Second)
	//defer ticker.Stop()

	go func() {
		for range ticker.C {
			var temp []LogMessage
			mu.Lock()
			if len(buffer) > 0 {
				temp = append(temp, buffer...)
				buffer = []LogMessage{}
			}
			mu.Unlock()
			if len(temp) > 0 {
				err := sendLogs(temp)
				if err != nil {
					log.Println("Error sending logs:", err)
				}
			}
		}
	}()

	var logMessage LogMessage
	for message := range logstream {

		level := "info"
		leverNumber := logLevelMap[strings.ToUpper(level)]

		serviceName := message.Container.Config.Image
		if serviceNameFromLabel, exists := message.Container.Config.Labels["com.docker.compose.service"]; exists {
			serviceName = serviceNameFromLabel
		}
		logMessage = LogMessage{
			Timestamp: int(message.Time.Unix()),
			//TraceID:        "0", // replace with actual data
			//SpanID:         "0", // replace with actual data
			//TraceFlags:     0,   // replace with actual data
			SeverityText:   level,
			SeverityNumber: leverNumber,
			Attributes:     map[string]string{},
			Resources: map[string]string{
				"service.name": serviceName,
			},
			Message: message.Data,
		}
		if a.env != "" {
			logMessage.Resources["deployment.environment"] = a.env
		}

		jsonInterface := parseJSON(message.Data)
		if jsonInterface != nil {
			jsonMap := jsonInterface.(map[string]interface{})

			timestamp, err := time.Parse(time.RFC3339, jsonMap["timestamp"].(string))
			if err == nil {
				logMessage.Timestamp = int(timestamp.Unix())
			}

			if jsonMap["level"] != nil {
				level = jsonMap["level"].(string)
				leverNumber := logLevelMap[strings.ToUpper(level)]
				logMessage.SeverityText = level
				logMessage.SeverityNumber = leverNumber
			}

			logMessage.Message = jsonMap["message"].(string)

			if jsonMap["env"] != nil {
				logMessage.Resources["deployment.environment"] = jsonMap["env"].(string)
			}
			if jsonMap["environment"] != nil {
				logMessage.Resources["deployment.environment"] = jsonMap["environment"].(string)
			}

			if jsonMap["service"] != nil {
				logMessage.Resources["service.name"] = jsonMap["service"].(string)
			}
			if jsonMap["namespace"] != nil {
				logMessage.Resources["namespace"] = jsonMap["namespace"].(string)
			}
			// Get loop through non standard keys and save them as attributes inside logMessage
			for key, value := range jsonMap {
				if !contains(standardJsonAttributeKeys, key) {
					logMessage.Attributes[key] = fmt.Sprintf("%v", value)
				}
			}
		} else {
			if a.autoLogLevelStringMatch {
				for level, number := range logLevelMap {
					if strings.Contains(message.Data, level) {
						logMessage.SeverityText = strings.ToLower(level)
						logMessage.SeverityNumber = number
						break
					}
				}
			}
		}

		mu.Lock()
		buffer = append(buffer, logMessage) // Add log to buffer
		mu.Unlock()
	}
}

func sendLogs(logs []LogMessage) error {
	// Convert logs to JSON
	data, err := json.Marshal(logs)
	if err != nil {
		return err
	}

	signozLogEndpoint := os.Getenv("SIGNOZ_LOG_ENDPOINT")
	if signozLogEndpoint == "" {
		signozLogEndpoint = "http://localhost:8082"
	}

	// Send HTTP POST request
	fmt.Println("Sending logs to: ", signozLogEndpoint)
	resp, err := http.Post(signozLogEndpoint, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send logs, status: %s", resp.Status)
	}
	return nil
}
