import axios from 'axios';
import { isDemoMode } from '@/features/demo/demoMode';

export interface RoutingAccount {
  name: string;
  auth_index: string;
  priority: number;
  tier?: string;
  cycle_end_ms?: number;
  used_percent?: number;
}

export interface RoutingStatus {
  enabled: boolean;
  strategy: string;
  last_poll_at_ms: number;
  last_applied_at_ms: number;
  last_error?: string;
  order: RoutingAccount[];
}

export type RoutingStatusByProvider = Record<string, RoutingStatus>;

const buildDemoRouting = (): RoutingStatusByProvider => {
  const now = Date.now();
  return {
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
};

/** Reads per-provider routing status from the personal manager-server endpoint. */
export const fetchRoutingStatus = async (
  managerServiceBase: string,
  managementKey: string | null | undefined,
  signal?: AbortSignal
): Promise<RoutingStatusByProvider> => {
  if (__DEMO_SITE__ && isDemoMode()) return buildDemoRouting();
  const base = managerServiceBase.replace(/\/+$/, '');
  const response = await axios.get<{ providers: RoutingStatusByProvider }>(
    `${base}/v0/management/routing-priority`,
    {
      timeout: 10_000,
      headers: managementKey ? { Authorization: `Bearer ${managementKey}` } : undefined,
      signal,
    }
  );
  return response.data.providers ?? {};
};
