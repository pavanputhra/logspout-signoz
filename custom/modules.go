package main

import (
	// The SigNoz adapter: signoz:// and signoz+https:// routes.
	_ "github.com/pavanputhra/logspout-signoz/v2/signoz"

	// multiline lets stack traces arrive as one log entry, chained in front of
	// this adapter as multiline+signoz://host:8082.
	_ "github.com/gliderlabs/logspout/adapters/multiline"

	// /health for container health checks, and the /routes API logspout users
	// expect. The v1 image shipped neither.
	_ "github.com/gliderlabs/logspout/healthcheck"
	_ "github.com/gliderlabs/logspout/routesapi"
)
