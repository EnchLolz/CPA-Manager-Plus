import { describe, expect, it } from 'vitest';
import { overviewQuotaRefreshAtMs, quotaRetryAtMs } from './refreshTiming';

const NOW = Date.UTC(2026, 9, 14);

describe('quota refresh timing', () => {
  it('honors both Retry-After formats and mixed header casing', () => {
    expect(quotaRetryAtMs({ 'Retry-After': ['72'] }, NOW)).toBe(NOW + 72_000);
    expect(quotaRetryAtMs({ 'retry-after': [new Date(NOW + 180_000).toUTCString()] }, NOW)).toBe(NOW + 180_000);
  });

  it.each<Record<string, string[]>>([{}, { 'Retry-After': ['invalid'] }, { 'Retry-After': ['-1'] }])(
    'backs off when the provider omits a valid retry delay', (headers) => {
      expect(quotaRetryAtMs(headers, NOW)).toBe(NOW + 120_000);
    }
  );

  it('keeps the retry deadline through storage hydration without extending it', () => {
    const state = JSON.parse(JSON.stringify({ failedAtMs: NOW, errorStatus: 429, retryAtMs: NOW + 72_000 }));
    expect(overviewQuotaRefreshAtMs('claude', state)).toBe(NOW + 72_000);
    expect(overviewQuotaRefreshAtMs('claude', state)).toBeLessThan(NOW + 73_000);
  });

  it('paces successful Claude refreshes and backs off older persisted 429s', () => {
    expect(overviewQuotaRefreshAtMs('claude', { fetchedAtMs: NOW })).toBe(NOW + 60_000);
    expect(overviewQuotaRefreshAtMs('claude', { errorStatus: 429, failedAtMs: NOW })).toBe(NOW + 120_000);
    expect(overviewQuotaRefreshAtMs('claude', { errorStatus: 401, failedAtMs: NOW })).toBe(0);
    expect(overviewQuotaRefreshAtMs('codex', { fetchedAtMs: NOW })).toBe(0);
  });
});
