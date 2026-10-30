// Package sharedkey provides a separate, restricted gateway for sharing CPA access.
package sharedkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const Prefix = "/share/v1"
const maxBody = 16 << 20

type Service struct {
	Store  *store.Store
	Config *managerconfig.Service
	client *http.Client
}

func New(st *store.Store, cfg *managerconfig.Service) *Service {
	return &Service{Store: st, Config: cfg, client: &http.Client{
		Timeout:       15 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type CreateRequest struct {
	Name          string   `json:"name"`
	Models        []string `json:"models"`
	ExpiresAt     int64    `json:"expires_at"`
	MaxTokens     int64    `json:"max_tokens"`
	MaxCostMicros int64    `json:"max_cost_micros"`
}

func Hash(key string) string { sum := sha256.Sum256([]byte(key)); return hex.EncodeToString(sum[:]) }

func (s *Service) Create(ctx context.Context, req CreateRequest) (store.SharedKey, string, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 120 {
		return store.SharedKey{}, "", errors.New("name must contain 1 to 120 bytes")
	}
	if req.ExpiresAt <= time.Now().UnixMilli() || req.ExpiresAt > time.Now().AddDate(1, 0, 0).UnixMilli() {
		return store.SharedKey{}, "", errors.New("expiration must be in the future and within one year")
	}
	if req.MaxTokens < 0 || req.MaxTokens > 1e12 || req.MaxCostMicros < 0 || req.MaxCostMicros > 1e12 || (req.MaxTokens == 0 && req.MaxCostMicros == 0) {
		return store.SharedKey{}, "", errors.New("set a positive token or spend allowance")
	}
	if len(req.Models) == 0 || len(req.Models) > 100 {
		return store.SharedKey{}, "", errors.New("choose 1 to 100 allowed models")
	}
	for i := range req.Models {
		req.Models[i] = strings.TrimSpace(req.Models[i])
		if req.Models[i] == "" || len(req.Models[i]) > 200 || strings.ContainsAny(req.Models[i], "*\r\n") {
			return store.SharedKey{}, "", errors.New("use exact model names, without wildcards")
		}
	}
	slices.Sort(req.Models)
	req.Models = slices.Compact(req.Models)
	if req.MaxCostMicros > 0 {
		prices, err := s.Store.LoadModelPrices(ctx)
		if err != nil {
			return store.SharedKey{}, "", err
		}
		for _, name := range req.Models {
			if !hasPrice(prices, name) {
				return store.SharedKey{}, "", fmt.Errorf("configure input and output prices for %s before setting a spend allowance", name)
			}
		}
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return store.SharedKey{}, "", err
	}
	secret := "sk-share-" + hex.EncodeToString(random)
	key := store.SharedKey{ID: Hash(secret)[:24], Name: req.Name, Models: req.Models, CreatedAt: time.Now().UnixMilli(), ExpiresAt: req.ExpiresAt, MaxTokens: req.MaxTokens, MaxCostMicros: req.MaxCostMicros}
	if err := s.Store.CreateSharedKey(ctx, key, Hash(secret)); err != nil {
		return store.SharedKey{}, "", err
	}
	return key, secret, nil
}

func hasPrice(prices map[string]model.ModelPrice, name string) bool {
	p, ok := prices[name]
	return ok && (p.Prompt > 0 || p.PromptConfigured) && (p.Completion > 0 || p.CompletionConfigured)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": code, "message": message}})
}

func (s *Service) Gateway(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	secret := security.ExtractBearerToken(r.Header.Get("Authorization"))
	if secret == "" {
		secret = r.Header.Get("X-Api-Key")
	}
	if !strings.HasPrefix(secret, "sk-share-") {
		writeError(w, 401, "invalid_api_key", "A shared API key is required")
		return
	}
	key, err := s.Store.FindSharedKey(r.Context(), Hash(secret))
	if err != nil {
		if store.IsSharedKeyNotFound(err) {
			writeError(w, 401, "invalid_api_key", "Invalid shared API key")
		} else {
			writeError(w, 503, "gateway_unavailable", "Shared key storage is unavailable")
		}
		return
	}
	if key.Revoked || key.ExpiresAt <= time.Now().UnixMilli() {
		writeError(w, 401, "key_expired", "This shared key has expired or been revoked")
		return
	}
	if (key.MaxTokens > 0 && key.UsedTokens >= key.MaxTokens) || (key.MaxCostMicros > 0 && key.UsedCostMicros >= key.MaxCostMicros) {
		writeError(w, 429, "allowance_exhausted", "This shared key has reached its allowance")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, Prefix)
	if path == "/models" && r.Method == http.MethodGet {
		data := []map[string]string{}
		for _, name := range key.Models {
			data = append(data, map[string]string{"id": name, "object": "model", "owned_by": "shared"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		return
	}
	if r.Method != http.MethodPost || (path != "/chat/completions" && path != "/responses" && path != "/messages") {
		writeError(w, 404, "unsupported_endpoint", "Supported endpoints: models, chat/completions, responses, messages")
		return
	}
	var body map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&body); err != nil || body == nil {
		writeError(w, 400, "invalid_request", "Expected a JSON request under 16 MiB")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeError(w, 400, "invalid_request", "Expected one JSON object")
		return
	}
	var name string
	if json.Unmarshal(body["model"], &name) != nil || !slices.Contains(key.Models, name) {
		writeError(w, 403, "model_not_allowed", "This model is not allowed by the shared key")
		return
	}
	// Background jobs and stored response chaining outlive the accounting scope.
	for _, field := range []string{"background", "store"} {
		if value, ok := body[field]; ok {
			var enabled bool
			if json.Unmarshal(value, &enabled) != nil || enabled {
				writeError(w, 400, "unsupported_option", field+" must be false for shared keys")
				return
			}
		}
	}
	if _, ok := body["previous_response_id"]; ok {
		writeError(w, 400, "unsupported_option", "Use explicit conversation input instead of previous_response_id")
		return
	}
	if path == "/responses" {
		body["store"] = json.RawMessage("false")
	}
	var streaming bool
	if value, ok := body["stream"]; ok && json.Unmarshal(value, &streaming) != nil {
		writeError(w, 400, "invalid_request", "stream must be a boolean")
		return
	}
	if streaming && path == "/chat/completions" {
		body["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	prices, err := s.Store.LoadModelPrices(r.Context())
	if err != nil || (key.MaxCostMicros > 0 && !hasPrice(prices, name)) {
		writeError(w, 503, "pricing_unavailable", "Model pricing is unavailable; contact the key owner")
		return
	}
	setup, ok, err := s.Config.ResolveSetup(r.Context())
	if err != nil || !ok {
		writeError(w, 503, "upstream_unavailable", "CPA connection is unavailable")
		return
	}
	upstreamKey, err := s.upstreamKey(r.Context(), setup)
	if err != nil {
		writeError(w, 503, "upstream_unavailable", "CPA client authentication is unavailable")
		return
	}
	base, err := url.Parse(strings.TrimRight(setup.CPAUpstreamURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		writeError(w, 503, "upstream_unavailable", "CPA URL is invalid")
		return
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1" + path
	base.RawPath, base.RawQuery, base.Fragment = "", "", ""
	payload, _ := json.Marshal(body)
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base.String(), bytes.NewReader(payload))
	if err != nil {
		writeError(w, 500, "gateway_error", "Unable to prepare request")
		return
	}
	up.Header.Set("Authorization", "Bearer "+upstreamKey)
	up.Header.Set("Content-Type", "application/json")
	for _, header := range []string{"Anthropic-Version", "Anthropic-Beta"} {
		if value := r.Header.Get(header); value != "" {
			up.Header.Set(header, value)
		}
	}
	acquired, err := s.Store.AcquireSharedKey(r.Context(), key.ID)
	if err != nil || !acquired {
		writeError(w, 429, "key_unavailable", "Shared key is busy, expired, revoked, or exhausted")
		return
	}
	usage := newMeter(path)
	_ = json.Unmarshal(body["service_tier"], &usage.tier)
	known := false
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cost := int64(0)
		if usage.seen && hasPrice(prices, name) {
			amount := pricing.CostForModelWithServiceTier(name, usage.tier, usage.tokens, prices) * 1e6
			if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > 1e15 {
				known = false
			} else {
				cost = int64(math.Ceil(amount))
			}
		}
		if !known {
			log.Printf("shared key %s: usage for %s could not be fully confirmed; charged %d observed tokens", key.ID, name, usage.total())
		}
		if err := s.Store.FinishSharedRequest(ctx, key.ID, usage.total(), cost); err != nil {
			log.Printf("shared key %s accounting failed: %v", key.ID, err)
		}
	}()
	resp, err := s.client.Do(up)
	if err != nil {
		writeError(w, 502, "upstream_error", "CPA request failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Only explicit client rejections are known not to have generated tokens.
		known = resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 429
		writeError(w, resp.StatusCode, "upstream_error", "CPA rejected the request")
		return
	}
	isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	if isSSE {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		controller := http.NewResponseController(w)
		buffer := make([]byte, 32<<10)
		for {
			n, readErr := resp.Body.Read(buffer)
			if n > 0 {
				usage.feed(buffer[:n])
				if _, err := w.Write(buffer[:n]); err != nil {
					return
				}
				if err := controller.Flush(); err != nil {
					return
				}
			}
			if readErr != nil {
				known = readErr == io.EOF && usage.complete && usage.seen && !usage.invalid
				return
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil || len(data) > 32<<20 {
		writeError(w, 502, "upstream_error", "CPA response could not be accounted for")
		return
	}
	usage.consume(data)
	known = usage.seen && !usage.invalid
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

// Upstream credentials remain on the server and are never stored in shared keys.
func (s *Service) upstreamKey(ctx context.Context, setup store.Setup) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(setup.CPAUpstreamURL, "/")+"/v0/management/api-keys", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := s.client.Do(req.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("CPA key lookup failed")
	}
	var result struct {
		Keys      []string `json:"api-keys"`
		CamelKeys []string `json:"apiKeys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	for _, key := range append(result.Keys, result.CamelKeys...) {
		if strings.TrimSpace(key) != "" {
			return key, nil
		}
	}
	return "", errors.New("CPA has no client key")
}
