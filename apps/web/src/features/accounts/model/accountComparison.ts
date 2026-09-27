import type { AccountQuotaSnapshotWindow } from '@/services/api/usageService';

export const selectWeeklyQuota = (windows: AccountQuotaSnapshotWindow[], now: number) =>
  windows.find(
    (window) =>
      ['seven-day', 'weekly'].includes(window.provider_window_id) &&
      window.model_scope_kind === 'all' &&
      window.availability !== 'inactive' &&
      window.cycle_end_ms != null &&
      window.cycle_end_ms > now &&
      window.duration_seconds === 604800
  );

export function remainingTokenEstimate(
  quota: AccountQuotaSnapshotWindow | undefined,
  tokens: number | undefined,
  exclusiveUsage: boolean,
  now: number
): number | null {
  if (
    !exclusiveUsage ||
    !quota ||
    quota.stale ||
    !quota.current_cycle?.forecast_eligible ||
    quota.used_percent == null ||
    quota.used_percent < 5 ||
    quota.used_percent > 100 ||
    quota.cycle_end_ms == null ||
    quota.cycle_end_ms <= now ||
    now - quota.observed_at_ms > 15 * 60_000 ||
    tokens == null ||
    !Number.isFinite(tokens) ||
    tokens <= 0
  )
    return null;
  return (tokens * (100 - quota.used_percent)) / quota.used_percent;
}

export function comparisonRatio(
  value: number | undefined,
  baseline: number | undefined
): number | null {
  return value != null &&
    baseline != null &&
    Number.isFinite(value) &&
    Number.isFinite(baseline) &&
    value >= 0 &&
    baseline > 0
    ? value / baseline
    : null;
}
