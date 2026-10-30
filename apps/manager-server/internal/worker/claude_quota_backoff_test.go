package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestClaudeQuotaBackoffSurvivesRestartWithoutBlockingHealthyAccounts(t *testing.T) {
	t.Setenv("USAGE_DATA_DIR", t.TempDir())
	t.Setenv("CLAUDE_RESET_PRIORITY", "false")
	now := time.Now().UTC().Truncate(time.Second)
	base := now
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[{"name":"limited.json","provider":"claude","auth_index":"limited"},{"name":"healthy.json","provider":"claude","auth_index":"healthy"}]}`))
			return
		}
		var req struct {
			AuthIndex string `json:"authIndex"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		calls[req.AuthIndex]++
		if req.AuthIndex == "limited" && calls[req.AuthIndex] <= 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 429, "header": map[string][]string{"Retry-After": {"1200"}}, "body": "rate limited"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": map[string]any{"seven_day": map[string]any{"utilization": 25, "resets_at": base.Add(24 * time.Hour).Format(time.RFC3339)}}})
	}))
	defer server.Close()
	config := quotaTestConfig{store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "test"}}
	writer := &quotaTestWriter{}
	worker := NewClaudeQuotaWorker(config, writer)
	worker.now = func() time.Time { return now }
	poll := func() {
		t.Helper()
		if err := worker.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	if calls["limited"] != 1 || calls["healthy"] != 1 || len(writer.requests) != 1 {
		t.Fatalf("unexpected first poll: %v", calls)
	}
	if got := worker.retries["limited"].Until; !got.Equal(base.Add(20 * time.Minute)) {
		t.Fatalf("retry deadline = %v", got)
	}
	if info, err := os.Stat(worker.retryPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("retry state permissions: %v %v", info, err)
	}
	// Restarting the process must not bypass Retry-After.
	worker = NewClaudeQuotaWorker(config, writer)
	worker.now = func() time.Time { return now }
	now = base.Add(5 * time.Minute)
	poll()
	if calls["limited"] != 1 || calls["healthy"] != 2 {
		t.Fatalf("retried during cooldown: %v", calls)
	}
	now = base.Add(20 * time.Minute)
	poll()
	if calls["limited"] != 2 || worker.retries["limited"].Failures != 2 {
		t.Fatalf("missing retry: %v", calls)
	}
	now = base.Add(25 * time.Minute)
	poll()
	if calls["limited"] != 2 {
		t.Fatal("second cooldown was ignored")
	}
	now = base.Add(40 * time.Minute)
	poll()
	if calls["limited"] != 3 || calls["healthy"] != 5 {
		t.Fatalf("unexpected recovery: %v", calls)
	}
	if _, ok := worker.retries["limited"]; ok {
		t.Fatal("successful refresh must clear cooldown")
	}
}

func TestClaudeQuotaRetryAfterFormatsAndFallback(t *testing.T) {
	now := time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		value string
		delay time.Duration
	}{
		{"1200", 20 * time.Minute},
		{now.Add(time.Hour).Format(http.TimeFormat), time.Hour},
		{"", 10 * time.Minute}, {"invalid", 10 * time.Minute}, {"-1", 10 * time.Minute},
	} {
		if got := claudeQuotaRetryAfter(item.value, now); !got.Equal(now.Add(item.delay)) {
			t.Errorf("%q: %v", item.value, got)
		}
	}
	for _, raw := range []string{`{"Retry-After":["1200"]}`, `{"retry-after":"1200"}`} {
		var headers map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			t.Fatal(err)
		}
		if got := claudeQuotaRetryHeader(headers); got != "1200" {
			t.Errorf("header = %q", got)
		}
	}
	w := NewClaudeQuotaWorker(nil, nil)
	w.now = func() time.Time { return now }
	for _, delay := range []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour} {
		if got := w.deferAccount("one", now); !got.Equal(now.Add(delay)) {
			t.Errorf("backoff = %v, want %v", got, delay)
		}
	}
}
