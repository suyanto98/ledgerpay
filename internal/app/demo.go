//go:build !demo

package app

import "github.com/go-chi/chi/v5"

// registerDemoRoutes: build release tidak punya endpoint demo sama sekali (lapis guard "build",
// docs/design/24-docker-compose.md §9). DEMO_MODE=true pun tidak berpengaruh di binary ini.
func registerDemoRoutes(chi.Router, Deps) {}
