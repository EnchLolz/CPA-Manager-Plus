package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	quotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestResetPriorityPinGoesFirstUnlessExhausted(t *testing.T) {
	const now int64 = 10000000
	a, b, c := now+100000, now+200000, now+300000
	usedA, usedB, usedC := 77.0, 0.0, 0.0
	files := []cpaauthfiles.File{{Name: "max-7h", AuthIndex: "a", Provider: "claude"}, {Name: "max-9h", AuthIndex: "b", Provider: "claude"}, {Name: "team", AuthIndex: "c", Provider: "claude"}}
	windows := map[string]quotasvc.WindowInput{
		"a": {CycleEndMS: &a, ObservedAtMS: now, UsedPercent: &usedA},
		"b": {CycleEndMS: &b, ObservedAtMS: now, UsedPercent: &usedB},
		"c": {CycleEndMS: &c, ObservedAtMS: now, UsedPercent: &usedC},
	}
	check := func(pinned string, want ...string) {
		t.Helper()
		plan, err := planResetPriorities(files, windows, now, pinned)
		if err != nil || len(plan) != len(want) {
			t.Fatalf("plan=%v err=%v", plan, err)
		}
		for i, item := range plan {
			if item.file.AuthIndex != want[i] || item.priority != (len(want)-i)*10 {
				t.Fatalf("pin %q position %d: got %s/%d, want %s", pinned, i, item.file.AuthIndex, item.priority, want[i])
			}
		}
	}
	check("", "a", "b", "c")
	check("b", "b", "a", "c")
	check("c", "c", "a", "b")
	check("missing", "a", "b", "c")
	// An exhausted pinned account cannot serve traffic, so it keeps its automatic slot.
	usedB = 100
	check("b", "a", "c", "b")
}

func TestCodexPinGoesAheadOfTierOrder(t *testing.T) {
	files := []cpaauthfiles.File{
		{Name: "codex-a-plus.json", Provider: "codex", AuthIndex: "plus", Raw: map[string]any{}},
		{Name: "codex-b-promax.json", Provider: "codex", AuthIndex: "promax", Raw: map[string]any{}},
		{Name: "codex-c-pro.json", Provider: "codex", AuthIndex: "pro", Raw: map[string]any{}},
	}
	plan := planCodexTierPriorities(files, parseCodexTierOrder(""), "promax")
	if plan[0].file.AuthIndex != "promax" || plan[0].priority != 30 || plan[1].tier != "plus" || plan[2].tier != "pro" {
		t.Fatalf("unexpected pinned codex plan %+v", plan)
	}
}

// fakeClaudeCPA serves the three CPA management routes the Claude worker uses
// and records priority writes like CPA does, as JSON numbers in the file list.
type fakeClaudeCPA struct {
	mu         sync.Mutex
	resets     map[string]time.Time
	used       map[string]float64
	denied     map[string]bool
	disabled   map[string]bool
	priorities map[string]int
	patches    int
}

