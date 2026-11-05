package worker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	quotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
)

func planOrder(t *testing.T, files []cpaauthfiles.File, windows map[string]quotasvc.WindowInput, now int64, pinned string) []string {
	t.Helper()
	plan, err := planResetPriorities(files, windows, now, pinned)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	order := []string{}
	for i, item := range plan {
		if item.priority != (len(plan)-i)*10 {
			t.Fatalf("position %d has priority %d", i, item.priority)
		}
		order = append(order, item.file.AuthIndex)
	}
	return order
}

func assertOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

func TestResetPriorityRolloverAndExhaustion(t *testing.T) {
	const now int64 = 10000000
	a, b := now+100000, now+200000
	usedA, usedB := 20.0, 40.0
	files := []cpaauthfiles.File{{Name: "max", AuthIndex: "a", Provider: "claude"}, {Name: "team", AuthIndex: "b", Provider: "claude"}, {Name: "disabled", Provider: "claude", Disabled: true}, {Name: "codex", Provider: "codex"}}
	windows := map[string]quotasvc.WindowInput{"a": {CycleEndMS: &a, ObservedAtMS: now, UsedPercent: &usedA}, "b": {CycleEndMS: &b, ObservedAtMS: now, UsedPercent: &usedB}}
	assertOrder(t, planOrder(t, files, windows, now, ""), "a", "b")
	a += 604800000
	assertOrder(t, planOrder(t, files, windows, now, ""), "b", "a")
	usedB = 100
	assertOrder(t, planOrder(t, files, windows, now, ""), "a", "b")
	// An unknown quota ranks above a known exhausted one, never above quota left.
	delete(windows, "a")
	assertOrder(t, planOrder(t, files, windows, now, ""), "a", "b")
	delete(windows, "b")
	if _, err := planResetPriorities(files, windows, now, ""); !errors.Is(err, errNoWeeklyQuotas) {
		t.Fatalf("ordering without any quota: %v", err)
	}
}

func TestResetPriorityIgnoresEndedOrOldObservations(t *testing.T) {
	now := int64(10_000_000_000)
	end := now - 1
	used := 20.0
	files := []cpaauthfiles.File{{Provider: "claude", AuthIndex: "a"}}
	windows := map[string]quotasvc.WindowInput{"a": {CycleEndMS: &end, ObservedAtMS: now, UsedPercent: &used}}
	if _, err := planResetPriorities(files, windows, now, ""); err == nil {
		t.Fatal("past reset accepted")
	}
	end = now + 10000
	// A weekly reset stays valid through ordinary quota backoff.
	windows["a"] = quotasvc.WindowInput{CycleEndMS: &end, ObservedAtMS: now - time.Hour.Milliseconds(), UsedPercent: &used}
	if _, err := planResetPriorities(files, windows, now, ""); err != nil {
		t.Fatalf("hour-old observation rejected: %v", err)
	}
	windows["a"] = quotasvc.WindowInput{CycleEndMS: &end, ObservedAtMS: now - routingQuotaMaxAge.Milliseconds() - 1, UsedPercent: &used}
	if _, err := planResetPriorities(files, windows, now, ""); err == nil {
		t.Fatal("observation older than the routing limit accepted")
	}
}

