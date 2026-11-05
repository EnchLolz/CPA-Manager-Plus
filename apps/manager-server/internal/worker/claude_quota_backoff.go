package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// claudeQuotaRetry is a credential's latest quota failure. Until is set when the
// next attempt must wait; Error and Status stay visible in the routing status
// until an observation succeeds.
type claudeQuotaRetry struct {
	Until    time.Time `json:"until"`
	Failures int       `json:"failures"`
	Error    string    `json:"error,omitempty"`
	Status   int       `json:"status,omitempty"`
}

// claudeQuotaDeferredError is a failure that retrying soon cannot fix: a rate
// limit (429) or a rejected sign-in (401/403). Repeated rejected sign-ins also
// draw 429s from the usage endpoint, so both back off the same way.
type claudeQuotaDeferredError struct {
	Status int
	Until  time.Time
}

func (e *claudeQuotaDeferredError) Error() string {
	return fmt.Sprintf("provider HTTP %d; quota refresh deferred until %s", e.Status, e.Until.UTC().Format(time.RFC3339))
}

// claudeQuotaStatusError is any other HTTP failure, retried on the next poll.
type claudeQuotaStatusError struct {
	Status int
	Source string
}

func (e *claudeQuotaStatusError) Error() string {
	return fmt.Sprintf("%s HTTP %d", e.Source, e.Status)
}

// recordQuotaFailure keeps the failure for the routing status and defers the
// next attempt when the provider asked for it or rejected the credential.
func (w *ClaudeQuotaWorker) recordQuotaFailure(index string, err error) {
	var deferred *claudeQuotaDeferredError
	var failed *claudeQuotaStatusError
	retry := claudeQuotaRetry{Failures: w.retries[index].Failures}
	switch {
	case errors.As(err, &deferred):
		// The message names the final deadline, so it is read after deferring.
		deferred.Until = w.deferAccount(index, deferred.Until)
		retry = w.retries[index]
		retry.Status = deferred.Status
	case errors.As(err, &failed):
		retry.Status = failed.Status
	}
	retry.Error = err.Error()
	w.retries[index] = retry
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