func (f *fakeClaudeCPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/v0/management/auth-files":
		files := []map[string]any{}
		for _, index := range []string{"a", "b"} {
			files = append(files, map[string]any{"name": index + ".json", "provider": "claude", "auth_index": index, "disabled": f.disabled[index], "priority": f.priorities[index]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	case "/v0/management/api-call":
		var req struct {
			AuthIndex string `json:"authIndex"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if f.denied[req.AuthIndex] {
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 403, "body": "denied"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": map[string]any{"seven_day": map[string]any{"utilization": f.used[req.AuthIndex], "resets_at": f.resets[req.AuthIndex].Format(time.RFC3339)}}})
	case "/v0/management/auth-files/fields":
		var req struct {
			Name     string `json:"name"`
			Priority int    `json:"priority"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.priorities[req.Name[:len(req.Name)-len(".json")]] = req.Priority
		f.patches++
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeClaudeCPA) snapshot() (map[string]int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.priorities {
		out[k] = v
	}
	return out, f.patches
}

func TestClaudePinAppliesImmediatelyPersistsAndEnds(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("USAGE_DATA_DIR", dataDir)
	t.Setenv("CLAUDE_RESET_PRIORITY", "true")
	now := time.Now().UTC().Truncate(time.Second)
	cpa := &fakeClaudeCPA{
		resets:     map[string]time.Time{"a": now.Add(7 * time.Hour), "b": now.Add(9 * time.Hour)},
		used:       map[string]float64{"a": 77, "b": 0},
		denied:     map[string]bool{},
		disabled:   map[string]bool{},
		priorities: map[string]int{},
	}
	server := httptest.NewServer(cpa)
	defer server.Close()
	config := quotaTestConfig{store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "test"}}
	newWorker := func() *ClaudeQuotaWorker {
		w := NewClaudeQuotaWorker(config, &quotaTestWriter{})
		w.now = func() time.Time { return now }
		return w
	}
	worker := newWorker()
	if err := worker.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := cpa.snapshot(); got["a"] != 20 || got["b"] != 10 {
		t.Fatalf("automatic order not applied: %v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.pins = registerRoutingPinner("claude")
	go serveRoutingPins(ctx, nil, worker.pins, worker.setPin)
	if err := PinRouting(ctx, "claude", "b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := cpa.snapshot(); got["b"] != 20 || got["a"] != 10 {
		t.Fatalf("pin not applied: %v", got)
	}
	status := CurrentRoutingStatus()["claude"]
	if status.Pin == nil || status.Pin.UntilMS != cpa.resets["b"].UnixMilli() || !status.Order[0].Pinned || status.Order[0].AuthIndex != "b" || status.Order[1].Pinned {
		t.Fatalf("pin status: %+v", status)
	}
	if err := PinRouting(ctx, "claude", "missing"); !errors.Is(err, ErrRoutingPinTarget) {
		t.Fatalf("unknown credential accepted: %v", err)
	}
	if err := PinRouting(ctx, "gemini", "b"); !errors.Is(err, ErrRoutingUnavailable) {
		t.Fatalf("provider without a worker accepted: %v", err)
	}

	// A restart keeps the pin, and unchanged priorities are not rewritten.
	restarted := newWorker()
	if restarted.pin == nil || restarted.pin.AuthIndex != "b" {
		t.Fatalf("pin not restored: %+v", restarted.pin)
	}
	_, before := cpa.snapshot()
	if err := restarted.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, after := cpa.snapshot(); after != before || got["b"] != 20 {
		t.Fatalf("restart rewrote priorities: %v patches %d -> %d", got, before, after)
	}

	// A quota read that starts failing keeps the last observation, the pin, and
	// the order, reports the failure, and defers the next read.
	cpa.mu.Lock()
	cpa.denied["b"] = true
	cpa.priorities["a"], cpa.priorities["b"] = 20, 10
	cpa.mu.Unlock()
	if err := restarted.poll(context.Background()); err != nil {
		t.Fatalf("one unreadable quota stopped routing: %v", err)
	}
	if got, _ := cpa.snapshot(); got["b"] != 20 || got["a"] != 10 {
		t.Fatalf("pin dropped while a quota was unreadable: %v", got)
	}
	status = CurrentRoutingStatus()["claude"]
	if first := status.Order[0]; first.AuthIndex != "b" || first.QuotaUnknown || first.QuotaErrorStatus != 403 || first.QuotaError == "" || status.LastError != "" {
		t.Fatalf("failing quota status: %+v", status)
	}
	if !restarted.retries["b"].Until.After(now) {
		t.Fatalf("rejected credential not deferred: %+v", restarted.retries["b"])
	}
	cpa.mu.Lock()
	cpa.denied["b"] = false
	cpa.mu.Unlock()

	// The pin ends at the pinned account's weekly reset.
	restarted.pin.UntilMS = now.UnixMilli()
	if err := restarted.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := cpa.snapshot(); restarted.pin != nil || got["a"] != 20 || got["b"] != 10 {
		t.Fatalf("expired pin still applied: %+v %v", restarted.pin, got)
	}
	if _, err := os.Stat(routingPinPath(dataDir, "claude")); !os.IsNotExist(err) {
		t.Fatalf("expired pin still persisted: %v", err)
	}

	// Disabling the pinned credential clears the pin.
	if err := restarted.setPin(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	cpa.mu.Lock()
	cpa.disabled["b"] = true
	cpa.mu.Unlock()
	if err := restarted.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarted.pin != nil || CurrentRoutingStatus()["claude"].Pin != nil {
		t.Fatalf("pin on a disabled credential kept: %+v", restarted.pin)
	}
}
