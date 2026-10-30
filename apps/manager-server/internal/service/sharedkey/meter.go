package sharedkey

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
)

// A stream can repeat cumulative usage. Keep maxima, not the sum of snapshots.
type meter struct {
	path                    string
	pending                 []byte
	tokens                  pricing.ModelTokens
	tier                    string
	seen, complete, invalid bool
	hasInput, hasOutput     bool
}

func newMeter(path string) *meter { return &meter{path: path} }
func (m *meter) total() int64     { return m.tokens.InputTokens + m.tokens.OutputTokens }

func (m *meter) feed(data []byte) {
	if m.invalid {
		return
	}
	m.pending = append(m.pending, data...)
	for {
		index := bytes.IndexByte(m.pending, '\n')
		if index < 0 {
			break
		}
		line := bytes.TrimSpace(m.pending[:index])
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[5:])
			if bytes.Equal(payload, []byte("[DONE]")) {
				m.complete = true
			} else {
				m.consume(payload)
			}
		}
		m.pending = m.pending[index+1:]
	}
	if len(m.pending) > 2<<20 {
		m.invalid = true
		m.pending = nil
	}
}

func (m *meter) consume(data []byte) {
	var event map[string]json.RawMessage
	if json.Unmarshal(data, &event) != nil {
		m.invalid = true
		return
	}
	var kind string
	_ = json.Unmarshal(event["type"], &kind)
	if kind == "error" || kind == "response.failed" {
		m.invalid = true
	}
	if kind == "message_stop" || kind == "response.completed" || kind == "response.incomplete" {
		m.complete = true
	}
	if nested := event["response"]; len(nested) > 0 {
		m.consume(nested)
	}
	if nested := event["message"]; len(nested) > 0 {
		m.consume(nested)
	}
	var tier string
	if json.Unmarshal(event["service_tier"], &tier) == nil && tier != "" {
		m.tier = tier
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(event["usage"], &usage) != nil || usage == nil {
		return
	}
	read := func(fields ...string) (int64, bool) {
		for _, field := range fields {
			if raw, ok := usage[field]; ok {
				var n int64
				if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &n) != nil || n < 0 || n > 1e12 {
					m.invalid = true
					return 0, false
				}
				return n, true
			}
		}
		return 0, false
	}
	input, hasInput := read("input_tokens", "prompt_tokens")
	output, hasOutput := read("output_tokens", "completion_tokens")
	m.hasInput = m.hasInput || hasInput
	m.hasOutput = m.hasOutput || hasOutput
	m.seen = m.hasInput && m.hasOutput
	cacheRead, _ := read("cache_read_input_tokens")
	cacheCreate, _ := read("cache_creation_input_tokens")
	if m.path == "/messages" {
		input += cacheRead + cacheCreate
	}
	m.tokens.InputTokens = max(m.tokens.InputTokens, input)
	m.tokens.ContextThresholdTokens = m.tokens.InputTokens
	m.tokens.OutputTokens = max(m.tokens.OutputTokens, output)
	m.tokens.CacheReadTokens = max(m.tokens.CacheReadTokens, cacheRead)
	m.tokens.CacheCreationTokens = max(m.tokens.CacheCreationTokens, cacheCreate)
	for _, field := range []string{"input_tokens_details", "prompt_tokens_details"} {
		var details struct {
			Cached int64 `json:"cached_tokens"`
		}
		if raw, ok := usage[field]; ok && string(raw) != "null" {
			if json.Unmarshal(raw, &details) != nil || details.Cached < 0 || details.Cached > 1e12 {
				m.invalid = true
			} else {
				m.tokens.CachedTokens = max(m.tokens.CachedTokens, details.Cached)
			}
		}
	}
	if strings.Contains(kind, "failed") {
		m.invalid = true
	}
}
