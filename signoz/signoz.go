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

//var funcs = template.FuncMap{
//	"toJSON": func(value interface{}) string {
//		bytes, err := json.Marshal(value)
//		if err != nil {
//			log.Println("error marshaling to JSON: ", err)
//			return "null"
//		}
//		return string(bytes)
//	},
//}

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

	// Parse filter parameters from route.Address
	filterName := route.Options["filter.name"]
	filterID := route.Options["filter.id"]
	filterSources := strings.Split(route.Options["filter.sources"], ",")
	filterLabels := make(map[string]string)

	if labelsStr := route.Options["filter.labels"]; labelsStr != "" {
		labelPairs := strings.Split(labelsStr, ",")
		for _, pair := range labelPairs {
			parts := strings.Split(pair, ":")
			if len(parts) == 2 {
				filterLabels[parts[0]] = parts[1]
			}
		}
	}

	return &Adapter{
		route:                   route,
		autoParseJson:           autoParseJson,
		autoLogLevelStringMatch: autoLogLevelStringMatch,
		env:                     envValue,
		filterName:              filterName,
		filterID:                filterID,
		filterSources:           filterSources,
		filterLabels:            filterLabels,
	}, nil
}

// Adapter is a simple adapter that streams log output to a connection without any templating
type Adapter struct {
	//conn  net.Conn
	route                   *router.Route
	autoParseJson           bool
	autoLogLevelStringMatch bool
	env                     string
	filterName              string
	filterID                string
	filterSources           []string
	filterLabels            map[string]string
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

func (a *Adapter) Stream(logStream chan *router.Message) {
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
	for message := range logStream {

		// Apply filters
		if !a.shouldProcessMessage(message) {
			continue
		}

		level := "info"
		leverNumber := logLevelMap[strings.ToUpper(level)]

		serviceName := message.Container.Config.Image
		if serviceNameFromComposeLabel, exists := message.Container.Config.Labels["com.docker.compose.service"]; exists {
			serviceName = serviceNameFromComposeLabel
		}
		if serviceNameFromSwarmLabel, exists := message.Container.Config.Labels["com.docker.swarm.task.name"]; exists {
			serviceName = serviceNameFromSwarmLabel
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
			if jsonMap, ok := jsonInterface.(map[string]interface{}); ok {
				if jsonMap["timestamp"] != nil {
					timestamp, err := time.Parse(time.RFC3339, jsonMap["timestamp"].(string))
					if err == nil {
						logMessage.Timestamp = int(timestamp.Unix())
					}
				}

				if jsonMap["level"] != nil {
					level = jsonMap["level"].(string)
					leverNumber := logLevelMap[strings.ToUpper(level)]
					logMessage.SeverityText = level
					logMessage.SeverityNumber = leverNumber
				}

				if jsonMap["message"] != nil {
					logMessage.Message = jsonMap["message"].(string)
				}

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

// shouldProcessMessage checks if a message should be processed based on filter criteria
func (a *Adapter) shouldProcessMessage(message *router.Message) bool {
	// Filter by container ID
	if a.filterID != "" && message.Container.ID != a.filterID {
		return false
	}

	// Filter by container name
	if a.filterName != "" && !a.matchesFilterPattern(message.Container.Name, a.filterName) {
		return false
	}

	// Filter by sources
	if len(a.filterSources) > 0 && a.filterSources[0] != "" {
		sourceFound := false
		for _, source := range a.filterSources {
			if message.Source == source {
				sourceFound = true
				break
			}
		}
		if !sourceFound {
			return false
		}
	}

	// Filter by labels
	if len(a.filterLabels) > 0 {
		for labelKey, labelPattern := range a.filterLabels {
			labelValue, exists := message.Container.Config.Labels[labelKey]
			if !exists {
				return false
			}
			if !a.matchesFilterPattern(labelValue, labelPattern) {
				return false
			}
		}
	}

	return true
}

// matchesFilterPattern checks if the container name matches the given pattern
func (a *Adapter) matchesFilterPattern(input, filterPattern string) bool {
	if strings.HasPrefix(filterPattern, "*") && strings.HasSuffix(filterPattern, "*") {
		return strings.Contains(input, filterPattern[1:len(filterPattern)-1])
	} else if strings.HasPrefix(filterPattern, "*") {
		return strings.HasSuffix(input, filterPattern[1:])
	} else if strings.HasSuffix(filterPattern, "*") {
		return strings.HasPrefix(input, filterPattern[:len(filterPattern)-1])
	}
	return input == filterPattern
}
