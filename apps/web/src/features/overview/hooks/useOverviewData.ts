/**
 * Data hook for the Overview page (personal fork).
 *
 * Sources, in order of freshness:
 *   1. Live provider quota calls through CPA (explicit refresh only).
 *   2. The quota store (zustand, persisted) which the live calls populate.
 *   3. Persisted quota snapshots in the manager-server, written by the Claude
 *      background worker every five minutes and by codex inspection.
 *
 * Upstream model helpers are imported, never modified.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useShallow } from 'zustand/react/shallow';
import axios from 'axios';
import { authFilesApi } from '@/services/api';
import {
  accountQuotaSnapshotApi,
  type AccountQuotaSnapshotWindow,
} from '@/services/api/usageService';
import { useAuthStore, useQuotaStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { isDemoMode } from '@/features/demo/demoMode';
import type { AuthFileItem } from '@/types/authFile';
import type { CredentialScopedQuotaState } from '@/types';
import { buildAccountRows, type AccountRow } from '@/features/accounts/model/accountRows';
import { buildAccountQuotaDisplayWindows } from '@/features/accounts/model/accountQuotaDisplayWindows';
import { buildAccountQuotaWindowDefinitions } from '@/features/accounts/model/accountQuotaWindowDefinitions';
import {
  buildAccountQuotaSnapshotQueryAccounts,
  mergeAccountQuotaSnapshotWindows,
} from '@/features/accounts/model/accountQuotaSnapshots';
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  KIMI_CONFIG,
  XAI_CONFIG,
  type QuotaConfig,
  getScopedQuotaState,
} from '@/components/quota/quotaConfigs';
import { refreshQuotaWithConfig, type QuotaSetter } from '@/components/quota/quotaRefresh';
import { getCredentialScopedQuotaState } from '@/utils/quota/credentialScope';
import { overviewQuotaRefreshAtMs } from '@/utils/quota/refreshTiming';
import {
  buildProviderCredentialTaskPlan,
  runProviderCredentialTaskPlan,
} from '@/utils/quota/providerRefreshScheduler';
import {
  buildOverviewCredential,
  buildOverviewProviders,
  labelForSnapshotWindowId,
  type OverviewCredential,
  type OverviewProvider,
} from '../model/overviewModel';
import {
  fetchRoutingStatus,
  setRoutingPin,
  type RoutingStatusByProvider,
} from '../services/routingApi';

type QuotaStoreState = ReturnType<typeof useQuotaStore.getState>;
type Refresher = (file: AuthFileItem, t: TFunction) => Promise<void>;

const errorMessage = (error: unknown): string => {
  const serverMessage = axios.isAxiosError(error)
    ? (error.response?.data as { error?: unknown } | undefined)?.error
    : null;
  if (typeof serverMessage === 'string' && serverMessage) return serverMessage;
  return error instanceof Error ? error.message : String(error);
};
const makeRefresher = <TState extends CredentialScopedQuotaState, TData>(
  config: QuotaConfig<TState, TData>,
  select: (state: QuotaStoreState) => {
    states: Record<string, TState>;
    setQuota: QuotaSetter<TState>;
  }
): Refresher => {
  return async (file, t) => {
    const { states, setQuota } = select(useQuotaStore.getState());
    const currentState = getScopedQuotaState(config, states, file);
    if (overviewQuotaRefreshAtMs(config.type, currentState) > Date.now()) return;
    const { apiBase, managementKey } = useAuthStore.getState();
    await refreshQuotaWithConfig({
      config,
      file,
      setQuota,
      t,
      currentState,
      requestScope: { apiBase, managementKey },
      isCurrent: () => {
        const current = useAuthStore.getState();
        return current.apiBase === apiBase && current.managementKey === managementKey;
      },
    });
  };
};

const REFRESHERS: Record<string, Refresher> = {
  claude: makeRefresher(CLAUDE_CONFIG, (s) => ({
    states: s.claudeQuota,
    setQuota: s.setClaudeQuota,
  })),
  codex: makeRefresher(CODEX_CONFIG, (s) => ({ states: s.codexQuota, setQuota: s.setCodexQuota })),
  kimi: makeRefresher(KIMI_CONFIG, (s) => ({ states: s.kimiQuota, setQuota: s.setKimiQuota })),
  xai: makeRefresher(XAI_CONFIG, (s) => ({ states: s.xaiQuota, setQuota: s.setXaiQuota })),
  antigravity: makeRefresher(ANTIGRAVITY_CONFIG, (s) => ({
    states: s.antigravityQuota,
    setQuota: s.setAntigravityQuota,
  })),
};

export const canRefreshProvider = (provider: string): boolean => provider in REFRESHERS;

export interface OverviewData {
  nowMs: number;
  loading: boolean;
  error: string | null;
  providers: OverviewProvider[];
  credentials: OverviewCredential[];
  refreshing: ReadonlySet<string>;
  refreshingAll: boolean;
  lastLoadedAtMs: number | null;
  routing: RoutingStatusByProvider;
  /** Provider whose routing pin is being changed, if any. */
  pinningProvider: string | null;
  reload: () => Promise<void>;
  /** Pins a credential first for its provider, or clears the pin with null. */
  pinRouting: (provider: string, authIndex: string | null) => Promise<void>;
  refreshCredential: (credential: OverviewCredential) => Promise<void>;
  refreshAll: () => Promise<void>;
}

