package sharedkey

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	sharedkeys "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/sharedkey"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const Path = "/v0/management/shared-keys"

type Handler struct {
	App     *app.Context
	Service *sharedkeys.Service
}

func (h *Handler) Gateway(w http.ResponseWriter, r *http.Request) {
	middleware.WriteCORS(h.App.Config, w, r)
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key, Anthropic-Version, Anthropic-Beta")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.Service.Gateway(w, r)
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == Path && r.Method == http.MethodGet:
		items, err := h.App.Store.ListSharedKeys(r.Context())
		if err != nil {
			response.Error(w, 500, err)
			return
		}
		response.JSON(w, 200, map[string]any{"items": items, "gateway_path": sharedkeys.Prefix, "gateway_base_url": h.App.Config.SharedGatewayPublicURL})
	case path == Path && r.Method == http.MethodPost:
		var req sharedkeys.CreateRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			response.Error(w, 400, err)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "Expected one JSON object", 400)
			return
		}
		key, secret, err := h.Service.Create(r.Context(), req)
		if err != nil {
			response.Error(w, 400, err)
			return
		}
		response.JSON(w, 201, map[string]any{"item": key, "key": secret, "gateway_path": sharedkeys.Prefix, "gateway_base_url": h.App.Config.SharedGatewayPublicURL})
	case strings.HasPrefix(path, Path+"/") && r.Method == http.MethodDelete:
		err := h.App.Store.RevokeSharedKey(r.Context(), strings.TrimPrefix(path, Path+"/"))
		if store.IsSharedKeyNotFound(err) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			response.Error(w, 500, err)
			return
		}
		response.JSON(w, 200, map[string]bool{"ok": true})
	default:
		response.MethodNotAllowed(w)
	}
}
