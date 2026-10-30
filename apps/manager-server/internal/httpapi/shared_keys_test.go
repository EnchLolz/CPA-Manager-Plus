package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	sharedkeysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/sharedkey"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

type sharedFixture struct {
	server *httptest.Server
	db     *store.Store
	calls  atomic.Int64
	mode   atomic.Value
}

func newSharedFixture(t *testing.T) *sharedFixture {
	t.Helper()
	f := &sharedFixture{}
	f.mode.Store("json")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/api-keys" {
			if r.Header.Get("Authorization") != "Bearer management-key" {
				t.Error("management authentication missing")
			}
			_, _ = io.WriteString(w, `{"api-keys":["private-upstream-key"]}`)
			return
		}
		f.calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-upstream-key" {
			t.Error("upstream client authentication missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch f.mode.Load().(string) {
		case "missing":
			_, _ = io.WriteString(w, `{"choices":[]}`)
		case "rejected":
			w.WriteHeader(429)
		case "stream":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			options, _ := body["stream_options"].(map[string]any)
			if options["include_usage"] != true {
				t.Error("stream usage not requested")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n")
		case "messages":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"cache_read_input_tokens\":2}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
		case "responses":
			_, _ = io.WriteString(w, `{"status":"completed","usage":{"input_tokens":6,"output_tokens":4}}`)
		default:
			_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":6,"completion_tokens":4},"choices":[]}`)
		}
	}))
	t.Cleanup(upstream.Close)
	var err error
	f.db, err = store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	testutil.EnsureAdminCredential(t, f.db)
	if err := f.db.SaveSetup(context.Background(), store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveModelPrices(context.Background(), map[string]store.ModelPrice{"test-model": {Prompt: 1, Completion: 2, Cache: 0.5}}); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(New(config.Config{}, f.db, nil).Handler())
	t.Cleanup(f.server.Close)
	return f
}

func (f *sharedFixture) request(t *testing.T, method, path, key, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}

