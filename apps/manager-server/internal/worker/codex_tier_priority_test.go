package worker

import (
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
)

func TestPlanCodexTierPrioritiesPrefersPlusThenPro(t *testing.T) {
	files := []cpaauthfiles.File{
		{Name: "codex-a@x.com-pro.json", Provider: "codex", AuthIndex: "1", Raw: map[string]any{}},
		{Name: "codex-b@x.com-promax.json", Provider: "codex", AuthIndex: "2", Raw: map[string]any{"plan_type": "promax"}},
		{Name: "codex-c@x.com-plus.json", Provider: "codex", AuthIndex: "3", Raw: map[string]any{"plan_type": "plus"}},
		{Name: "codex-d@x.com-plus.json", Provider: "codex", AuthIndex: "4", Disabled: true, Raw: map[string]any{"plan_type": "plus"}},
		{Name: "claude-e@x.com.json", Provider: "claude", AuthIndex: "5", Raw: map[string]any{}},
	}
	plan := planCodexTierPriorities(files, parseCodexTierOrder(""))
	if len(plan) != 3 {
		t.Fatalf("expected 3 enabled codex credentials, got %d", len(plan))
	}
	want := []struct {
		name, tier string
		priority   int
	}{
		{"codex-c@x.com-plus.json", "plus", 30},
		{"codex-a@x.com-pro.json", "pro", 20},
		{"codex-b@x.com-promax.json", "promax", 10},
	}
	for i, item := range plan {
		if item.file.Name != want[i].name || item.tier != want[i].tier || item.priority != want[i].priority {
			t.Fatalf("position %d: got %s/%s/%d, want %s/%s/%d", i, item.file.Name, item.tier, item.priority, want[i].name, want[i].tier, want[i].priority)
		}
	}
}

func TestPlanCodexTierPrioritiesHonorsCustomOrderAndUnknownTier(t *testing.T) {
	files := []cpaauthfiles.File{
		{Name: "codex-mystery.json", Provider: "codex", Raw: map[string]any{}},
		{Name: "codex-a-pro.json", Provider: "codex", Raw: map[string]any{}},
		{Name: "codex-b-plus.json", Provider: "codex", Raw: map[string]any{}},
	}
	plan := planCodexTierPriorities(files, parseCodexTierOrder("pro, plus"))
	got := []string{plan[0].tier, plan[1].tier, plan[2].tier}
	if got[0] != "pro" || got[1] != "plus" || got[2] != "unknown" {
		t.Fatalf("unexpected order %v", got)
	}
}

func TestCurrentPriorityReadsNumbersAndStrings(t *testing.T) {
	if p, ok := currentPriority(cpaauthfiles.File{Raw: map[string]any{"priority": float64(20)}}); !ok || p != 20 {
		t.Fatalf("float priority: got %d %v", p, ok)
	}
	if p, ok := currentPriority(cpaauthfiles.File{Raw: map[string]any{"priority": "30"}}); !ok || p != 30 {
		t.Fatalf("string priority: got %d %v", p, ok)
	}
	if _, ok := currentPriority(cpaauthfiles.File{Raw: map[string]any{}}); ok {
		t.Fatal("missing priority should not be ok")
	}
}
