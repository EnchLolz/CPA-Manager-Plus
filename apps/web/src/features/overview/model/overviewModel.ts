/**
 * Overview model (personal fork).
 *
 * Pure functions that turn account rows plus merged quota window definitions
 * into the compact per-provider / per-credential view. No React, no i18n, so it
 * stays trivially testable and independent from upstream page code.
 */
import type { AccountRow } from '@/features/accounts/model/accountRows';
import type { AccountQuotaWindowDefinition } from '@/features/accounts/model/accountQuotaWindowDefinitions';
import { getAccountQuotaSemanticGroup } from '@/features/accounts/model/accountQuotaDisplayWindows';

export type OverviewTone = 'good' | 'warn' | 'bad' | 'none';

export interface OverviewWindow {
  id: string;
  label: string;
  remainingPercent: number | null;
  resetAtMs: number | null;
  durationSeconds: number | null;
  modelScoped: boolean;
  stale: boolean;
}

export interface OverviewCredential {
  key: string;
  fileName: string;
  accountLabel: string;
  provider: string;
  planType: string | null;
  disabled: boolean;
  priority: number | null;
  windows: OverviewWindow[];
  headline: OverviewWindow | null;
  nextResetAtMs: number | null;
  raw: AccountRow;
}

export interface OverviewSegment {
  key: string;
  remainingPercent: number | null;
}

export interface OverviewProvider {
  provider: string;
  credentials: OverviewCredential[];
  headlineLabel: string;
  remainingPercent: number | null;
  capacityPercent: number;
  nextResetAtMs: number | null;
  segments: OverviewSegment[];
}

export const PROVIDER_ORDER = ['claude', 'codex', 'antigravity', 'xai', 'kimi', 'gemini', 'devin', 'meta'];

export const MAX_WINDOWS_PER_CREDENTIAL = 3;

const SNAPSHOT_WINDOW_LABELS: Record<string, string> = {
  'five-hour': '5-hour limit',
  'seven-day': '7-day limit',
  'seven-day-opus': '7-day Opus',
  'seven-day-sonnet': '7-day Sonnet',
  'seven-day-oauth-apps': '7-day OAuth apps',
};

