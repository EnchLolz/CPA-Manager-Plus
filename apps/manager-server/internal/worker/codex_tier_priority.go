package worker

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// Default tier preference: spend the Plus subscription first, keep Pro and
// Pro Max as reserve. Override with CODEX_TIER_ORDER="plus,pro,promax".
const defaultCodexTierOrder = "plus,pro,promax"

var codexNameTier = regexp.MustCompile(`-(plus|pro|promax|team|business|edu|free)\.json$`)

type codexTierConfig interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

// CodexTierPriorityWorker orders enabled Codex credentials by subscription tier
// and writes CPA priorities so the preferred tier is used first. Opt-in via
// CODEX_TIER_PRIORITY=true. It only ever changes priority metadata.
type CodexTierPriorityWorker struct {
	config  codexTierConfig
	client  *http.Client
	enabled bool
	order   []string
	pin     *RoutingPin
	pinPath string
	pins    chan routingPinRequest
}

func NewCodexTierPriorityWorker(config codexTierConfig) *CodexTierPriorityWorker {
	pinPath := routingPinPath(os.Getenv("USAGE_DATA_DIR"), "codex")
	return &CodexTierPriorityWorker{
		config:  config,
		client:  &http.Client{Timeout: 30 * time.Second},
		enabled: os.Getenv("CODEX_TIER_PRIORITY") == "true",
		order:   parseCodexTierOrder(os.Getenv("CODEX_TIER_ORDER")),
		pin:     loadRoutingPin(pinPath),
		pinPath: pinPath,
	}
}

func parseCodexTierOrder(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		raw = defaultCodexTierOrder
	}
	order := []string{}
	for _, part := range strings.Split(raw, ",") {
		if tier := strings.ToLower(strings.TrimSpace(part)); tier != "" {
			order = append(order, tier)
		}
	}
	return order
}

func (w *CodexTierPriorityWorker) Start(ctx context.Context) {
	if w.enabled {
		w.pins = registerRoutingPinner("codex")
	}
	go func() {
		timer := time.NewTicker(5 * time.Minute)
		defer timer.Stop()
		for {
			if err := w.poll(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[codex-tier-priority] %v", err)
			}
			if !serveRoutingPins(ctx, timer.C, w.pins, w.setPin) {
				return
			}
		}
	}()
}

func (w *CodexTierPriorityWorker) poll(ctx context.Context) error {
	setup, ok, err := w.config.ResolveSetup(ctx)
	if err != nil || !ok {
		return err
	}
	files, err := cpaauthfiles.New(w.client).Fetch(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return fmt.Errorf("account inventory unavailable")
	}
	return w.apply(ctx, setup, files)
}

// setPin puts one enabled Codex credential ahead of the tier order until it is
// cleared. Tier order is static, so unlike Claude there is no natural end.
func (w *CodexTierPriorityWorker) setPin(ctx context.Context, authIndex string) error {
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
			log.Printf("[codex-tier-priority] pin on %s cleared", w.pin.Name)
		}
		w.pin = nil
	} else {
		file, ok := enabledCredential(files, "codex", authIndex)
		if !ok {
			return ErrRoutingPinTarget
		}
		w.pin = &RoutingPin{AuthIndex: file.AuthIndex, Name: file.Name, PinnedAtMS: time.Now().UnixMilli()}
		log.Printf("[codex-tier-priority] pinned %s first", file.Name)
	}
	saveRoutingPin(w.pinPath, w.pin)
	// Ordering problems are reported in the routing status like a regular poll.
	_ = w.apply(ctx, setup, files)
	return nil
}

func (w *CodexTierPriorityWorker) apply(ctx context.Context, setup store.Setup, files []cpaauthfiles.File) error {
	if w.pin != nil {
		if _, ok := enabledCredential(files, "codex", w.pin.AuthIndex); !ok {
			log.Printf("[codex-tier-priority] pin on %s cleared: credential is no longer enabled", w.pin.Name)
			w.pin = nil
			saveRoutingPin(w.pinPath, nil)
		}
	}
	pinned := pinnedAuthIndex(w.pin)
	plan := planCodexTierPriorities(files, w.order, pinned)
	order := make([]RoutingAccount, 0, len(plan))
	for _, item := range plan {
		order = append(order, RoutingAccount{Name: item.file.Name, AuthIndex: item.file.AuthIndex, Priority: item.priority, Tier: item.tier, Pinned: item.file.AuthIndex == pinned && pinned != ""})
	}
	now := time.Now().UnixMilli()
	updateRoutingStatus("codex", func(status *RoutingStatus) {
		status.Enabled = w.enabled
		status.Strategy = "tier " + strings.Join(w.order, " > ")
		status.LastPollAtMS = now
		status.Order = order
		status.LastError = ""
		status.Pin = copyRoutingPin(w.pin)
	})
	if !w.enabled {
		return nil
	}
	for _, item := range plan {
		if current, ok := currentPriority(item.file); ok && current == item.priority {
			continue
		}
		if err := patchAuthFilePriority(ctx, w.client, setup, item.file, item.priority); err != nil {
			updateRoutingStatus("codex", func(status *RoutingStatus) { status.LastError = err.Error() })
			return fmt.Errorf("tier priority: %w", err)
		}
		log.Printf("[codex-tier-priority] updated %s (%s) to %d", item.file.Name, item.tier, item.priority)
	}
	updateRoutingStatus("codex", func(status *RoutingStatus) { status.LastAppliedAtMS = now })
	return nil
}

type codexTierPriority struct {
	file     cpaauthfiles.File
	tier     string
	priority int
}

// codexTier reads the subscription tier from the credential's plan_type field,
// falling back to the file name suffix CPA's OAuth login writes.
func codexTier(file cpaauthfiles.File) string {
	if v, ok := file.Raw["plan_type"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.ToLower(strings.TrimSpace(v))
	}
	if m := codexNameTier.FindStringSubmatch(strings.ToLower(file.Name)); m != nil {
		return m[1]
	}
	return "unknown"
}

// planCodexTierPriorities ranks enabled Codex credentials by tier order; tiers
// not in the order sort last, and ties keep a stable name order so repeated
// polls do not flip priorities. A pinned credential goes first.
func planCodexTierPriorities(files []cpaauthfiles.File, order []string, pinned string) []codexTierPriority {
	rank := func(tier string) int {
		for i, t := range order {
			if t == tier {
				return i
			}
		}
		return len(order)
	}
	plan := []codexTierPriority{}
	for _, file := range files {
		if !strings.EqualFold(file.Provider, "codex") || file.Disabled {
			continue
		}
		plan = append(plan, codexTierPriority{file: file, tier: codexTier(file)})
	}
	sort.SliceStable(plan, func(i, j int) bool {
		ri, rj := rank(plan[i].tier), rank(plan[j].tier)
		if ri != rj {
			return ri < rj
		}
		return plan[i].file.Name < plan[j].file.Name
	})
	plan = pinFirst(plan, func(item codexTierPriority) bool { return pinned != "" && item.file.AuthIndex == pinned })
	for i := range plan {
		plan[i].priority = (len(plan) - i) * 10
	}
	return plan
}
