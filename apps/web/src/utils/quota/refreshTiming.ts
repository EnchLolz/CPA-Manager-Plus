import type { CredentialScopedQuotaState } from '@/types';

const CLAUDE_REFRESH_INTERVAL_MS = 60_000;
const RATE_LIMIT_FALLBACK_MS = 120_000;

export const quotaRetryAtMs = (
  headers: Record<string, string[]>,
  nowMs = Date.now()
): number => {
  const raw = Object.entries(headers).find(([name]) => name.toLowerCase() === 'retry-after')?.[1]?.[0];
  if (raw?.trim()) {
    const seconds = Number(raw);
    if (Number.isFinite(seconds) && seconds >= 0) return nowMs + seconds * 1_000;
    const date = Date.parse(raw);
    if (Number.isFinite(date) && date > nowMs) return date;
  }
  return nowMs + RATE_LIMIT_FALLBACK_MS;
};

export const overviewQuotaRefreshAtMs = (
  provider: string,
  state?: CredentialScopedQuotaState & { errorStatus?: number }
): number => {
  if (provider !== 'claude' || !state) return 0;
  if (state.errorStatus === 429 && state.failedAtMs) {
    return state.retryAtMs ?? state.failedAtMs + RATE_LIMIT_FALLBACK_MS;
  }
  return state.fetchedAtMs ? state.fetchedAtMs + CLAUDE_REFRESH_INTERVAL_MS : 0;
};
