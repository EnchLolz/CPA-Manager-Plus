package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	quotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type claudeQuotaConfig interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}
type claudeQuotaWriter interface {
	Write(context.Context, quotasvc.WriteRequest) (quotasvc.WriteResponse, error)
}

// ClaudeQuotaWorker records quota even when no browser is open. It never sends
// inference requests or changes enabled state. Opt-in reset priority changes only priority metadata.
type ClaudeQuotaWorker struct {
	config        claudeQuotaConfig
	writer        claudeQuotaWriter
	client        *http.Client
	resetPriority bool
	weekly        map[string]quotasvc.WindowInput
}

func NewClaudeQuotaWorker(config claudeQuotaConfig, writer claudeQuotaWriter) *ClaudeQuotaWorker {
	return &ClaudeQuotaWorker{config: config, writer: writer, client: &http.Client{Timeout: 30 * time.Second}, resetPriority: os.Getenv("CLAUDE_RESET_PRIORITY") == "true"}
}

func (w *ClaudeQuotaWorker) Start(ctx context.Context) {
	go func() {
		timer := time.NewTicker(5 * time.Minute)
		defer timer.Stop()
		for {
			if err := w.poll(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[claude-quota] %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}

func (w *ClaudeQuotaWorker) poll(ctx context.Context) error {
	setup, ok, err := w.config.ResolveSetup(ctx)
	if err != nil || !ok {
		return err
	}
	files, err := cpaauthfiles.New(w.client).Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return fmt.Errorf("account inventory unavailable")
	}
	w.weekly = make(map[string]quotasvc.WindowInput)
	for _, file := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !strings.EqualFold(file.Provider, "claude") || file.AuthIndex == "" {
			continue
		}
		// Disabled accounts may still consume quota outside the proxy and are useful
		// comparison baselines, so include them without enabling them.
		if err := w.pollAccount(ctx, setup, file); err != nil && ctx.Err() == nil {
			// Do not log provider bodies, tokens, or account identifiers.
			log.Printf("[claude-quota] account observation failed: %v", err)
		}
	}
	if w.resetPriority {
		return w.applyResetPriority(ctx, setup, files)
	}
	return nil
}

func (w *ClaudeQuotaWorker) pollAccount(ctx context.Context, setup store.Setup, file cpaauthfiles.File) error {
	body, _ := json.Marshal(map[string]any{
		"authIndex": file.AuthIndex, "method": "GET", "url": "https://api.anthropic.com/api/oauth/usage",
		"header": map[string]string{"Authorization": "Bearer $TOKEN$", "Content-Type": "application/json", "anthropic-beta": "oauth-2025-04-20"},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", cpa.NormalizeBaseURL(setup.CPAUpstreamURL)+"/v0/management/api-call", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid CPA endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("quota request unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("CPA HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return fmt.Errorf("invalid quota response size")
	}
	var envelope struct {
		Status int             `json:"status_code"`
		Body   json.RawMessage `json:"body"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return fmt.Errorf("invalid quota response")
	}
	if envelope.Status < 200 || envelope.Status >= 300 {
		return fmt.Errorf("provider HTTP %d", envelope.Status)
	}
	var bodyText string
	if json.Unmarshal(envelope.Body, &bodyText) == nil {
		envelope.Body = json.RawMessage(bodyText)
	}
	observed := time.Now().UnixMilli()
	windows, err := parseClaudeQuota(envelope.Body, observed)
	if err != nil {
		return err
	}
	// Unknown or absent quotas are not zero usage or unlimited capacity. Do not
	// overwrite the last observation or remove windows on an unsupported payload.
	if len(windows) == 0 {
		return nil
	}
	_, err = w.writer.Write(ctx, quotasvc.WriteRequest{Entries: []quotasvc.WriteEntry{{
		Provider:    "claude",
		Account:     quotasvc.AccountTarget{AccountSnapshot: file.AccountSnapshot, AuthFileSnapshot: file.Name, AuthProviderSnapshot: "claude", AuthIndex: file.AuthIndex, Source: file.Name},
		Observation: &quotasvc.ObservationInput{Source: "api_query", ObservedAtMS: observed, InventoryScopeKey: "claude-background", InventoryMode: "partial"},
		Windows:     windows,
	}}})
	if err != nil {
		return fmt.Errorf("could not save quota observation")
	}
	if w.weekly != nil {
		for _, window := range windows {
			if window.ProviderWindowID == "seven-day" {
				w.weekly[file.AuthIndex] = window
			}
		}
	}
	return nil
}

func parseClaudeQuota(raw json.RawMessage, observed int64) ([]quotasvc.WindowInput, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		return nil, fmt.Errorf("invalid Claude usage payload")
	}
	windows := []quotasvc.WindowInput{}
	seen := map[string]bool{}
	add := func(id string, data json.RawMessage, seconds int64) {
		if seen[id] {
			return
		}
		var value struct {
			Utilization *float64 `json:"utilization"`
			Percent     *float64 `json:"percent"`
			ResetsAt    string   `json:"resets_at"`
			ResetAt     string   `json:"reset_at"`
		}
		if json.Unmarshal(data, &value) != nil {
			return
		}
		percent := value.Utilization
		if percent == nil {
			percent = value.Percent
		}
		if percent == nil || *percent < 0 || *percent > 100 {
			return
		}
		if value.ResetsAt == "" {
			value.ResetsAt = value.ResetAt
		}
		reset, err := time.Parse(time.RFC3339Nano, value.ResetsAt)
		if err != nil || reset.UnixMilli() <= observed {
			return
		}
		end := reset.UnixMilli()
		start := end - seconds*1000
		remaining := 100 - *percent
		kind := "weekly"
		if seconds == 18000 {
			kind = "session"
		}
		windows = append(windows, quotasvc.WindowInput{ProviderWindowID: id, WindowKind: kind, WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: observed, BoundaryAccuracy: "derived", CycleStartMS: &start, CycleEndMS: &end, DurationSeconds: &seconds, UsedPercent: percent, RemainingPercent: &remaining})
		seen[id] = true
	}
	add("seven-day", payload["seven_day"], 604800)
	add("five-hour", payload["five_hour"], 18000)
	// Newer Claude payloads expose unscoped base limits in a limits array. Scoped
	// limits must not be treated as account-wide allowances.
	var limits []map[string]json.RawMessage
	_ = json.Unmarshal(payload["limits"], &limits)
	for _, limit := range limits {
		var kind, group string
		_ = json.Unmarshal(limit["kind"], &kind)
		_ = json.Unmarshal(limit["group"], &group)
		if scope, ok := limit["scope"]; ok && string(scope) != "null" {
			continue
		}
		if string(limit["is_active"]) == "false" {
			continue
		}
		data, _ := json.Marshal(limit)
		if (kind == "weekly_all" || kind == "weekly") && (group == "" || group == "weekly" || group == "weekly_all") {
			add("seven-day", data, 604800)
		}
		if kind == "session" && (group == "" || group == "session") {
			add("five-hour", data, 18000)
		}
	}
	return windows, nil
}
