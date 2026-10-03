import { describe, expect, it } from 'vitest';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import type { AccountQuotaWindowDefinition } from '@/features/accounts/model/accountQuotaWindowDefinitions';
import {
  buildOverviewCredential,
  buildOverviewProviders,
  formatPlanType,
  formatResetParts,
  labelForSnapshotWindowId,
  maskCredentialName,
  resolveHeadlineLabel,
  selectDisplayWindows,
  sortCredentials,
  toneForRemaining,
} from './overviewModel';

const NOW = Date.UTC(2026, 8, 10, 12, 0, 0);
const HOUR = 3_600_000;
const DAY = 24 * HOUR;

const definition = (
  overrides: Partial<AccountQuotaWindowDefinition> & { providerWindowId: string }
): AccountQuotaWindowDefinition =>
  ({
    key: overrides.providerWindowId,
    provider: 'claude',
    label: '',
    kind: 'weekly',
    windowMode: 'fixed',
    modelScope: { kind: 'all', complete: true },
    observationSource: 'api_query',
    observedAtMs: NOW,
    quotaProgressObservedAtMs: NOW,
    boundaryAccuracy: 'exact',
    cycleStartMs: NOW - DAY,
    cycleEndMs: NOW + DAY,
    durationSeconds: 7 * 86_400,
    remainingPercent: null,
    usedPercent: null,
    stale: false,
    display: {
      key: overrides.providerWindowId,
      label: '',
      kind: overrides.kind ?? 'weekly',
      remainingPercent: null,
      usedPercent: null,
      resetLabel: '-',
      resetAccuracy: 'exact',
      limitWindowSeconds: null,
      resetAtMs: null,
      fromMs: null,
      toMs: null,
      modelScope: overrides.modelScope ?? { kind: 'all', complete: true },
      windowMode: 'fixed',
    },
    ...overrides,
  }) as AccountQuotaWindowDefinition;

const row = (overrides: Partial<AccountRow> & { selectionKey: string }): AccountRow =>
  ({
    key: overrides.selectionKey,
    fileName: `${overrides.selectionKey}.json`,
    accountLabel: overrides.selectionKey,
    provider: 'claude',
    planType: 'max',
    disabled: false,
    runtimeOnly: false,
    statusMessage: '',
    authIndex: overrides.selectionKey,
    projectId: '',
    priority: null,
    createdAtMs: null,
    updatedAtMs: null,
    subscriptionUntilMs: null,
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    quota: { status: 'unknown', remainingPercent: null, usedPercent: null, resetLabel: '-', resetAtMs: null, resetAccuracy: 'unknown', planType: null, source: 'none' },
    usage: {} as AccountRow['usage'],
    inspection: null,
    raw: { name: `${overrides.selectionKey}.json` },
    ...overrides,
  }) as AccountRow;

describe('selectDisplayWindows', () => {
  it('puts the model-family weekly window first, then account windows shortest to longest', () => {
    const windows = selectDisplayWindows(
      [
        definition({ providerWindowId: 'seven-day', label: '7-day limit', usedPercent: 25 }),
        definition({ providerWindowId: 'five-hour', label: '5-hour limit', kind: 'five_hour', durationSeconds: 18_000, usedPercent: 0 }),
        definition({ providerWindowId: 'seven-day-opus', label: '7-day Opus', usedPercent: 42 }),
      ],
      NOW
    );
    expect(windows.map((w) => w.id)).toEqual(['seven-day-opus', 'five-hour', 'seven-day']);
    expect(windows[0].modelScoped).toBe(true);
    expect(windows[0].remainingPercent).toBe(58);
  });

  it('leads with the weekly limit for codex even when a model-scoped 5-hour window exists', () => {
    const windows = selectDisplayWindows(
      [
        definition({ providerWindowId: 'codex-review-5h', label: 'Code review 5-hour limit', kind: 'five_hour', durationSeconds: 18_000, usedPercent: 40, modelScope: { kind: 'feature', key: 'code-review', complete: true } }),
        definition({ providerWindowId: 'primary', label: '5-hour limit', kind: 'five_hour', durationSeconds: 18_000, usedPercent: 60 }),
        definition({ providerWindowId: 'secondary', label: 'Weekly limit', usedPercent: 30 }),
      ],
      NOW
    );
    expect(windows.map((w) => w.id)).toEqual(['secondary', 'primary', 'codex-review-5h']);
  });

  it('leads with the longest model-scoped window and keeps group names', () => {
    const grouped = (id: string, seconds: number, group: string) =>
      definition({
        providerWindowId: id,
        label: seconds === 18_000 ? '5-hour limit' : 'Weekly limit',
        kind: seconds === 18_000 ? 'five_hour' : 'weekly',
        durationSeconds: seconds,
        usedPercent: 10,
        modelScope: { kind: 'family', key: group, complete: true },
        display: {
          key: id,
          label: '',
          kind: seconds === 18_000 ? 'five_hour' : 'weekly',
          remainingPercent: null,
          usedPercent: null,
          resetLabel: '-',
          resetAccuracy: 'exact',
          limitWindowSeconds: seconds,
          resetAtMs: null,
          fromMs: null,
          toMs: null,
          groupLabel: group,
          modelScope: { kind: 'family', key: group, complete: true },
          windowMode: 'fixed',
        },
      });
    const windows = selectDisplayWindows(
      [grouped('g1-5h', 18_000, 'Gemini models'), grouped('g2-5h', 18_000, 'Claude and GPT models'), grouped('g1-week', 604_800, 'Gemini models')],
      NOW
    );
    expect(windows[0].id).toBe('g1-week');
    expect(windows[0].label).toBe('Weekly limit · Gemini models');
    expect(windows).toHaveLength(3);
  });

  it('drops billing windows without a percentage and ignores past resets', () => {
    const windows = selectDisplayWindows(
      [
        definition({ providerWindowId: 'billing', kind: 'billing', usedPercent: null }),
        definition({ providerWindowId: 'seven-day', usedPercent: 10, cycleEndMs: NOW - 1 }),
      ],
      NOW
    );
    expect(windows).toHaveLength(1);
    expect(windows[0].resetAtMs).toBeNull();
  });

  it('prefers a fresh duplicate over a stale one', () => {
    const windows = selectDisplayWindows(
      [
        definition({ providerWindowId: 'seven-day', usedPercent: 90, stale: true }),
        definition({ providerWindowId: 'seven-day', usedPercent: 10, stale: false }),
      ],
      NOW
    );
    expect(windows).toHaveLength(1);
    expect(windows[0].remainingPercent).toBe(90);
  });
});

