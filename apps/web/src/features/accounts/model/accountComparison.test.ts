import { describe, expect, it } from 'vitest';
import type { AccountQuotaSnapshotWindow } from '@/services/api/usageService';
import { comparisonRatio, remainingTokenEstimate, selectWeeklyQuota } from './accountComparison';
const now = Date.UTC(2026, 8, 27);
const quota: AccountQuotaSnapshotWindow = {
  provider_window_id: 'seven-day',
  model_scope_kind: 'all',
  window_kind: 'weekly',
  window_mode: 'fixed',
  source: 'api_query',
  boundary_accuracy: 'derived',
  observed_at_ms: now,
  cycle_end_ms: now + 86400_000,
  duration_seconds: 604800,
  used_percent: 40,
  stale: false,
  current_cycle: {
    id: 1,
    activation_id: 1,
    state: 'active',
    actual_start_ms: now - 6 * 86400_000,
    boundary_accuracy: 'derived',
    forecast_eligible: true,
  },
};
describe('account value comparison', () => {
  it('estimates remaining tokens only with evidence and exclusive quota confirmation', () => {
    expect(remainingTokenEstimate(quota, 20_000_000, true, now)).toBe(30_000_000);
    expect(remainingTokenEstimate(quota, 20_000_000, false, now)).toBeNull();
    expect(remainingTokenEstimate({ ...quota, stale: true }, 20, true, now)).toBeNull();
    expect(remainingTokenEstimate({ ...quota, used_percent: 1 }, 20, true, now)).toBeNull();
    expect(
      remainingTokenEstimate({ ...quota, current_cycle: undefined }, 20, true, now)
    ).toBeNull();
    expect(remainingTokenEstimate(quota, 20, true, now + 16 * 60_000)).toBeNull();
    expect(remainingTokenEstimate({ ...quota, used_percent: 100 }, 20, true, now)).toBe(0);
  });
  it('does not substitute session or model quotas for a weekly allowance', () => {
    expect(
      selectWeeklyQuota(
        [{ ...quota, provider_window_id: 'five-hour', duration_seconds: 18000 }],
        now
      )
    ).toBeUndefined();
    expect(selectWeeklyQuota([{ ...quota, model_scope_kind: 'family' }], now)).toBeUndefined();
    expect(selectWeeklyQuota([quota], now)).toEqual(quota);
    expect(selectWeeklyQuota([quota], now + 2 * 86400_000)).toBeUndefined();
  });
  it('does not divide by zero or treat missing account data as zero usage', () => {
    expect(comparisonRatio(20, 10)).toBe(2);
    expect(comparisonRatio(0, 10)).toBe(0);
    expect(comparisonRatio(20, 0)).toBeNull();
    expect(comparisonRatio(undefined, 10)).toBeNull();
  });
});
