//go:build !demo

package app

import (
	"time"

	"ledgerpay/internal/platform/jobs"
)

// registerDemoRoutes: build release tidak punya endpoint demo sama sekali (lapis guard "build",
// docs/design/24-docker-compose.md §9). DEMO_MODE=true pun tidak berpengaruh di binary ini.
func registerDemoRoutes(demoWiring, Deps) {}

// demoMaintenanceKinds: build release tidak punya job demo (tidak ada tabel demo_sandboxes).
func demoMaintenanceKinds(Deps) ([]jobs.Kind, map[string]time.Duration) { return nil, nil }
