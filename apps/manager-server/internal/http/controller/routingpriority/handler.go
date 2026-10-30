// Package routingpriority exposes the per-provider routing status written by
// the priority workers, and lets the panel pin one credential first.
// Personal fork addition.
package routingpriority

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
)

const (
	Path    = "/v0/management/routing-priority"
	PinPath = Path + "/pin"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"providers": worker.CurrentRoutingStatus()})
}

// HandlePin sets (PUT {provider, auth_index}) or clears (DELETE ?provider=)
// the pinned credential and answers with the routing status after it applied.
func (h *Handler) HandlePin(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	var provider, authIndex string
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Provider  string `json:"provider"`
			AuthIndex string `json:"auth_index"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || strings.TrimSpace(req.AuthIndex) == "" {
			response.Error(w, http.StatusBadRequest, errors.New("provider and auth_index are required"))
			return
		}
		provider, authIndex = req.Provider, strings.TrimSpace(req.AuthIndex)
	case http.MethodDelete:
		provider = r.URL.Query().Get("provider")
	default:
		response.MethodNotAllowed(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := worker.PinRouting(ctx, strings.TrimSpace(provider), authIndex); err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, worker.ErrRoutingUnavailable):
			status = http.StatusConflict
		case errors.Is(err, worker.ErrRoutingPinTarget):
			status = http.StatusNotFound
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		}
		response.Error(w, status, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"providers": worker.CurrentRoutingStatus()})
}
