package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	quotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type resetPriority struct {
	file     cpaauthfiles.File
	priority int
}

// Require a fresh, future weekly reset for every enabled Claude credential before
// reordering anything. Unknown quotas never imply unlimited or exhausted usage.
func planResetPriorities(files []cpaauthfiles.File, windows map[string]quotasvc.WindowInput, now int64) ([]resetPriority, error) {
	enabled := []cpaauthfiles.File{}
	for _, file := range files {
		if file.Provider != "claude" || file.Disabled {
			continue
		}
		q, ok := windows[file.AuthIndex]
		if !ok || file.AuthIndex == "" || q.CycleEndMS == nil || *q.CycleEndMS <= now || q.ObservedAtMS > now || now-q.ObservedAtMS > 15*60*1000 || q.UsedPercent == nil {
			return nil, fmt.Errorf("reset priority unchanged: incomplete or stale weekly quotas")
		}
		enabled = append(enabled, file)
	}
	sort.Slice(enabled, func(i, j int) bool {
		a, b := windows[enabled[i].AuthIndex], windows[enabled[j].AuthIndex]
		if (*a.UsedPercent >= 100) != (*b.UsedPercent >= 100) {
			return *a.UsedPercent < 100
		}
		// API reset timestamps can jitter by milliseconds. Equal seconds are a tie.
		if *a.CycleEndMS/1000 != *b.CycleEndMS/1000 {
			return *a.CycleEndMS < *b.CycleEndMS
		}
		return enabled[i].Name < enabled[j].Name
	})
	result := []resetPriority{}
	for i, file := range enabled {
		result = append(result, resetPriority{file: file, priority: (len(enabled) - i) * 10})
	}
	return result, nil
}

func (w *ClaudeQuotaWorker) applyResetPriority(ctx context.Context, setup store.Setup, files []cpaauthfiles.File) error {
	plan, err := planResetPriorities(files, w.weekly, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	order := make([]ClaudeResetPriorityAccount, 0, len(plan))
	for _, item := range plan {
		q := w.weekly[item.file.AuthIndex]
		order = append(order, ClaudeResetPriorityAccount{
			Name: item.file.Name, AuthIndex: item.file.AuthIndex, Priority: item.priority,
			CycleEndMS: *q.CycleEndMS, UsedPercent: *q.UsedPercent,
		})
	}
	updateClaudeResetPriorityStatus(func(status *ClaudeResetPriorityStatus) {
		status.Order = order
		status.LastAppliedAtMS = time.Now().UnixMilli()
	})
	for _, item := range plan {
		current, _ := json.Marshal(item.file.Raw["priority"])
		if string(current) == fmt.Sprint(item.priority) || string(current) == fmt.Sprintf("%q", fmt.Sprint(item.priority)) {
			continue
		}
		selector := item.file.ID
		if selector == "" {
			selector = item.file.Name
		}
		body, _ := json.Marshal(map[string]any{"name": selector, "priority": item.priority})
		req, err := http.NewRequestWithContext(ctx, "PATCH", cpa.NormalizeBaseURL(setup.CPAUpstreamURL)+"/v0/management/auth-files/fields", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("reset priority: invalid endpoint")
		}
		req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
		req.Header.Set("Content-Type", "application/json")
		res, err := w.client.Do(req)
		if err != nil {
			return fmt.Errorf("reset priority update unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 65536))
		res.Body.Close()
		if readErr != nil || res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("reset priority update HTTP %d", res.StatusCode)
		}
		var status struct {
			Status string `json:"status"`
			OK     *bool  `json:"ok"`
		}
		if json.Unmarshal(raw, &status) != nil || (status.OK != nil && !*status.OK) || (status.Status != "" && !strings.EqualFold(status.Status, "ok")) {
			return fmt.Errorf("reset priority update rejected")
		}
		log.Printf("[claude-reset-priority] updated %s to %d", item.file.Name, item.priority)
	}
	return nil
}
