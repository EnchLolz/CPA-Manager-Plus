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
		got, err := parseClaudeQuota(json.RawMessage(payload), now)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: %+v %v", payload, got, err)
		}
	}
	got, err := parseClaudeQuota(json.RawMessage(`{"limits":[{"kind":"weekly_all","percent":0,"resets_at":"2026-09-29T00:00:00Z"}]}`), now)
	if err != nil || len(got) != 1 || *got[0].RemainingPercent != 100 {
		t.Fatalf("zero utilization must remain valid: %+v %v", got, err)
	}
}
