package worker

import "sync"

// ClaudeResetPriorityAccount is one enabled Claude credential in the routing order.
// Higher priority is preferred by CLIProxyAPI, so index 0 is the account currently
// receiving traffic first.
type ClaudeResetPriorityAccount struct {
	Name        string  `json:"name"`
	AuthIndex   string  `json:"auth_index"`
	Priority    int     `json:"priority"`
	CycleEndMS  int64   `json:"cycle_end_ms"`
	UsedPercent float64 `json:"used_percent"`
}

// ClaudeResetPriorityStatus is the last observed state of the reset-priority worker.
// It exists so the panel can show what routing is doing without reading logs.
type ClaudeResetPriorityStatus struct {
	Enabled         bool                         `json:"enabled"`
	LastPollAtMS    int64                        `json:"last_poll_at_ms"`
	LastAppliedAtMS int64                        `json:"last_applied_at_ms"`
	LastError       string                       `json:"last_error,omitempty"`
	Order           []ClaudeResetPriorityAccount `json:"order"`
}

var claudeResetPriorityState struct {
	mu     sync.RWMutex
	status ClaudeResetPriorityStatus
}

// CurrentClaudeResetPriorityStatus returns a copy of the latest status.
func CurrentClaudeResetPriorityStatus() ClaudeResetPriorityStatus {
	claudeResetPriorityState.mu.RLock()
	defer claudeResetPriorityState.mu.RUnlock()
	status := claudeResetPriorityState.status
	status.Order = append([]ClaudeResetPriorityAccount(nil), status.Order...)
	return status
}

func updateClaudeResetPriorityStatus(mutate func(*ClaudeResetPriorityStatus)) {
	claudeResetPriorityState.mu.Lock()
	defer claudeResetPriorityState.mu.Unlock()
	mutate(&claudeResetPriorityState.status)
}