describe('sortCredentials / buildOverviewProviders', () => {
  const make = (key: string, used: number | null, resetInDays: number | null, disabled = false) =>
    buildOverviewCredential(
      row({ selectionKey: key, disabled }),
      resetInDays === null
        ? []
        : [definition({ providerWindowId: 'seven-day', usedPercent: used, cycleEndMs: NOW + resetInDays * DAY })],
      NOW
    );

  it('orders by earliest reset among usable credentials, then exhausted, then unknown, then disabled', () => {
    const sorted = sortCredentials([
      make('late', 20, 5),
      make('disabled', 0, 1, true),
      make('nodata', null, null),
      make('exhausted', 100, 2),
      make('soon', 40, 1),
    ]);
    expect(sorted.map((c) => c.key)).toEqual(['soon', 'late', 'exhausted', 'nodata', 'disabled']);
  });

  it('aggregates remaining percent against the enabled capacity', () => {
    const [claude] = buildOverviewProviders([make('a', 40, 1), make('b', 10, 3), make('off', 0, 1, true)]);
    expect(claude.provider).toBe('claude');
    expect(claude.remainingPercent).toBe(150);
    expect(claude.capacityPercent).toBe(200);
    expect(claude.nextResetAtMs).toBe(NOW + DAY);
    expect(claude.segments.map((s) => s.remainingPercent)).toEqual([60, 90]);
  });
});

describe('formatting helpers', () => {
  it('masks the account part of a credential file name', () => {
    expect(maskCredentialName('claude-team@example.dev.json')).toBe('claude-t•••@e•••.dev.json');
    expect(maskCredentialName('codex-5552f23c-nathan@gmail.com-pro.json')).toBe('codex-5•••@g•••.com-pro.json');
    expect(maskCredentialName('kimi-1784357897531.json')).toBe('kimi-1784357897531.json');
  });

  it('formats relative and absolute reset parts', () => {
    expect(formatResetParts(NOW + 3 * HOUR, NOW)?.relative).toBe('in 3 hours');
    expect(formatResetParts(NOW + 2 * DAY, NOW)?.relative).toBe('in 2 days');
    expect(formatResetParts(NOW + 10 * 60_000, NOW)?.relative).toBe('in 10 min');
    expect(formatResetParts(null, NOW)).toBeNull();
  });

  it('names the provider card by consensus, not by one credential', () => {
    const w = (label: string, durationSeconds: number | null) =>
      ({ id: label, label, remainingPercent: 50, resetAtMs: null, durationSeconds, modelScoped: false, stale: false });
    expect(resolveHeadlineLabel([w('7-day Opus', 604_800), w('7-day Opus', 604_800)])).toBe('7-day Opus');
    expect(resolveHeadlineLabel([w('7-day Opus', 604_800), w('7-day limit', 604_800)])).toBe('7-day limit');
    expect(resolveHeadlineLabel([w('Weekly', 604_800), w('Monthly', 2_592_000)])).toBe('Longest limit');
    expect(resolveHeadlineLabel([])).toBe('Quota');
  });

  it('formats plan types for display', () => {
    expect(formatPlanType('plan_max')).toBe('Max');
    expect(formatPlanType('PLAN_PRO')).toBe('Pro');
    expect(formatPlanType('team')).toBe('Team');
    expect(formatPlanType('')).toBeNull();
    expect(formatPlanType(null)).toBeNull();
  });

  it('maps remaining percent to a tone', () => {
    expect(toneForRemaining(null)).toBe('none');
    expect(toneForRemaining(75)).toBe('good');
    expect(toneForRemaining(30)).toBe('warn');
    expect(toneForRemaining(5)).toBe('bad');
  });

  it('labels known snapshot windows and humanizes unknown ids', () => {
    expect(labelForSnapshotWindowId('seven-day-opus')).toBe('7-day Opus');
    expect(labelForSnapshotWindowId('primary_window')).toBe('Primary window');
  });
});
