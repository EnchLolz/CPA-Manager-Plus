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
	// weekly holds each credential's latest successful account-wide weekly
	// window. It outlives failed polls so one unreadable account cannot stop
	// routing; the planner decides how old an observation may be.
	weekly    map[string]quotasvc.WindowInput
	retries   map[string]claudeQuotaRetry
	retryPath string
	now       func() time.Time
	pin       *RoutingPin
	pinPath   string
	pins      chan routingPinRequest
	// lastWarning keeps a persistent ranking warning to one log line.
	lastWarning string
}

func NewClaudeQuotaWorker(config claudeQuotaConfig, writer claudeQuotaWriter) *ClaudeQuotaWorker {
	w := &ClaudeQuotaWorker{config: config, writer: writer, client: &http.Client{Timeout: 30 * time.Second}, resetPriority: os.Getenv("CLAUDE_RESET_PRIORITY") == "true", weekly: map[string]quotasvc.WindowInput{}, now: time.Now}
	w.loadRetries(os.Getenv("USAGE_DATA_DIR"))
	w.pinPath = routingPinPath(os.Getenv("USAGE_DATA_DIR"), "claude")
	w.pin = loadRoutingPin(w.pinPath)
	return w
}

func (w *ClaudeQuotaWorker) Start(ctx context.Context) {
	if w.resetPriority {
		w.pins = registerRoutingPinner("claude")
	}
	go func() {
		timer := time.NewTicker(5 * time.Minute)
		defer timer.Stop()
		for {
			if err := w.poll(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[claude-quota] %v", err)
			}
			// Pins apply from cached quotas without querying providers again.
			if !serveRoutingPins(ctx, timer.C, w.pins, w.setPin) {
				return
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
	w.pruneWeekly(files)
	defer w.saveRetries()
	for _, file := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !strings.EqualFold(file.Provider, "claude") || file.AuthIndex == "" {
			continue
		}
		if retry := w.retries[file.AuthIndex]; retry.Until.After(w.now()) {
			continue
		}
		// Disabled accounts may still consume quota outside the proxy and are useful
		// comparison baselines, so include them without enabling them.
		if err := w.pollAccount(ctx, setup, file); err != nil && ctx.Err() == nil {
			w.recordQuotaFailure(file.AuthIndex, err)
			// Do not log provider bodies, tokens, or account identifiers.
			log.Printf("[claude-quota] account observation failed: %v", err)
		} else if err == nil {
			delete(w.retries, file.AuthIndex)
		}
	}
	updateRoutingStatus("claude", func(status *RoutingStatus) {
		status.Enabled = w.resetPriority
		status.Strategy = "earliest weekly reset first"
		status.LastPollAtMS = time.Now().UnixMilli()
	})
	if w.resetPriority {
		return w.applyAndReport(ctx, setup, files)
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
	if res.StatusCode == http.StatusTooManyRequests {
		return &claudeQuotaDeferredError{Status: res.StatusCode, Until: claudeQuotaRetryAfter(res.Header.Get("Retry-After"), w.now())}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &claudeQuotaStatusError{Source: "CPA", Status: res.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return fmt.Errorf("invalid quota response size")
	}
	var envelope struct {
		Status  int                        `json:"status_code"`
		Body    json.RawMessage            `json:"body"`
		Header  map[string]json.RawMessage `json:"header"`
		Headers map[string]json.RawMessage `json:"headers"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return fmt.Errorf("invalid quota response")
	}
	switch {
	case envelope.Status == http.StatusTooManyRequests:
		value := claudeQuotaRetryHeader(envelope.Header)
		if value == "" {
			value = claudeQuotaRetryHeader(envelope.Headers)
		}
		return &claudeQuotaDeferredError{Status: envelope.Status, Until: claudeQuotaRetryAfter(value, w.now())}
	case envelope.Status == http.StatusUnauthorized || envelope.Status == http.StatusForbidden:
		// The provider rejected the credential. Retrying every poll cannot help.
		return &claudeQuotaDeferredError{Status: envelope.Status, Until: w.now()}
	case envelope.Status < 200 || envelope.Status >= 300:
		return &claudeQuotaStatusError{Source: "provider", Status: envelope.Status}
	}
	var bodyText string
	if json.Unmarshal(envelope.Body, &bodyText) == nil {
		envelope.Body = json.RawMessage(bodyText)
	}
	observed := w.now().UnixMilli()
	windows, complete, err := parseClaudeQuota(envelope.Body, observed)
	if err != nil {
		return err
	}
	// Unknown or absent quotas are not zero usage or unlimited capacity. Do not
	// overwrite the last observation or remove windows on an unsupported payload.
	if len(windows) == 0 {
		return nil
	}
	// A complete inventory lets the store retire windows the provider stopped
	// reporting, such as the five-hour limit Anthropic dropped for Max plans.
	mode := "partial"
	if complete {
		mode = "complete"
	}
	_, err = w.writer.Write(ctx, quotasvc.WriteRequest{Entries: []quotasvc.WriteEntry{{
		Provider:    "claude",
		Account:     quotasvc.AccountTarget{AccountSnapshot: file.AccountSnapshot, AuthFileSnapshot: file.Name, AuthProviderSnapshot: "claude", AuthIndex: file.AuthIndex, Source: file.Name},
		Observation: &quotasvc.ObservationInput{Source: "api_query", ObservedAtMS: observed, InventoryScopeKey: "claude-background", InventoryMode: mode},
		Windows:     windows,
	}}})
	if err != nil {
		return fmt.Errorf("could not save quota observation")
	}
	weekly, ok := findWindow(windows, "seven-day")
	switch {
	case ok:
		w.weekly[file.AuthIndex] = weekly
	case complete:
		delete(w.weekly, file.AuthIndex)
	}
	return nil
}

// pruneWeekly forgets credentials that CPA no longer lists and weekly cycles
// that have ended, whose usage the provider has already reset.
func (w *ClaudeQuotaWorker) pruneWeekly(files []cpaauthfiles.File) {
	listed := map[string]bool{}
	for _, file := range files {
		listed[file.AuthIndex] = true
	}
	now := w.now().UnixMilli()
	for index, window := range w.weekly {
		if !listed[index] || (window.CycleEndMS != nil && *window.CycleEndMS <= now) {
			delete(w.weekly, index)
		}
	}
}

func findWindow(windows []quotasvc.WindowInput, id string) (quotasvc.WindowInput, bool) {
	for _, window := range windows {
		if window.ProviderWindowID == id {
			return window, true
		}
	}
	return quotasvc.WindowInput{}, false
}

// parseClaudeQuota reads the windows the background worker tracks. Complete is
// true when every tracked window in the payload was understood, so a tracked
// window missing from the result is one the provider no longer reports.
func parseClaudeQuota(raw json.RawMessage, observed int64) ([]quotasvc.WindowInput, bool, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		return nil, false, fmt.Errorf("invalid Claude usage payload")
	}
	windows := []quotasvc.WindowInput{}
	seen, rejected := map[string]bool{}, map[string]bool{}
	add := func(id string, data json.RawMessage, seconds int64) {
		// Null means the provider has no such limit for this account.
		if seen[id] || len(data) == 0 || string(data) == "null" {
			return
		}
		window, ok := parseClaudeWindow(id, data, seconds, observed)
		if !ok {
			rejected[id] = true
			return
		}
		windows = append(windows, window)
		seen[id] = true
	}
	add("seven-day", payload["seven_day"], 604800)
	add("five-hour", payload["five_hour"], 18000)
	// Model-family weekly windows are what actually gate heavy usage, so persist
	// them alongside the account-wide windows. IDs match the panel's window keys.
	add("seven-day-opus", payload["seven_day_opus"], 604800)
	add("seven-day-sonnet", payload["seven_day_sonnet"], 604800)
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
	complete := len(windows) > 0
	for id := range rejected {
		complete = complete && seen[id]
	}
	return windows, complete, nil
}

func parseClaudeWindow(id string, data json.RawMessage, seconds, observed int64) (quotasvc.WindowInput, bool) {
	var value struct {
		Utilization *float64 `json:"utilization"`
		Percent     *float64 `json:"percent"`
		ResetsAt    string   `json:"resets_at"`
		ResetAt     string   `json:"reset_at"`
	}
	if json.Unmarshal(data, &value) != nil {
		return quotasvc.WindowInput{}, false
	}
	percent := value.Utilization
	if percent == nil {
		percent = value.Percent
	}
	if percent == nil || *percent < 0 || *percent > 100 {
		return quotasvc.WindowInput{}, false
	}
	remaining := 100 - *percent
	kind := "weekly"
	if seconds == 18000 {
		kind = "session"
	}
	window := quotasvc.WindowInput{ProviderWindowID: id, WindowKind: kind, ModelScopeKind: "all", Source: "api_query", ObservedAtMS: observed, DurationSeconds: &seconds, UsedPercent: percent, RemainingPercent: &remaining}
	if value.ResetsAt == "" {
		value.ResetsAt = value.ResetAt
	}
	if value.ResetsAt == "" {
		// An idle window has no cycle until the account is used again. It still
		// exists, and the panel stores it the same way.
		window.WindowMode, window.BoundaryAccuracy = "unknown", "unknown"
		return window, true
	}
	reset, err := time.Parse(time.RFC3339Nano, value.ResetsAt)
	if err != nil || reset.UnixMilli() <= observed {
		return quotasvc.WindowInput{}, false
	}
	end := reset.UnixMilli()
	start := end - seconds*1000
	window.WindowMode, window.BoundaryAccuracy, window.CycleStartMS, window.CycleEndMS = "fixed", "derived", &start, &end
	return window, true
}