func (f *sharedFixture) create(t *testing.T, tokens, cost int64, expiry int64) (string, string) {
	t.Helper()
	status, data := f.request(t, "POST", "/v0/management/shared-keys", testutil.AdminKey, fmt.Sprintf(`{"name":"Friend","models":["test-model"],"expires_at":%d,"max_tokens":%d,"max_cost_micros":%d}`, expiry, tokens, cost))
	if status != 201 {
		t.Fatalf("create status=%d body=%s", status, data)
	}
	var result struct {
		Key  string          `json:"key"`
		Item store.SharedKey `json:"item"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result.Key, result.Item.ID
}

func TestSharedKeysHTTPEnforcesLimitsAndRevocation(t *testing.T) {
	f := newSharedFixture(t)
	key, id := f.create(t, 10, 0, time.Now().Add(time.Hour).UnixMilli())
	status, data := f.request(t, "GET", "/v0/management/shared-keys", testutil.AdminKey, "")
	if status != 200 || bytes.Contains(data, []byte(key)) || bytes.Contains(data, []byte("private-upstream-key")) {
		t.Fatalf("list leaked secrets: %d %s", status, data)
	}
	if status, _ := f.request(t, "GET", "/v0/management/shared-keys", key, ""); status != 401 {
		t.Fatalf("shared key accessed admin: %d", status)
	}
	if status, _ := f.request(t, "POST", "/share/v1/chat/completions", key, `{"model":"forbidden"}`); status != 403 {
		t.Fatalf("model restriction: %d", status)
	}
	if status, _ := f.request(t, "GET", "/share/v1/models", key, ""); status != 200 {
		t.Fatalf("models: %d", status)
	}
	if status, _ := f.request(t, "POST", "/share/v1/chat/completions", key, `{"model":"test-model"}`); status != 200 {
		t.Fatalf("inference: %d", status)
	}
	if status, _ := f.request(t, "POST", "/share/v1/chat/completions", key, `{"model":"test-model"}`); status != 429 {
		t.Fatalf("token cap: %d", status)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("unexpected forwarded calls: %d", f.calls.Load())
	}
	if status, _ := f.request(t, "DELETE", "/v0/management/shared-keys/"+id, testutil.AdminKey, ""); status != 200 {
		t.Fatalf("revoke: %d", status)
	}
	if status, _ := f.request(t, "GET", "/share/v1/models", key, ""); status != 401 {
		t.Fatalf("revoked key: %d", status)
	}
}

func TestSharedKeysHTTPStreamingAndSpend(t *testing.T) {
	for _, mode := range []string{"stream", "messages", "responses"} {
		t.Run(mode, func(t *testing.T) {
			f := newSharedFixture(t)
			f.mode.Store(mode)
			key, _ := f.create(t, 0, 10, time.Now().Add(time.Hour).UnixMilli())
			path := "/share/v1/chat/completions"
			if mode == "messages" {
				path = "/share/v1/messages"
			}
			if mode == "responses" {
				path = "/share/v1/responses"
			}
			status, data := f.request(t, "POST", path, key, `{"model":"test-model","stream":true}`)
			if status != 200 {
				t.Fatalf("request: %d %s", status, data)
			}
			// Response streaming may reach the client before final accounting commits.
			deadline := time.Now().Add(5 * time.Second)
			for {
				rows, err := f.db.ListSharedKeys(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if !rows[0].InFlight {
					if rows[0].UsedTokens != 10 || rows[0].UsedCostMicros < 10 || rows[0].Blocked {
						t.Fatalf("accounting: %+v", rows[0])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("accounting did not complete")
				}
				time.Sleep(time.Millisecond)
			}
			if status, _ := f.request(t, "POST", path, key, `{"model":"test-model"}`); status != 429 {
				t.Fatalf("spend cap: %d", status)
			}
		})
	}
}

func TestSharedKeysHTTPUnknownUsageKeepsKeyUsableAndExpiryBlocks(t *testing.T) {
	f := newSharedFixture(t)
	f.mode.Store("missing")
	key, _ := f.create(t, 100, 0, time.Now().Add(time.Hour).UnixMilli())
	f.request(t, "POST", "/share/v1/chat/completions", key, `{"model":"test-model"}`)
	if status, _ := f.request(t, "POST", "/share/v1/chat/completions", key, `{"model":"test-model"}`); status != 200 {
		t.Fatalf("unknown usage paused the key: %d", status)
	}
	rows, err := f.db.ListSharedKeys(context.Background())
	if err != nil || rows[0].Blocked {
		t.Fatalf("unknown usage blocked the key: %v %+v", err, rows)
	}
	key = "sk-share-expired"
	if err := f.db.CreateSharedKey(context.Background(), store.SharedKey{ID: "expired", Name: "Expired", Models: []string{"test-model"}, ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(), MaxTokens: 100}, sharedkeysvc.Hash(key)); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.request(t, "GET", "/share/v1/models", key, ""); status != 401 {
		t.Fatalf("expiry: %d", status)
	}
}

func TestSharedKeysHTTPRejectsUnsafeOptionsWithoutForwarding(t *testing.T) {
	f := newSharedFixture(t)
	key, _ := f.create(t, 100, 0, time.Now().Add(time.Hour).UnixMilli())
	for _, body := range []string{`{"model":"test-model","background":true}`, `{"model":"test-model","store":true}`, `{"model":"test-model","previous_response_id":"other-response"}`} {
		if status, _ := f.request(t, "POST", "/share/v1/responses", key, body); status != 400 {
			t.Fatalf("unsafe option accepted: %d", status)
		}
	}
	if status, _ := f.request(t, "POST", "/share/v1/management/api-keys", key, `{}`); status != 404 {
		t.Fatalf("unexpected route: %d", status)
	}
	if f.calls.Load() != 0 {
		t.Fatal("invalid request forwarded")
	}
}

func TestSharedKeysHTTPRequiresPricingForSpendCaps(t *testing.T) {
	f := newSharedFixture(t)
	if err := f.db.SaveModelPrices(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"name":"Friend","models":["test-model"],"expires_at":%d,"max_cost_micros":100}`, time.Now().Add(time.Hour).UnixMilli())
	if status, _ := f.request(t, "POST", "/v0/management/shared-keys", testutil.AdminKey, body); status != 400 {
		t.Fatalf("unpriced spend allowance accepted: %d", status)
	}
	key, _ := f.create(t, 10, 0, time.Now().Add(time.Hour).UnixMilli())
	if status, _ := f.request(t, "POST", "/share/v1/chat/completions", key, `{"model":"test-model"}`); status != 200 {
		t.Fatalf("token-only key requires prices: %d", status)
	}
}

func TestSharedKeysHTTPAnthropicCORSAndHeaderAuthentication(t *testing.T) {
	f := newSharedFixture(t)
	key, _ := f.create(t, 100, 0, time.Now().Add(time.Hour).UnixMilli())
	req, _ := http.NewRequest("OPTIONS", f.server.URL+"/share/v1/messages", nil)
	req.Header.Set("Origin", "https://client.example")
	req.Header.Set("Access-Control-Request-Headers", "x-api-key,anthropic-version")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "X-Api-Key") {
		t.Fatalf("Anthropic preflight failed: %d %v", resp.StatusCode, resp.Header)
	}
	req, _ = http.NewRequest("GET", f.server.URL+"/share/v1/models", nil)
	req.Header.Set("x-api-key", key)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Anthropic header authentication: %d", resp.StatusCode)
	}
}