/** Label for a window that only exists in the persisted snapshot store. */
export const labelForSnapshotWindowId = (providerWindowId: string): string =>
  SNAPSHOT_WINDOW_LABELS[providerWindowId] ??
  providerWindowId.replace(/[-_]+/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

const CLAUDE_MODEL_WINDOW = /^seven-day-(opus|sonnet)$/;

const isModelWindow = (definition: AccountQuotaWindowDefinition): boolean =>
  getAccountQuotaSemanticGroup(definition.display) === 'model' ||
  CLAUDE_MODEL_WINDOW.test(definition.providerWindowId);

const isDisplayable = (definition: AccountQuotaWindowDefinition): boolean => {
  const group = getAccountQuotaSemanticGroup(definition.display);
  if (group === 'standard' || group === 'model') return true;
  // xAI exposes a billing-period window; show it when it carries a percentage.
  return definition.kind === 'billing' && resolveRemaining(definition) !== null;
};

const resolveRemaining = (definition: AccountQuotaWindowDefinition): number | null => {
  if (typeof definition.remainingPercent === 'number' && Number.isFinite(definition.remainingPercent)) {
    return clamp(definition.remainingPercent);
  }
  if (typeof definition.usedPercent === 'number' && Number.isFinite(definition.usedPercent)) {
    return clamp(100 - definition.usedPercent);
  }
  return null;
};

const clamp = (value: number) => Math.max(0, Math.min(100, value));

const resolveResetAt = (definition: AccountQuotaWindowDefinition, nowMs: number): number | null => {
  const candidate = definition.cycleEndMs ?? definition.display.resetAtMs ?? null;
  return typeof candidate === 'number' && Number.isFinite(candidate) && candidate > nowMs
    ? candidate
    : null;
};

const toOverviewWindow = (
  definition: AccountQuotaWindowDefinition,
  nowMs: number
): OverviewWindow => ({
  id: definition.providerWindowId,
  label: definition.label || labelForSnapshotWindowId(definition.providerWindowId),
  remainingPercent: resolveRemaining(definition),
  resetAtMs: resolveResetAt(definition, nowMs),
  durationSeconds: definition.durationSeconds ?? definition.display.limitWindowSeconds ?? null,
  modelScoped: isModelWindow(definition),
  stale: definition.stale,
});

const byDuration = (a: OverviewWindow, b: OverviewWindow) =>
  (a.durationSeconds ?? Number.MAX_SAFE_INTEGER) - (b.durationSeconds ?? Number.MAX_SAFE_INTEGER);

/**
 * Pick the windows worth showing on one row: the model-family weekly window
 * first (it is the one that actually gates heavy use), then account-wide
 * windows shortest to longest. Duplicated ids collapse to the freshest entry.
 */
export const selectDisplayWindows = (
  definitions: AccountQuotaWindowDefinition[],
  nowMs = Date.now()
): OverviewWindow[] => {
  const byId = new Map<string, OverviewWindow>();
  for (const definition of definitions) {
    if (!isDisplayable(definition)) continue;
    const window = toOverviewWindow(definition, nowMs);
    const existing = byId.get(window.id);
    if (!existing || (existing.stale && !window.stale) || (existing.remainingPercent === null && window.remainingPercent !== null)) {
      byId.set(window.id, window);
    }
  }
  const windows = Array.from(byId.values());
  const model = windows.filter((w) => w.modelScoped).sort(byDuration);
  const standard = windows.filter((w) => !w.modelScoped).sort(byDuration);
  return [...model.slice(0, 1), ...standard, ...model.slice(1)].slice(0, MAX_WINDOWS_PER_CREDENTIAL);
};

/** The window that best represents "how much of this subscription is left". */
export const selectHeadlineWindow = (windows: OverviewWindow[]): OverviewWindow | null => {
  const model = windows.find((w) => w.modelScoped);
  if (model) return model;
  const standard = windows.filter((w) => !w.modelScoped).sort(byDuration);
  return standard.length ? standard[standard.length - 1] : null;
};

export const buildOverviewCredential = (
  row: AccountRow,
  definitions: AccountQuotaWindowDefinition[],
  nowMs = Date.now()
): OverviewCredential => {
  const windows = selectDisplayWindows(definitions, nowMs);
  const headline = selectHeadlineWindow(windows);
  const resets = windows.map((w) => w.resetAtMs).filter((v): v is number => v !== null);
  return {
    key: row.selectionKey,
    fileName: row.fileName,
    accountLabel: row.accountLabel,
    provider: row.provider,
    planType: row.planType,
    disabled: row.disabled,
    priority: row.priority,
    windows,
    headline,
    nextResetAtMs: resets.length ? Math.min(...resets) : null,
    raw: row,
  };
};

/**
 * Routing-aligned ordering: enabled credentials with remaining quota first,
 * earliest headline reset first (the reset-priority worker uses the same
 * rule), then exhausted ones, then credentials without data, then disabled.
 */
export const sortCredentials = (credentials: OverviewCredential[]): OverviewCredential[] => {
  const rank = (c: OverviewCredential): number => {
    if (c.disabled) return 3;
    if (!c.headline || c.headline.remainingPercent === null) return 2;
    if (c.headline.remainingPercent <= 0) return 1;
    return 0;
  };
  return [...credentials].sort((a, b) => {
    const r = rank(a) - rank(b);
    if (r !== 0) return r;
    const ar = a.headline?.resetAtMs ?? Number.MAX_SAFE_INTEGER;
    const br = b.headline?.resetAtMs ?? Number.MAX_SAFE_INTEGER;
    if (ar !== br) return ar - br;
    return a.fileName.localeCompare(b.fileName);
  });
};

export const buildOverviewProviders = (credentials: OverviewCredential[]): OverviewProvider[] => {
  const groups = new Map<string, OverviewCredential[]>();
  for (const credential of credentials) {
    const list = groups.get(credential.provider) ?? [];
    list.push(credential);
    groups.set(credential.provider, list);
  }
  const providers = Array.from(groups.entries()).map(([provider, list]) => {
    const sorted = sortCredentials(list);
    const withHeadline = sorted.filter((c) => !c.disabled && c.headline);
    const measured = withHeadline.filter((c) => c.headline?.remainingPercent !== null);
    const resets = withHeadline
      .map((c) => c.headline?.resetAtMs ?? null)
      .filter((v): v is number => v !== null);
    return {
      provider,
      credentials: sorted,
      headlineLabel: withHeadline[0]?.headline?.label ?? 'Quota',
      remainingPercent: measured.length
        ? Math.round(measured.reduce((sum, c) => sum + (c.headline?.remainingPercent ?? 0), 0))
        : null,
      capacityPercent: withHeadline.length * 100,
      nextResetAtMs: resets.length ? Math.min(...resets) : null,
      segments: sorted
        .filter((c) => !c.disabled)
        .map((c) => ({ key: c.key, remainingPercent: c.headline?.remainingPercent ?? null })),
    };
  });
  const order = (p: string) => {
    const index = PROVIDER_ORDER.indexOf(p);
    return index === -1 ? PROVIDER_ORDER.length : index;
  };
  return providers.sort((a, b) => order(a.provider) - order(b.provider) || a.provider.localeCompare(b.provider));
};

export const toneForRemaining = (remainingPercent: number | null): OverviewTone => {
  if (remainingPercent === null) return 'none';
  if (remainingPercent >= 50) return 'good';
  if (remainingPercent >= 20) return 'warn';
  return 'bad';
};

/** `claude-team@example.dev.json` -> `claude-t•••@e•••.dev.json`. */
export const maskCredentialName = (name: string): string =>
  name.replace(/([^@\s/]+)@([^.\s/]+)((?:\.[^.\s/]+)*)/g, (_m, local: string, domain: string, rest: string) => {
    const localPrefix = local.includes('-') ? local.slice(0, local.indexOf('-') + 2) : local.slice(0, 1);
    return `${localPrefix}•••@${domain.slice(0, 1)}•••${rest}`;
  });

export interface ResetParts {
  relative: string;
  absolute: string;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

export const formatResetParts = (
  resetAtMs: number | null,
  nowMs = Date.now(),
  locale = 'en-US'
): ResetParts | null => {
  if (resetAtMs === null || !Number.isFinite(resetAtMs)) return null;
  const delta = resetAtMs - nowMs;
  let relative: string;
  if (delta <= 0) relative = 'now';
  else if (delta < HOUR) relative = `in ${Math.max(1, Math.round(delta / MINUTE))} min`;
  else if (delta < DAY) {
    const hours = Math.round(delta / HOUR);
    relative = `in ${hours} hour${hours === 1 ? '' : 's'}`;
  } else {
    const days = Math.round(delta / DAY);
    relative = `in ${days} day${days === 1 ? '' : 's'}`;
  }
  const absolute = new Intl.DateTimeFormat(locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(resetAtMs));
  return { relative, absolute };
};

export const formatPercentValue = (value: number | null): string =>
  value === null ? '--' : `${Math.round(value)}%`;
