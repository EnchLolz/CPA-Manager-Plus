package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// RoutingAccount is one enabled credential in a provider's routing order.
// Higher priority is preferred by CLIProxyAPI, so index 0 receives traffic first.
type RoutingAccount struct {
	Name        string   `json:"name"`
	AuthIndex   string   `json:"auth_index"`
	Priority    int      `json:"priority"`
	Tier        string   `json:"tier,omitempty"`
	CycleEndMS  *int64   `json:"cycle_end_ms,omitempty"`
	UsedPercent *float64 `json:"used_percent,omitempty"`
}

// RoutingStatus is the last observed state of one provider's priority worker,
// so the panel can show what routing is doing without reading logs.
type RoutingStatus struct {
	Enabled         bool             `json:"enabled"`
	Strategy        string           `json:"strategy"`
	LastPollAtMS    int64            `json:"last_poll_at_ms"`
	LastAppliedAtMS int64            `json:"last_applied_at_ms"`
	LastError       string           `json:"last_error,omitempty"`
	Order           []RoutingAccount `json:"order"`
}

var routingState struct {
	mu     sync.RWMutex
	status map[string]RoutingStatus
}

// CurrentRoutingStatus returns a copy of every provider's routing status.
func CurrentRoutingStatus() map[string]RoutingStatus {
	routingState.mu.RLock()
	defer routingState.mu.RUnlock()
	out := make(map[string]RoutingStatus, len(routingState.status))
	for provider, status := range routingState.status {
		status.Order = append([]RoutingAccount(nil), status.Order...)
		out[provider] = status
	}
	return out
}

func updateRoutingStatus(provider string, mutate func(*RoutingStatus)) {
	routingState.mu.Lock()
	defer routingState.mu.Unlock()
	if routingState.status == nil {
		routingState.status = map[string]RoutingStatus{}
	}
	status := routingState.status[provider]
	mutate(&status)
	routingState.status[provider] = status
}

// currentPriority reads the priority CLIProxyAPI currently holds for a file.
func currentPriority(file cpaauthfiles.File) (int, bool) {
	switch v := file.Raw["priority"].(type) {
	case float64:
		return int(v), true
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

// patchAuthFilePriority sets only the priority field of one credential through
// the CPA management API. It never touches enabled state or tokens.
func patchAuthFilePriority(ctx context.Context, client *http.Client, setup store.Setup, file cpaauthfiles.File, priority int) error {
	selector := file.ID
	if selector == "" {
		selector = file.Name
	}
	body, _ := json.Marshal(map[string]any{"name": selector, "priority": priority})
	req, err := http.NewRequestWithContext(ctx, "PATCH", cpa.NormalizeBaseURL(setup.CPAUpstreamURL)+"/v0/management/auth-files/fields", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("priority update: invalid endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("priority update unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, 65536))
	res.Body.Close()
	if readErr != nil || res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("priority update HTTP %d", res.StatusCode)
	}
	var status struct {
		Status string `json:"status"`
		OK     *bool  `json:"ok"`
	}
	if json.Unmarshal(raw, &status) != nil || (status.OK != nil && !*status.OK) || (status.Status != "" && !strings.EqualFold(status.Status, "ok")) {
		return fmt.Errorf("priority update rejected")
	}
	return nil
}
