import axios from 'axios';
import { isDemoMode } from '@/features/demo/demoMode';

export interface RoutingAccount {
  name: string;
  auth_index: string;
  priority: number;
  tier?: string;
  cycle_end_ms?: number;
  used_percent?: number;
  pinned?: boolean;
  /** Ranked without a current quota observation, after accounts with quota left. */
  quota_unknown?: boolean;
  /** Latest failed quota read, kept until one succeeds; 401 means the login was rejected. */
  quota_error?: string;
  quota_error_status?: number;
}

/** A manual "use this credential first" override; until_ms ends it automatically. */
export interface RoutingPin {
  auth_index: string;
  name: string;
  pinned_at_ms: number;
  until_ms?: number;
}

export interface RoutingStatus {
  enabled: boolean;
  strategy: string;
  last_poll_at_ms: number;
  last_applied_at_ms: number;
  /** The automatic order is paused. */
  last_error?: string;
  /** The order is applied but degraded, e.g. some accounts have no current quota. */
  warning?: string;
  order: RoutingAccount[];
  pin?: RoutingPin | null;
}

export type RoutingStatusByProvider = Record<string, RoutingStatus>;

// Go encodes a nil slice as null, including before a worker has an order.
type RoutingResponse = {
  providers?: Record<string, Omit<RoutingStatus, 'order'> & { order?: RoutingAccount[] | null }> | null;
};

const normalizeRouting = (data: RoutingResponse): RoutingStatusByProvider =>
  Object.fromEntries(
    Object.entries(data.providers ?? {}).map(([provider, status]) => [
      provider,
      { ...status, order: status.order ?? [] },
    ])
  );

const demoPins: Record<string, string> = {};

const applyDemoPin = (status: RoutingStatus, pinned: string | undefined): RoutingStatus => {
  const target = status.order.find((account) => account.auth_index === pinned);
  if (!target) return status;
  const order = [target, ...status.order.filter((account) => account !== target)].map((account, index, all) => ({
    ...account,
    priority: (all.length - index) * 10,
    pinned: account === target,
  }));
  return {
    ...status,
    order,
    pin: { auth_index: target.auth_index, name: target.name, pinned_at_ms: Date.now(), until_ms: target.cycle_end_ms },
  };
};

const buildDemoRouting = (): RoutingStatusByProvider => {
  const now = Date.now();
  const routing: RoutingStatusByProvider = {
    claude: {
      enabled: true,
      strategy: 'earliest weekly reset first',
      last_poll_at_ms: now - 3 * 60_000,
      last_applied_at_ms: now - 3 * 60_000,
      order: [
        { name: 'claude-research-02.json', auth_index: '2', priority: 30, cycle_end_ms: now + 2 * 86_400_000, used_percent: 74 },
        { name: 'claude-extra-usage-03.json', auth_index: '3', priority: 20, cycle_end_ms: now + 3 * 86_400_000, used_percent: 18 },
        { name: 'claude-team-01.json', auth_index: '1', priority: 10, cycle_end_ms: now + 4 * 86_400_000, used_percent: 31 },
      ],
    },
    codex: {
      enabled: true,
      strategy: 'tier plus > pro > promax',
      last_poll_at_ms: now - 3 * 60_000,
      last_applied_at_ms: now - 3 * 60_000,
      order: [
        { name: 'codex-email-user.json', auth_index: '12', priority: 30, tier: 'plus' },
        { name: 'codex-pro-20x-01.json', auth_index: '11', priority: 20, tier: 'pro' },
        { name: 'codex-team-01.json', auth_index: '10', priority: 10, tier: 'team' },
      ],
    },
  };
  return Object.fromEntries(
    Object.entries(routing).map(([provider, status]) => [provider, applyDemoPin(status, demoPins[provider])])
  );
};

const authHeaders = (managementKey: string | null | undefined) =>
  managementKey ? { Authorization: `Bearer ${managementKey}` } : undefined;

/** Reads per-provider routing status from the personal manager-server endpoint. */
export const fetchRoutingStatus = async (
  managerServiceBase: string,
  managementKey: string | null | undefined,
  signal?: AbortSignal
): Promise<RoutingStatusByProvider> => {
  if (__DEMO_SITE__ && isDemoMode()) return buildDemoRouting();
  const base = managerServiceBase.replace(/\/+$/, '');
  const response = await axios.get<RoutingResponse>(
    `${base}/v0/management/routing-priority`,
    {
      timeout: 10_000,
      headers: authHeaders(managementKey),
      signal,
    }
  );
  return normalizeRouting(response.data);
};

/**
 * Pins one credential first for a provider, or clears the pin when authIndex
 * is null. Resolves with the routing status after the worker applied it.
 */
export const setRoutingPin = async (
  managerServiceBase: string,
  managementKey: string | null | undefined,
  provider: string,
  authIndex: string | null
): Promise<RoutingStatusByProvider> => {
  if (__DEMO_SITE__ && isDemoMode()) {
    if (authIndex) demoPins[provider] = authIndex;
    else delete demoPins[provider];
    return buildDemoRouting();
  }
  const url = `${managerServiceBase.replace(/\/+$/, '')}/v0/management/routing-priority/pin`;
  const config = { timeout: 60_000, headers: authHeaders(managementKey) };
  const response = authIndex
    ? await axios.put<RoutingResponse>(url, { provider, auth_index: authIndex }, config)
    : await axios.delete<RoutingResponse>(url, { ...config, params: { provider } });
  return normalizeRouting(response.data);
};
