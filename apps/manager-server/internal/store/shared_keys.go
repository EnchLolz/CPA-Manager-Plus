package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SharedKey never contains the plaintext credential. InFlight is durable: a
// process crash must not silently allow another request with unknown usage.
type SharedKey struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Models         []string `json:"models"`
	CreatedAt      int64    `json:"created_at"`
	ExpiresAt      int64    `json:"expires_at"`
	MaxTokens      int64    `json:"max_tokens"`
	MaxCostMicros  int64    `json:"max_cost_micros"`
	UsedTokens     int64    `json:"used_tokens"`
	UsedCostMicros int64    `json:"used_cost_micros"`
	Revoked        bool     `json:"revoked"`
	InFlight       bool     `json:"in_flight"`
	Blocked        bool     `json:"blocked"`
}

const sharedKeyColumns = `id, name, models, created_at, expires_at, max_tokens, max_cost_micros, used_tokens, used_cost_micros, revoked, in_flight, blocked`

func scanSharedKey(row interface{ Scan(...any) error }) (SharedKey, error) {
	var key SharedKey
	var models string
	err := row.Scan(&key.ID, &key.Name, &models, &key.CreatedAt, &key.ExpiresAt, &key.MaxTokens, &key.MaxCostMicros, &key.UsedTokens, &key.UsedCostMicros, &key.Revoked, &key.InFlight, &key.Blocked)
	if err == nil {
		err = json.Unmarshal([]byte(models), &key.Models)
	}
	return key, err
}

func (s *Store) CreateSharedKey(ctx context.Context, key SharedKey, hash string) error {
	models, err := json.Marshal(key.Models)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into shared_keys (id, key_hash, name, models, created_at, expires_at, max_tokens, max_cost_micros) values (?, ?, ?, ?, ?, ?, ?, ?)`, key.ID, hash, key.Name, string(models), key.CreatedAt, key.ExpiresAt, key.MaxTokens, key.MaxCostMicros)
	return err
}

func (s *Store) ListSharedKeys(ctx context.Context) ([]SharedKey, error) {
	rows, err := s.db.QueryContext(ctx, `select `+sharedKeyColumns+` from shared_keys order by created_at desc, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []SharedKey{}
	for rows.Next() {
		key, err := scanSharedKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) FindSharedKey(ctx context.Context, hash string) (SharedKey, error) {
	return scanSharedKey(s.db.QueryRowContext(ctx, `select `+sharedKeyColumns+` from shared_keys where key_hash = ?`, hash))
}

// AcquireSharedKey admits a request while the key is live and within its allowance.
// Requests may run concurrently; in_flight only marks recent activity.
func (s *Store) AcquireSharedKey(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `update shared_keys set in_flight = 1 where id = ? and revoked = 0 and expires_at > ? and (max_tokens = 0 or used_tokens < max_tokens) and (max_cost_micros = 0 or used_cost_micros < max_cost_micros)`, id, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// FinishSharedRequest charges the usage that was observed. Uncertain usage is
// logged by the caller but no longer pauses the key.
func (s *Store) FinishSharedRequest(ctx context.Context, id string, tokens, cost int64) error {
	_, err := s.db.ExecContext(ctx, `update shared_keys set used_tokens = used_tokens + ?, used_cost_micros = used_cost_micros + ?, in_flight = 0, blocked = 0 where id = ?`, tokens, cost, id)
	return err
}

func (s *Store) RevokeSharedKey(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `update shared_keys set revoked = 1 where id = ?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func IsSharedKeyNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
