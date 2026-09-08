package signoz

import (
	"testing"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/logspout/router"
)

func containerMessage(name, image string, labels map[string]string) *router.Message {
	return &router.Message{
		Container: &docker.Container{
			ID:     "abcdef0123456789",
			Name:   name,
			Config: &docker.Config{Image: image, Labels: labels},
		},
		Data: "x",
	}
}

func TestServiceNameSources(t *testing.T) {
	tests := []struct {
		name    string
		message *router.Message
		want    string
	}{
		{
			name:    "compose service label",
			message: containerMessage("/proj_api_1", "acme/api:1.0", map[string]string{labelComposeService: "api"}),
			want:    "api",
		},
		{
			// v1 used the task label, which is per-task and changes on every
			// restart, so SigNoz saw a new service each time.
			name: "swarm service label wins over the task label",
			message: containerMessage("/api.1.xyz", "acme/api:1.0", map[string]string{
				labelSwarmService: "stack_api",
				labelSwarmTask:    "stack_api.1.qw3rtyasdf",
			}),
			want: "stack_api",
		},
		{
			name:    "task label alone is reduced to the service",
			message: containerMessage("/api.1.xyz", "acme/api:1.0", map[string]string{labelSwarmTask: "stack_api.1.qw3rtyasdf"}),
			want:    "stack_api",
		},
		{
			name:    "container name when unlabelled",
			message: containerMessage("/lonely_container", "acme/api:1.0", nil),
			want:    "lonely_container",
		},
		{
			name:    "image basename as a last resort",
			message: containerMessage("", "ghcr.io/acme/api:1.4.2", nil),
			want:    "api",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containerServiceName(tt.message); got != tt.want {
				t.Errorf("containerServiceName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// v1 used the raw image string, so service.name changed with every tag bump.
func TestImageServiceName(t *testing.T) {
	for in, want := range map[string]string{
		"api":                       "api",
		"acme/api":                  "api",
		"acme/api:1.4.2":            "api",
		"ghcr.io/acme/api:1.4.2":    "api",
		"localhost:5000/myapp:v1":   "myapp",
		"acme/api@sha256:abc123def": "api",
		"":                          "",
	} {
		if got := imageServiceName(in); got != want {
			t.Errorf("imageServiceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServiceNameOptionOverridesLabels(t *testing.T) {
	a := testAdapter(t, &Config{ServiceName: "forced"})
	got := a.serviceName(containerMessage("/proj_api_1", "acme/api", map[string]string{labelComposeService: "api"}))
	if got != "forced" {
		t.Errorf("serviceName() = %q, want the route option to win", got)
	}
}

func TestServiceNameHandlesMissingContainer(t *testing.T) {
	if got := containerServiceName(&router.Message{Data: "x"}); got != "" {
		t.Errorf("containerServiceName() = %q, want empty for a message with no container", got)
	}
}
