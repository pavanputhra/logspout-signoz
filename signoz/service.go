package signoz

import (
	"strings"

	"github.com/gliderlabs/logspout/router"
)

// Docker labels that identify the logical service a container belongs to.
const (
	labelComposeService = "com.docker.compose.service"
	labelSwarmService   = "com.docker.swarm.service.name"
	labelSwarmTask      = "com.docker.swarm.task.name"
)

// containerServiceName derives the OTel service.name for a container.
//
// Order: swarm service, compose service, container name, then the image.
//
// v1 used com.docker.swarm.task.name, which is per-task ("web.1.qw3rty...") and
// changes on every restart — that produced a new service in SigNoz for every
// task and made service-level grouping useless. The service label is the stable
// one.
func containerServiceName(m *router.Message) string {
	if m == nil || m.Container == nil {
		return ""
	}

	if m.Container.Config != nil {
		labels := m.Container.Config.Labels
		if name := labels[labelSwarmService]; name != "" {
			return name
		}
		if name := labels[labelComposeService]; name != "" {
			return name
		}
		// Fall back to deriving the service from the task name
		// ("stack_web.1.qw3rty" -> "stack_web") for older swarm versions that
		// set only the task label.
		if task := labels[labelSwarmTask]; task != "" {
			if name := serviceFromTaskName(task); name != "" {
				return name
			}
		}
	}

	if name := strings.TrimPrefix(m.Container.Name, "/"); name != "" {
		return name
	}
	if m.Container.Config != nil {
		return imageServiceName(m.Container.Config.Image)
	}
	return ""
}

// serviceFromTaskName strips the replica and task ID from a swarm task name.
func serviceFromTaskName(task string) string {
	if i := strings.Index(task, "."); i > 0 {
		return task[:i]
	}
	return task
}

// imageServiceName reduces an image reference to its repository basename, so
// "ghcr.io/acme/api:1.4.2" becomes "api". v1 used the raw image string, which
// meant service.name changed with every tag bump.
func imageServiceName(image string) string {
	if image == "" {
		return ""
	}
	if i := strings.Index(image, "@"); i > 0 { // digest
		image = image[:i]
	}
	lastSlash := strings.LastIndex(image, "/")
	if i := strings.LastIndex(image, ":"); i > lastSlash {
		image = image[:i]
	}
	if lastSlash := strings.LastIndex(image, "/"); lastSlash >= 0 {
		image = image[lastSlash+1:]
	}
	return image
}
