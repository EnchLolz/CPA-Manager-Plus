package worker

import (
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	quotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"testing"
)

func TestResetPriorityRolloverAndExhaustion(t *testing.T) {
	const now int64 = 10000000
	a, b := now+100000, now+200000
	usedA, usedB := 20.0, 40.0
	files := []cpaauthfiles.File{{Name: "max", AuthIndex: "a", Provider: "claude"}, {Name: "team", AuthIndex: "b", Provider: "claude"}, {Name: "disabled", Provider: "claude", Disabled: true}, {Name: "codex", Provider: "codex"}}
	windows := map[string]quotasvc.WindowInput{"a": {CycleEndMS: &a, ObservedAtMS: now, UsedPercent: &usedA}, "b": {CycleEndMS: &b, ObservedAtMS: now, UsedPercent: &usedB}}
	check := func(want string) {
		t.Helper()
		p, e := planResetPriorities(files, windows, now)
		if e != nil || len(p) != 2 || p[0].file.Name != want || p[0].priority <= p[1].priority {
			t.Fatalf("plan=%v err=%v", p, e)
		}
	}
	check("max")
	a += 604800000
	check("team")
	usedB = 100
	check("max")
	delete(windows, "a")
	if _, e := planResetPriorities(files, windows, now); e == nil {
		t.Fatal("missing quota allowed mutation")
	}
}
func TestResetPriorityRejectsExpiredOrStaleObservation(t *testing.T) {
	now := int64(10000000)
	end := now - 1
	used := 20.0
	f := []cpaauthfiles.File{{Provider: "claude", AuthIndex: "a"}}
	q := map[string]quotasvc.WindowInput{"a": {CycleEndMS: &end, ObservedAtMS: now, UsedPercent: &used}}
	if _, e := planResetPriorities(f, q, now); e == nil {
		t.Fatal("past reset accepted")
	}
	end = now + 10000
	v := q["a"]
	v.ObservedAtMS = now - 16*60*1000
	q["a"] = v
	if _, e := planResetPriorities(f, q, now); e == nil {
		t.Fatal("stale observation accepted")
	}
}