// Replays Kiwi on 2026-11-05: jdoe88389's sign-in expired on Nov 2, which froze
// the whole order and left nathanqye first right after its weekly reset.
func TestResetPriorityRanksPastAnUnreadableAccount(t *testing.T) {
	now := time.Date(2026, 11, 5, 18, 50, 0, 0, time.UTC)
	hours := func(h float64) *int64 {
		v := now.Add(time.Duration(h * float64(time.Hour))).UnixMilli()
		return &v
	}
	pct := func(v float64) *float64 { return &v }
	priority := func(p string) map[string]any { return map[string]any{"priority": json.Number(p)} }
	files := []cpaauthfiles.File{
		{Name: "claude-0db9a0c2-nathanqye.json", AuthIndex: "nathanqye", Provider: "claude", Raw: priority("50")},
		{Name: "claude-3c53bcdc-pineapple.json", AuthIndex: "max-pineapple", Provider: "claude", Raw: priority("40")},
		{Name: "claude-52aa2655-pineapple.json", AuthIndex: "team-pineapple", Provider: "claude", Raw: priority("30")},
		{Name: "claude-52aa2655-jdoe88389.json", AuthIndex: "jdoe", Provider: "claude", Raw: priority("20")},
		{Name: "claude-52aa2655-enchlolzpro.json", AuthIndex: "enchlolzpro", Provider: "claude", Raw: priority("10")},
		{Name: "claude-cmu.json", AuthIndex: "cmu", Provider: "claude", Disabled: true},
	}
	observed := now.UnixMilli()
	windows := map[string]quotasvc.WindowInput{
		"nathanqye":      {CycleEndMS: hours(167), ObservedAtMS: observed, UsedPercent: pct(3)},
		"max-pineapple":  {CycleEndMS: hours(1.2), ObservedAtMS: observed, UsedPercent: pct(37)},
		"team-pineapple": {CycleEndMS: hours(12), ObservedAtMS: observed, UsedPercent: pct(7)},
		"enchlolzpro":    {CycleEndMS: hours(148), ObservedAtMS: observed, UsedPercent: pct(0)},
		// Last read three days ago, before its sign-in expired.
		"jdoe": {CycleEndMS: hours(64), ObservedAtMS: now.Add(-74 * time.Hour).UnixMilli(), UsedPercent: pct(26)},
	}
	unixMS := now.UnixMilli()
	assertOrder(t, planOrder(t, files, windows, unixMS, ""), "max-pineapple", "team-pineapple", "enchlolzpro", "nathanqye", "jdoe")
	assertOrder(t, planOrder(t, files, windows, unixMS, "nathanqye"), "nathanqye", "max-pineapple", "team-pineapple", "enchlolzpro", "jdoe")
	// A pin on the unreadable account is still honored; nothing says it is exhausted.
	assertOrder(t, planOrder(t, files, windows, unixMS, "jdoe"), "jdoe", "max-pineapple", "team-pineapple", "enchlolzpro", "nathanqye")

	plan, _ := planResetPriorities(files, windows, unixMS, "")
	if warning, paused := routingWarning(plan); paused || warning != "1 account has no current weekly quota and is ranked after accounts with quota left" {
		t.Fatalf("warning %q paused %v", warning, paused)
	}
}

func TestResetPriorityIdleAndUnknownAccounts(t *testing.T) {
	const now int64 = 10_000_000_000
	end := now + 3_600_000
	used, idle := 50.0, 0.0
	files := []cpaauthfiles.File{
		{Name: "unknown-low", AuthIndex: "u1", Provider: "claude", Raw: map[string]any{"priority": json.Number("10")}},
		{Name: "idle", AuthIndex: "idle", Provider: "claude"},
		{Name: "unknown-high", AuthIndex: "u2", Provider: "claude", Raw: map[string]any{"priority": json.Number("90")}},
		{Name: "running", AuthIndex: "running", Provider: "claude"},
	}
	windows := map[string]quotasvc.WindowInput{
		"running": {CycleEndMS: &end, ObservedAtMS: now, UsedPercent: &used},
		// An unused account has no weekly cycle yet; it would reset last.
		"idle": {ObservedAtMS: now, UsedPercent: &idle},
	}
	// Unknown accounts keep their current relative order behind known ones.
	assertOrder(t, planOrder(t, files, windows, now, ""), "running", "idle", "u2", "u1")
}

func TestResetPriorityWithoutQuotasMovesOnlyThePin(t *testing.T) {
	files := []cpaauthfiles.File{
		{Name: "a", AuthIndex: "a", Provider: "claude", Raw: map[string]any{"priority": json.Number("50")}},
		{Name: "b", AuthIndex: "b", Provider: "claude", Raw: map[string]any{"priority": json.Number("40")}},
		{Name: "c", AuthIndex: "c", Provider: "claude", Raw: map[string]any{"priority": json.Number("30")}},
		{Name: "off", AuthIndex: "off", Provider: "claude", Disabled: true, Raw: map[string]any{"priority": json.Number("90")}},
		{Name: "codex", AuthIndex: "x", Provider: "codex", Raw: map[string]any{"priority": json.Number("99")}},
	}
	assertOrder(t, planOrder(t, files, nil, 1, "c"), "c", "a", "b")
	plan, _ := planResetPriorities(files, nil, 1, "c")
	if _, paused := routingWarning(plan); !paused {
		t.Fatal("an order without quotas must be reported as paused")
	}
}
