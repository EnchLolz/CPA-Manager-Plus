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
} from '@/components/quota/quotaConfigs';
import {
  buildOverviewCredential,
  buildOverviewProviders,
  labelForSnapshotWindowId,
  type OverviewCredential,
  type OverviewProvider,
} from '../model/overviewModel';
import { fetchRoutingStatus, type RoutingStatusByProvider } from '../services/routingApi';

type QuotaStoreState = ReturnType<typeof useQuotaStore.getState>;
type Refresher = (file: AuthFileItem, t: TFunction) => Promise<void>;

const errorMessage = (error: unknown): string =>
  error instanceof Error ? error.message : String(error);
const errorStatus = (error: unknown): number | undefined =>
  typeof (error as { status?: unknown })?.status === 'number'
    ? (error as { status: number }).status
    : undefined;

const makeRefresher = <TState extends CredentialScopedQuotaState, TData>(
  config: QuotaConfig<TState, TData>,
  select: (state: QuotaStoreState) => (updater: (prev: Record<string, TState>) => Record<string, TState>) => void
): Refresher => {
  return async (file, t) => {
    const set = select(useQuotaStore.getState());
    const storeKey = config.getStoreKey?.(file) ?? file.name;
    set((prev) => ({ ...prev, [storeKey]: config.buildLoadingState(file) }));
    try {
      const data = await config.fetchQuota(file, t);
      set((prev) => ({ ...prev, [storeKey]: config.buildSuccessState(data, file, prev[storeKey]) }));
    } catch (error) {
      set((prev) => ({
        ...prev,
        [storeKey]: config.buildErrorState(errorMessage(error), errorStatus(error), file),
      }));
      throw error;
    }
  };
};

const REFRESHERS: Record<string, Refresher> = {
  claude: makeRefresher(CLAUDE_CONFIG, (s) => s.setClaudeQuota),
  codex: makeRefresher(CODEX_CONFIG, (s) => s.setCodexQuota),
  kimi: makeRefresher(KIMI_CONFIG, (s) => s.setKimiQuota),
  xai: makeRefresher(XAI_CONFIG, (s) => s.setXaiQuota),
  antigravity: makeRefresher(ANTIGRAVITY_CONFIG, (s) => s.setAntigravityQuota),
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
  reload: () => Promise<void>;
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
  const requestSeq = useRef(0);
  // One clock for the whole page so relative times stay consistent and pure.
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNowMs(Date.now()), 30_000);
    return () => window.clearInterval(id);
  }, []);

  const managerBase = availability.managerServiceAvailable ? availability.managerServiceBase : '';

  const loadSnapshots = useCallback(
    async (rows: AccountRow[]) => {
      if (!managerBase) return;
      const accounts = buildAccountQuotaSnapshotQueryAccounts(rows);
      if (accounts.length === 0) return;
      const response = await accountQuotaSnapshotApi.query(managerBase, managementKey ?? undefined, accounts);
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
      setError(snapshotFailure ? `Snapshot history unavailable: ${errorMessage(snapshotFailure)}` : null);
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
      if (!refresher) return;
      setRefreshing((prev) => new Set(prev).add(credential.key));
      try {
        await refresher(credential.raw.raw, t);
      } catch {
        // The error state is already in the store and rendered on the row.
      } finally {
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
    (label: string | undefined, labelKey?: string, labelParams?: Record<string, string | number>) => {
      if (!labelKey) return label || 'Quota';
      return t(labelKey, { defaultValue: label || labelKey, ...labelParams });
    },
    [t]
  );

  const rows = useMemo(() => buildAccountRows(files, stores), [files, stores]);

  // Disabled credentials are not routed and are not shown; manage them in
  // Credential Management.
  const credentials = useMemo(() => {
    return rows.filter((row) => !row.disabled).map((row) => {
      const displayWindows = buildAccountQuotaDisplayWindows(row, { stores, translateQuotaWindowLabel, t, nowMs });
      const definitions = buildAccountQuotaWindowDefinitions(displayWindows, nowMs);
      const merged = mergeAccountQuotaSnapshotWindows(definitions, snapshots.get(row.selectionKey) ?? [], {
        provider: row.provider,
        getLabel: (snapshot) => labelForSnapshotWindowId(snapshot.provider_window_id),
      });
      return buildOverviewCredential(row, merged, nowMs);
    });
  }, [nowMs, rows, snapshots, stores, t, translateQuotaWindowLabel]);

  const providers = useMemo(() => buildOverviewProviders(credentials), [credentials]);

  const refreshAll = useCallback(async () => {
    setRefreshingAll(true);
    try {
      await Promise.allSettled(
        credentials.filter((c) => !c.disabled && canRefreshProvider(c.provider)).map(refreshCredential)
      );
      await reload();
    } finally {
      setRefreshingAll(false);
    }
  }, [credentials, refreshCredential, reload]);

  useHeaderRefresh(refreshAll);

  // Fetch live quota for every enabled credential once the inventory is in,
  // so the page is current on open without a manual click.
  const autoRefreshed = useRef(false);
  useEffect(() => {
    if (loading || autoRefreshed.current || credentials.length === 0) return;
    autoRefreshed.current = true;
    const targets = credentials.filter((c) => !c.disabled && canRefreshProvider(c.provider));
    if (targets.length === 0) return;
    setRefreshingAll(true);
    void Promise.allSettled(targets.map(refreshCredential)).finally(() => setRefreshingAll(false));
  }, [credentials, loading, refreshCredential]);

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
    reload,
    refreshCredential,
    refreshAll,
  };
}
