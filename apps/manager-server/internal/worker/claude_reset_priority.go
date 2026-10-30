package worker

import (
	"context"
	"fmt"
	"log"
	"sort"

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
// A pinned account goes first unless its weekly limit is exhausted.
func planResetPriorities(files []cpaauthfiles.File, windows map[string]quotasvc.WindowInput, now int64, pinned string) ([]resetPriority, error) {
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
	enabled = pinFirst(enabled, func(file cpaauthfiles.File) bool {
		return pinned != "" && file.AuthIndex == pinned && *windows[file.AuthIndex].UsedPercent < 100
	})
	return rankResetPriorities(enabled), nil
}

// planPinnedOverCurrent honors a pin while quotas are incomplete: the automatic
// order is unknown, so CPA's current order is kept and only the pin moves.
func planPinnedOverCurrent(files []cpaauthfiles.File, pinned string) []resetPriority {
	enabled := []cpaauthfiles.File{}
	for _, file := range files {
		if file.Provider == "claude" && !file.Disabled && file.AuthIndex != "" {
			enabled = append(enabled, file)
		}
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		a, _ := currentPriority(enabled[i])
		b, _ := currentPriority(enabled[j])
		if a != b {
			return a > b
		}
		return enabled[i].Name < enabled[j].Name
	})
	enabled = pinFirst(enabled, func(file cpaauthfiles.File) bool { return file.AuthIndex == pinned })
	return rankResetPriorities(enabled)
}

func rankResetPriorities(ordered []cpaauthfiles.File) []resetPriority {
	result := []resetPriority{}
	for i, file := range ordered {
		result = append(result, resetPriority{file: file, priority: (len(ordered) - i) * 10})
	}
	return result
}

// refreshPin ends a pin at the pinned account's weekly reset, when its fresh
// quota makes earliest-reset ordering right again, and drops pins on accounts
// that are no longer enabled so they cannot resurface later.
func (w *ClaudeQuotaWorker) refreshPin(files []cpaauthfiles.File, now int64) {
	if w.pin == nil {
		return
	}
	before := *w.pin
	if _, ok := enabledCredential(files, "claude", w.pin.AuthIndex); !ok {
		log.Printf("[claude-reset-priority] pin on %s cleared: credential is no longer enabled", w.pin.Name)
		w.pin = nil
	} else if w.pin.UntilMS > 0 && now >= w.pin.UntilMS {
		log.Printf("[claude-reset-priority] pin on %s ended at its weekly reset", w.pin.Name)
		w.pin = nil
	} else if q, ok := w.weekly[w.pin.AuthIndex]; ok && w.pin.UntilMS == 0 && q.CycleEndMS != nil && *q.CycleEndMS > now {
		w.pin.UntilMS = *q.CycleEndMS
	}
	if w.pin == nil || *w.pin != before {
		saveRoutingPin(w.pinPath, w.pin)
	}
}

func (w *ClaudeQuotaWorker) setPin(ctx context.Context, authIndex string) error {
	setup, ok, err := w.config.ResolveSetup(ctx)
	if err != nil || !ok {
		return fmt.Errorf("CPA connection unavailable")
	}
	files, err := cpaauthfiles.New(w.client).Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return fmt.Errorf("account inventory unavailable")
	}
	if authIndex == "" {
		if w.pin != nil {
			log.Printf("[claude-reset-priority] pin on %s cleared", w.pin.Name)
		}
		w.pin = nil
	} else {
		file, ok := enabledCredential(files, "claude", authIndex)
		if !ok {
			return ErrRoutingPinTarget
		}
		w.pin = &RoutingPin{AuthIndex: file.AuthIndex, Name: file.Name, PinnedAtMS: w.now().UnixMilli()}
		log.Printf("[claude-reset-priority] pinned %s first", file.Name)
	}
	saveRoutingPin(w.pinPath, w.pin)
	// Ordering problems are reported in the routing status like a regular poll.
	_ = w.applyAndReport(ctx, setup, files)
	return nil
}

func (w *ClaudeQuotaWorker) applyAndReport(ctx context.Context, setup store.Setup, files []cpaauthfiles.File) error {
	err := w.applyResetPriority(ctx, setup, files)
	updateRoutingStatus("claude", func(status *RoutingStatus) {
		if err != nil {
			status.LastError = err.Error()
		} else {
			status.LastError = ""
		}
	})
	return err
}

func (w *ClaudeQuotaWorker) applyResetPriority(ctx context.Context, setup store.Setup, files []cpaauthfiles.File) error {
	now := w.now().UnixMilli()
	w.refreshPin(files, now)
	pinned := pinnedAuthIndex(w.pin)
	updateRoutingStatus("claude", func(status *RoutingStatus) { status.Pin = copyRoutingPin(w.pin) })
	plan, planErr := planResetPriorities(files, w.weekly, now, pinned)
	if planErr != nil {
		if pinned == "" {
			return planErr
		}
		plan = planPinnedOverCurrent(files, pinned)
		planErr = fmt.Errorf("incomplete or stale weekly quotas: kept the current order with the pinned account first")
	}
	order := make([]RoutingAccount, 0, len(plan))
	for _, item := range plan {
		q := w.weekly[item.file.AuthIndex]
		order = append(order, RoutingAccount{
			Name: item.file.Name, AuthIndex: item.file.AuthIndex, Priority: item.priority,
			CycleEndMS: q.CycleEndMS, UsedPercent: q.UsedPercent, Pinned: item.file.AuthIndex == pinned,
		})
	}
	updateRoutingStatus("claude", func(status *RoutingStatus) {
		status.Order = order
		status.LastAppliedAtMS = now
	})
	for _, item := range plan {
		if current, ok := currentPriority(item.file); ok && current == item.priority {
			continue
		}
		if err := patchAuthFilePriority(ctx, w.client, setup, item.file, item.priority); err != nil {
			return fmt.Errorf("reset priority: %w", err)
		}
		log.Printf("[claude-reset-priority] updated %s to %d", item.file.Name, item.priority)
	}
	return planErr
}
