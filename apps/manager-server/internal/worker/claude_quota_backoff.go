package worker

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type claudeQuotaRetry struct {
	Until    time.Time `json:"until"`
	Failures int       `json:"failures"`
}

type claudeQuotaRateLimitError struct{ Until time.Time }

func (e *claudeQuotaRateLimitError) Error() string {
	return "provider HTTP 429; quota refresh deferred until " + e.Until.UTC().Format(time.RFC3339)
}

func claudeQuotaRetryHeader(headers map[string]json.RawMessage) string {
	for key, raw := range headers {
		if !strings.EqualFold(key, "Retry-After") {
			continue
		}
		var values []string
		if json.Unmarshal(raw, &values) == nil && len(values) > 0 {
			return values[0]
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
	}
	return ""
}

func claudeQuotaRetryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= 86400*365 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline
	}
	return now.Add(10 * time.Minute)
}

func (w *ClaudeQuotaWorker) deferAccount(index string, deadline time.Time) time.Time {
	previous := w.retries[index]
	failures := min(max(previous.Failures, 0)+1, 4)
	delay := min(10*time.Minute*time.Duration(1<<(failures-1)), time.Hour)
	if fallback := w.now().Add(delay); fallback.After(deadline) {
		deadline = fallback
	}
	w.retries[index] = claudeQuotaRetry{Until: deadline, Failures: failures}
	return deadline
}

func (w *ClaudeQuotaWorker) loadRetries(dataDir string) {
	w.retries = make(map[string]claudeQuotaRetry)
	if dataDir == "" {
		return
	}
	w.retryPath = filepath.Join(dataDir, "claude-quota-backoff.json")
	raw, err := os.ReadFile(w.retryPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || json.Unmarshal(raw, &w.retries) != nil {
		log.Print("[claude-quota] could not load quota retry state")
		w.retries = make(map[string]claudeQuotaRetry)
	}
	if w.retries == nil {
		w.retries = make(map[string]claudeQuotaRetry)
	}
}

func (w *ClaudeQuotaWorker) saveRetries() {
	if w.retryPath == "" {
		return
	}
	raw, err := json.Marshal(w.retries)
	if err == nil {
		err = os.WriteFile(w.retryPath+".tmp", raw, 0600)
	}
	if err == nil {
		err = os.Rename(w.retryPath+".tmp", w.retryPath)
	}
	if err != nil {
		log.Print("[claude-quota] could not save quota retry state")
	}
}
