package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Check is one named readiness dependency.
type Check struct {
	Name string
	Ping func(ctx context.Context) error
}

type HealthHandler struct {
	checks []Check
}

// NewHealthHandler reports ready only when every check passes.
func NewHealthHandler(checks ...Check) *HealthHandler {
	return &HealthHandler{checks: checks}
}

func (h *HealthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /ready", h.Ready)
}

// Health is liveness: the process is up. It checks no dependencies, so a
// database blip does not get the container restarted.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready is readiness: every dependency answers. Failures say which
// dependency is down but never include error details.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	status := map[string]string{"status": "ok"}
	code := http.StatusOK
	for _, c := range h.checks {
		if err := c.Ping(ctx); err != nil {
			status[c.Name] = "unreachable"
			status["status"] = "degraded"
			code = http.StatusServiceUnavailable
			continue
		}
		status[c.Name] = "ok"
	}
	writeJSON(w, code, status)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
