package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	quotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// routingQuotaMaxAge bounds how long a weekly observation keeps ranking an
// account whose quota can no longer be read. A weekly reset is fixed for its
// cycle and usage only grows within it, so an observation stays a sound sort
// key through the hour-long quota backoff, but not for a credential that has
// stopped working.
const routingQuotaMaxAge = 2 * time.Hour

var errNoWeeklyQuotas = errors.New("reset priority unchanged: no current weekly quotas")

type quotaTier int

const (
	tierAvailable quotaTier = iota
	tierUnknown
	tierExhausted
)

type resetPriority struct {
	file     cpaauthfiles.File
	priority int
	tier     quotaTier
	// weekly is the observation the account was ranked by; zero when unknown.
	weekly quotasvc.WindowInput
}

// currentWeekly returns an account's weekly window when it is recent enough to
// rank by and its cycle has not ended.
func currentWeekly(windows map[string]quotasvc.WindowInput, index string, now int64) (quotasvc.WindowInput, bool) {
	q, ok := windows[index]
	if !ok || index == "" || q.UsedPercent == nil || q.ObservedAtMS > now || now-q.ObservedAtMS > routingQuotaMaxAge.Milliseconds() {
		return quotasvc.WindowInput{}, false
	}
	if q.CycleEndMS != nil && *q.CycleEndMS <= now {
		return quotasvc.WindowInput{}, false
	}
	return q, true
}

// resetSortKey orders by reset second; API timestamps jitter by milliseconds.
// An idle weekly window starts its cycle at the next request, so it resets
// after every running cycle.
func resetSortKey(q quotasvc.WindowInput) int64 {
	if q.CycleEndMS == nil {
		return math.MaxInt64
	}
	return *q.CycleEndMS / 1000
}

// planResetPriorities ranks enabled Claude credentials: accounts with quota
// left by earliest weekly reset, then accounts whose quota is unknown in their
// current order, then exhausted accounts. Unknown quotas never imply unlimited
// or exhausted usage, so they neither jump ahead nor drop to the bottom, and
// one unreadable account does not hold back the rest. A pinned account goes
// first unless its weekly limit is known to be exhausted. Without any current
// quota there is nothing to rank by, so only a pin changes the order.
func planResetPriorities(files []cpaauthfiles.File, windows map[string]quotasvc.WindowInput, now int64, pinned string) ([]resetPriority, error) {
	ranked := []resetPriority{}
	known := 0
	for _, file := range files {
		if !strings.EqualFold(file.Provider, "claude") || file.Disabled {
			continue
		}
		item := resetPriority{file: file, tier: tierUnknown}
		if q, ok := currentWeekly(windows, file.AuthIndex, now); ok {
			item.weekly, item.tier = q, tierAvailable
			if *q.UsedPercent >= 100 {
				item.tier = tierExhausted
			}
			known++
		}
		ranked = append(ranked, item)
	}
	if known == 0 && len(ranked) > 0 && pinned == "" {
		return nil, errNoWeeklyQuotas
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.tier == tierUnknown {
			pa, _ := currentPriority(a.file)
			pb, _ := currentPriority(b.file)
			if pa != pb {
				return pa > pb
			}
		} else if ka, kb := resetSortKey(a.weekly), resetSortKey(b.weekly); ka != kb {
			return ka < kb
		}
		return a.file.Name < b.file.Name
	})
	ranked = pinFirst(ranked, func(item resetPriority) bool {
		return pinned != "" && item.file.AuthIndex == pinned && item.tier != tierExhausted
	})
	for i := range ranked {
		ranked[i].priority = (len(ranked) - i) * 10
	}
	return ranked, nil
}

// routingWarning describes accounts ranked without a current quota. When none
// has one, the order is CPA's current order with only the pin moved.
func routingWarning(plan []resetPriority) (warning string, paused bool) {
	unknown := 0
	for _, item := range plan {
		if item.tier == tierUnknown {
			unknown++
		}
	}
	switch {
	case unknown == 0:
		return "", false
	case unknown == len(plan):
		return "no current weekly quotas: kept the current order with the pinned account first", true
	case unknown == 1:
		return "1 account has no current weekly quota and is ranked after accounts with quota left", false
	default:
		return fmt.Sprintf("%d accounts have no current weekly quota and are ranked after accounts with quota left", unknown), false
	}
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
		updateRoutingStatus("claude", func(status *RoutingStatus) { status.Warning = "" })
		return planErr
	}
	warning, paused := routingWarning(plan)
	if paused {
		// Reported as an error so the panel shows automatic ordering as paused.
		planErr, warning = errors.New(warning), ""
	}
	if warning != w.lastWarning {
		if warning != "" {
			log.Printf("[claude-reset-priority] %s", warning)
		}
		w.lastWarning = warning
	}
	order := make([]RoutingAccount, 0, len(plan))
	for _, item := range plan {
		failure := w.retries[item.file.AuthIndex]
		order = append(order, RoutingAccount{
			Name: item.file.Name, AuthIndex: item.file.AuthIndex, Priority: item.priority,
			CycleEndMS: item.weekly.CycleEndMS, UsedPercent: item.weekly.UsedPercent, Pinned: item.file.AuthIndex == pinned,
			QuotaUnknown: item.tier == tierUnknown, QuotaError: failure.Error, QuotaErrorStatus: failure.Status,
		})
	}
	updateRoutingStatus("claude", func(status *RoutingStatus) {
		status.Order = order
		status.Warning = warning
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