export function useOverviewData(): OverviewData {
  const { t, i18n } = useTranslation();
  const managementKey = useAuthStore((state) => state.managementKey);
  const availability = usePanelFeatureAvailability();
  const stores = useQuotaStore(
    useShallow((state) => ({
      antigravityQuota: state.antigravityQuota,
      claudeQuota: state.claudeQuota,
      codexQuota: state.codexQuota,
      devinQuota: state.devinQuota,
      kimiQuota: state.kimiQuota,
      metaQuota: state.metaQuota,
      xaiQuota: state.xaiQuota,
    }))
  );

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [snapshots, setSnapshots] = useState<Map<string, AccountQuotaSnapshotWindow[]>>(new Map());
  const [refreshing, setRefreshing] = useState<Set<string>>(new Set());
  const [refreshingAll, setRefreshingAll] = useState(false);
  const [lastLoadedAtMs, setLastLoadedAtMs] = useState<number | null>(null);
  const [routing, setRouting] = useState<RoutingStatusByProvider>({});
  const [pinningProvider, setPinningProvider] = useState<string | null>(null);
  const requestSeq = useRef(0);
  const refreshingKeys = useRef(new Set<string>());
  // One clock for the whole page so relative times stay consistent and pure.
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNowMs(Date.now()), 1_000);
    return () => window.clearInterval(id);
  }, []);

  const managerBase = availability.managerServiceAvailable ? availability.managerServiceBase : '';

  const loadSnapshots = useCallback(
    async (rows: AccountRow[]) => {
      if (!managerBase) return;
      const accounts = buildAccountQuotaSnapshotQueryAccounts(rows);
      if (accounts.length === 0) return;
      const response = await accountQuotaSnapshotApi.query(
        managerBase,
        managementKey ?? undefined,
        accounts
      );
      setSnapshots(new Map(response.items.map((item) => [item.row_key, item.windows])));
    },
    [managerBase, managementKey]
  );

  const loadRouting = useCallback(async () => {
    if (!managerBase && !(__DEMO_SITE__ && isDemoMode())) {
      setRouting({});
      return;
    }
    try {
      setRouting(await fetchRoutingStatus(managerBase, managementKey));
    } catch {
      // Upstream manager-server builds do not expose this endpoint. That is fine.
      setRouting({});
    }
  }, [managerBase, managementKey]);

  const pinRouting = useCallback(
    async (provider: string, authIndex: string | null) => {
      if (!managerBase && !(__DEMO_SITE__ && isDemoMode())) return;
      setPinningProvider(provider);
      try {
        setRouting(await setRoutingPin(managerBase, managementKey, provider, authIndex));
        setError(null);
      } catch (pinError) {
        setError(`Routing unchanged: ${errorMessage(pinError)}`);
      } finally {
        setPinningProvider(null);
      }
    },
    [managerBase, managementKey]
  );

  const reload = useCallback(async () => {
    const seq = ++requestSeq.current;
    setLoading(true);
    try {
      const response = await authFilesApi.list();
      if (seq !== requestSeq.current) return;
      const nextFiles = response.files ?? [];
      setFiles(nextFiles);
      const rows = buildAccountRows(nextFiles, useQuotaStore.getState());
      const results = await Promise.allSettled([loadSnapshots(rows), loadRouting()]);
      if (seq !== requestSeq.current) return;
      const snapshotFailure = results[0].status === 'rejected' ? results[0].reason : null;
      setError(
        snapshotFailure ? `Snapshot history unavailable: ${errorMessage(snapshotFailure)}` : null
      );
      const loadedAt = Date.now();
      setLastLoadedAtMs(loadedAt);
      setNowMs(loadedAt);
    } catch (loadError) {
      if (seq !== requestSeq.current) return;
      setError(errorMessage(loadError));
    } finally {
      if (seq === requestSeq.current) setLoading(false);
    }
  }, [loadRouting, loadSnapshots]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const refreshCredential = useCallback(
    async (credential: OverviewCredential) => {
      const refresher = REFRESHERS[credential.provider];
      if (!refresher || refreshingKeys.current.has(credential.key)) return;
      refreshingKeys.current.add(credential.key);
      setRefreshing((prev) => new Set(prev).add(credential.key));
      try {
        await refresher(credential.raw.raw, t);
      } catch {
        // The error state is already in the store and rendered on the row.
      } finally {
        refreshingKeys.current.delete(credential.key);
        setRefreshing((prev) => {
          const next = new Set(prev);
          next.delete(credential.key);
          return next;
        });
      }
    },
    [t]
  );

  const translateQuotaWindowLabel = useCallback(
    (
      label: string | undefined,
      labelKey?: string,
      labelParams?: Record<string, string | number>
    ) => {
      if (!labelKey) return label || 'Quota';
      return t(labelKey, { defaultValue: label || labelKey, ...labelParams });
    },
    [t]
  );

  const rows = useMemo(() => buildAccountRows(files, stores), [files, stores]);

  const credentials = useMemo(() => {
    return rows.map((row) => {
      const displayWindows = buildAccountQuotaDisplayWindows(row, {
        stores,
        translateQuotaWindowLabel,
        t,
        nowMs,
      });
      const definitions = buildAccountQuotaWindowDefinitions(displayWindows, nowMs);
      const merged = mergeAccountQuotaSnapshotWindows(
        definitions,
        snapshots.get(row.selectionKey) ?? [],
        {
          provider: row.provider,
          getLabel: (snapshot) => labelForSnapshotWindowId(snapshot.provider_window_id),
        }
      );
      return {
        ...buildOverviewCredential(row, merged, nowMs),
        refreshAtMs: overviewQuotaRefreshAtMs(
          row.provider,
          row.provider === 'claude'
            ? getCredentialScopedQuotaState(stores.claudeQuota, row.raw)
            : undefined
        ),
      };
    });
  }, [nowMs, rows, snapshots, stores, t, translateQuotaWindowLabel]);

  const providers = useMemo(() => buildOverviewProviders(credentials), [credentials]);

  const refreshAll = useCallback(async () => {
    setRefreshingAll(true);
    try {
      const tasks = buildProviderCredentialTaskPlan(
        credentials.filter((c) => !c.disabled && canRefreshProvider(c.provider)),
        { getProviderKey: (c) => c.provider, getCredentialKey: (c) => c.key }
      );
      await runProviderCredentialTaskPlan(tasks, { perProviderConcurrency: 1 }, ({ item }) =>
        refreshCredential(item)
      );
      await reload();
    } finally {
      setRefreshingAll(false);
    }
  }, [credentials, refreshCredential, reload]);

  useHeaderRefresh(refreshAll);

  // Page loads read persisted observations. Only an explicit refresh queries
  // providers; repeated browser reloads must not consume their request limits.

  void i18n;

  return {
    nowMs,
    loading,
    error,
    providers,
    credentials,
    refreshing,
    refreshingAll,
    lastLoadedAtMs,
    routing,
    pinningProvider,
    reload,
    pinRouting,
    refreshCredential,
    refreshAll,
  };
}
