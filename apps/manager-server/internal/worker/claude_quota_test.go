package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	quotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type quotaTestConfig struct{ setup store.Setup }

func (c quotaTestConfig) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return c.setup, true, nil
}

type quotaTestWriter struct{ requests []quotasvc.WriteRequest }

func (w *quotaTestWriter) Write(_ context.Context, r quotasvc.WriteRequest) (quotasvc.WriteResponse, error) {
	w.requests = append(w.requests, r)
	return quotasvc.WriteResponse{}, nil
}

func TestClaudeQuotaPollingRoutesByAccountAndPersistsWithoutBrowser(t *testing.T) {
	writer := &quotaTestWriter{}
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-test" {
			t.Error("missing CPA authentication")
		}
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"name":"personal.json","provider":"claude","auth_index":"personal"},{"name":"org.json","provider":"claude","auth_index":"org"},{"name":"other.json","provider":"codex","auth_index":"other"}]}`))
		case "/v0/management/api-call":
			calls++
			var request struct {
				AuthIndex string            `json:"authIndex"`
				URL       string            `json:"url"`
				Method    string            `json:"method"`
				Header    map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.URL != "https://api.anthropic.com/api/oauth/usage" || request.Method != "GET" || request.Header["Authorization"] != "Bearer $TOKEN$" {
				t.Error("must use CPA token substitution for a read-only quota request")
			}
			if request.AuthIndex == "org" {
				_, _ = w.Write([]byte(`{"status_code":403,"body":"not authorized"}`))
				return
			}
			payload := map[string]any{"seven_day": map[string]any{"utilization": 40, "resets_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}}
			// CPA supports both JSON and string bodies.
			encoded, _ := json.Marshal(payload)
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": string(encoded)})
		default:
			t.Errorf("unexpected action %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	worker := NewClaudeQuotaWorker(quotaTestConfig{store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "management-test"}}, writer)
	if err := worker.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(writer.requests) != 1 {
		t.Fatalf("calls=%d saved=%d", calls, len(writer.requests))
	}
	entry := writer.requests[0].Entries[0]
	if entry.Account.AuthIndex != "personal" || entry.Account.AuthFileSnapshot != "personal.json" {
		t.Fatalf("wrong account: %+v", entry.Account)
	}
	if *entry.Windows[0].RemainingPercent != 60 || *entry.Windows[0].DurationSeconds != 604800 {
		t.Fatalf("wrong quota: %+v", entry.Windows[0])
	}
	// Exercise real validation and persistence, not just a mock write contract.
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = quotasvc.New(st).Write(context.Background(), writer.requests[0]); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeQuotaUnknownIsNotUnlimited(t *testing.T) {
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).UnixMilli()
	for _, payload := range []string{
		`{}`, `{"seven_day":null}`, `{"limits":[]}`,
		`{"seven_day":{"utilization":-1,"resets_at":"2026-09-29T00:00:00Z"}}`,
		`{"seven_day":{"utilization":20,"resets_at":"2026-09-20T00:00:00Z"}}`,
		`{"limits":[{"kind":"weekly","scope":{"model":"opus"},"percent":20,"resets_at":"2026-09-29T00:00:00Z"}]}`,
	} {
		got, complete, err := parseClaudeQuota(json.RawMessage(payload), now)
		if err != nil || len(got) != 0 || complete {
			t.Fatalf("%s: %+v complete=%v %v", payload, got, complete, err)
		}
	}
	got, _, err := parseClaudeQuota(json.RawMessage(`{"limits":[{"kind":"weekly_all","percent":0,"resets_at":"2026-09-29T00:00:00Z"}]}`), now)
	if err != nil || len(got) != 1 || *got[0].RemainingPercent != 100 {
		t.Fatalf("zero utilization must remain valid: %+v %v", got, err)
	}
}

// Shapes of the live usage payloads from 2026-11-05: Max plans report
// five_hour as null and no session limit; Team reports an idle five-hour window.
const (
	maxUsagePayload  = `{"five_hour":null,"seven_day":{"utilization":3.0,"resets_at":"2026-11-12T18:00:00.103628+00:00"},"seven_day_opus":null,"seven_day_sonnet":null,"limits":[{"kind":"weekly_all","group":"weekly","percent":3,"resets_at":"2026-11-12T18:00:00.103628+00:00","scope":null,"is_active":true},{"kind":"weekly_scoped","group":"weekly","percent":0,"resets_at":"2026-11-12T18:00:00+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}]}`
	teamUsagePayload = `{"five_hour":{"utilization":0.0,"resets_at":null},"seven_day":{"utilization":0.0,"resets_at":"2026-11-11T23:00:00+00:00"},"seven_day_opus":null,"seven_day_sonnet":null,"limits":[{"kind":"session","group":"session","percent":0,"resets_at":null,"scope":null,"is_active":true},{"kind":"weekly_all","group":"weekly","percent":0,"resets_at":"2026-11-11T23:00:00+00:00","scope":null,"is_active":false}]}`
)

func TestClaudeQuotaInventoryKeepsIdleAndOmitsDroppedWindows(t *testing.T) {
	now := time.Date(2026, 11, 5, 18, 57, 0, 0, time.UTC).UnixMilli()
	ids := func(windows []quotasvc.WindowInput) map[string]quotasvc.WindowInput {
		out := map[string]quotasvc.WindowInput{}
		for _, w := range windows {
			out[w.ProviderWindowID] = w
		}
		return out
	}
	got, complete, err := parseClaudeQuota(json.RawMessage(maxUsagePayload), now)
	if byID := ids(got); err != nil || !complete || len(got) != 1 || byID["seven-day"].WindowMode != "fixed" {
		t.Fatalf("max: %+v complete=%v %v", got, complete, err)
	}
	got, complete, err = parseClaudeQuota(json.RawMessage(teamUsagePayload), now)
	byID := ids(got)
	idle, ok := byID["five-hour"]
	if err != nil || !complete || len(got) != 2 || !ok || idle.WindowMode != "unknown" || idle.CycleEndMS != nil || *idle.UsedPercent != 0 || *idle.DurationSeconds != 18000 {
		t.Fatalf("team: %+v complete=%v %v", got, complete, err)
	}
	// A reported window that cannot be read must not retire stored windows.
	got, complete, err = parseClaudeQuota(json.RawMessage(`{"five_hour":{"utilization":10,"resets_at":"2026-11-05T20:00:00Z"},"seven_day":{"utilization":10,"resets_at":"bad"}}`), now)
	if err != nil || len(got) != 1 || complete {
		t.Fatalf("unreadable window treated as complete: %+v %v", got, err)
	}
}

// Drives the worker against the real snapshot store, as the Overview reads it:
// a window the provider drops disappears after two complete observations, and
// an idle window stays.
func TestClaudeQuotaRetiresWindowsTheProviderDrops(t *testing.T) {
	t.Setenv("USAGE_DATA_DIR", t.TempDir())
	t.Setenv("CLAUDE_RESET_PRIORITY", "false")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	activeFiveHour := `{"five_hour":{"utilization":40,"resets_at":"2026-10-30T14:00:00Z"},"seven_day":{"utilization":10,"resets_at":"2026-11-05T20:00:00Z"}}`
	payloads := map[string]string{"max": activeFiveHour, "team": activeFiveHour}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[{"name":"max.json","provider":"claude","auth_index":"max"},{"name":"team.json","provider":"claude","auth_index":"team"}]}`))
			return
		}
		var req struct {
			AuthIndex string `json:"authIndex"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": payloads[req.AuthIndex]})
	}))
	defer server.Close()
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	snapshots := quotasvc.New(st)
	worker := NewClaudeQuotaWorker(quotaTestConfig{store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "test"}}, snapshots)
	worker.now = func() time.Time { return now }
	poll := func() {
		t.Helper()
		if err := worker.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
	}
	query := func(index string) map[string]quotasvc.Window {
		t.Helper()
		res, err := snapshots.Query(context.Background(), quotasvc.QueryRequest{NowMS: now.UnixMilli(), Accounts: []quotasvc.QueryAccount{{
			RowKey: index, Provider: "claude",
			Account: quotasvc.AccountTarget{AuthFileSnapshot: index + ".json", AuthProviderSnapshot: "claude", AuthIndex: index, Source: index + ".json"},
		}}})
		if err != nil || len(res.Items) != 1 {
			t.Fatalf("query %s: %+v %v", index, res, err)
		}
		out := map[string]quotasvc.Window{}
		for _, w := range res.Items[0].Windows {
			out[w.ProviderWindowID] = w
		}
		return out
	}
	poll()
	if _, ok := query("max")["five-hour"]; !ok {
		t.Fatal("active five-hour window not stored")
	}
	// After the five-hour window ends, Anthropic reports none for Max and an
	// idle one for Team.
	now = time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)
	payloads["max"] = maxUsagePayload
	payloads["team"] = teamUsagePayload
	poll()
	if _, ok := query("max")["five-hour"]; !ok {
		t.Fatal("one missed observation must not retire a window")
	}
	poll()
	maxWindows, teamWindows := query("max"), query("team")
	if _, ok := maxWindows["five-hour"]; ok {
		t.Fatalf("dropped five-hour window still shown: %+v", maxWindows["five-hour"])
	}
	if _, ok := maxWindows["seven-day"]; !ok {
		t.Fatal("weekly window lost")
	}
	// Before, the idle window was never written and showed the last active
	// cycle's usage (60% left) indefinitely.
	idle, ok := teamWindows["five-hour"]
	if !ok || idle.UsedPercent == nil || *idle.UsedPercent != 0 || idle.Availability != "active" {
		t.Fatalf("idle five-hour window lost: %+v", teamWindows)
	}
}
