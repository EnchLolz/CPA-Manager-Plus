import { useEffect, useMemo, useState } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import {
  accountQuotaSnapshotApi,
  monitoringAnalyticsApi,
  type AccountQuotaSnapshotWindow,
  type MonitoringAnalyticsSummary,
} from '@/services/api/usageService';
import type { AccountRow } from '../model/accountRows';
import { buildAccountQuotaSnapshotQueryAccounts } from '../model/accountQuotaSnapshots';
import { mapWithConcurrency } from '../model/asyncPool';
import {
  comparisonRatio,
  remainingTokenEstimate,
  selectWeeklyQuota,
} from '../model/accountComparison';
import styles from './AccountComparison.module.scss';

type Result = {
  summary?: MonitoringAnalyticsSummary;
  quota?: AccountQuotaSnapshotWindow;
  cycle?: MonitoringAnalyticsSummary;
  error?: string;
};
type Preferences = {
  baseline: string;
  prices: Record<string, string>;
  exclusive: Record<string, boolean>;
};
const emptyPreferences: Preferences = { baseline: '', prices: {}, exclusive: {} };
const number = (n: number | undefined) =>
  n == null
    ? 'Unknown'
    : new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 2 }).format(n);
const dollars = (n: number | undefined) =>
  n == null
    ? 'Unknown'
    : new Intl.NumberFormat('en', {
        style: 'currency',
        currency: 'USD',
        maximumFractionDigits: 2,
      }).format(n);

export function AccountComparison({
  rows,
  base,
  managementKey,
  onClose,
}: {
  rows: AccountRow[];
  base: string;
  managementKey: string | undefined;
  onClose: () => void;
}) {
  const storageKey = `account-value-comparison:${base}`;
  const [preferences, setPreferences] = useState<Preferences>(() => {
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey) ?? 'null');
      return saved && typeof saved.baseline === 'string' && saved.prices && saved.exclusive
        ? saved
        : emptyPreferences;
    } catch {
      return emptyPreferences;
    }
  });
  const accounts = useMemo(() => rows.filter((row) => !row.runtimeOnly), [rows]);
  const [days, setDays] = useState(7);
  const [provider, setProvider] = useState('claude');
  const [metric, setMetric] = useState<'output_tokens' | 'total_tokens' | 'total_cost'>(
    'output_tokens'
  );
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(true);
  const [results, setResults] = useState<Record<string, Result>>({});
  const [asOf, setAsOf] = useState(() => Date.now());
  const [error, setError] = useState('');
  const [quotaError, setQuotaError] = useState('');
  const baselineKey = accounts.some((row) => row.selectionKey === preferences.baseline)
    ? preferences.baseline
    : ((accounts.find((row) => row.provider === 'claude' && !row.disabled) ?? accounts[0])
        ?.selectionKey ?? '');
  const baseline = results[baselineKey]?.summary;
  const displayedAccounts = accounts
    .filter(
      (row) => provider === 'all' || row.provider === provider || row.selectionKey === baselineKey
    )
    .sort(
      (a, b) => Number(b.selectionKey === baselineKey) - Number(a.selectionKey === baselineKey)
    );

  useEffect(() => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(preferences));
    } catch {
      /* Storage may be disabled. */
    }
  }, [preferences, storageKey]);

  useEffect(() => {
    const controller = new AbortController();
    const now = Date.now();
    const summary = async (row: AccountRow, from: number, to: number) => {
      const response = await monitoringAnalyticsApi.getAnalytics(
        base,
        managementKey,
        {
          from_ms: from,
          to_ms: to,
          now_ms: now,
          filters: {
            auth_files: [row.fileName],
            ...(row.authIndex ? { auth_indices: [String(row.authIndex)] } : {}),
          },
          include: { summary: true, summary_profile: 'compact' },
        },
        controller.signal
      );
      return response.summary;
    };
    void (async () => {
      const quotas = new Map<string, AccountQuotaSnapshotWindow>();
      try {
        const targets = buildAccountQuotaSnapshotQueryAccounts(accounts);
        for (let i = 0; i < targets.length; i += 200) {
          const response = await accountQuotaSnapshotApi.query(
            base,
            managementKey,
            targets.slice(i, i + 200),
            {},
            controller.signal
          );
          for (const item of response.items) {
            const quota = selectWeeklyQuota(item.windows, now);
            if (quota) quotas.set(item.row_key, quota);
          }
        }
      } catch {
        if (!controller.signal.aborted)
          setQuotaError('Weekly quota history is unavailable. Recorded usage is still shown.');
      }
      const entries = await mapWithConcurrency(
        accounts,
        3,
        async (row): Promise<[string, Result]> => {
          if (controller.signal.aborted) return [row.selectionKey, {}];
          const quota = quotas.get(row.selectionKey);
          try {
            const measured = await summary(row, now - days * 86400_000, now);
            let cycle: MonitoringAnalyticsSummary | undefined;
            if (quota?.cycle_start_ms != null && quota.observed_at_ms > quota.cycle_start_ms) {
              try {
                cycle = await summary(row, quota.cycle_start_ms, quota.observed_at_ms);
              } catch {
                /* Keep measured period even if cycle query fails. */
              }
            }
            return [row.selectionKey, { summary: measured, quota, cycle }];
          } catch {
            return [
              row.selectionKey,
              { quota, error: 'Usage could not be loaded. Retry to compare this account.' },
            ];
          }
        }
      );
      if (!controller.signal.aborted) {
        setAsOf(now);
        setResults(Object.fromEntries(entries));
        setLoading(false);
      }
    })().catch(() => {
      if (!controller.signal.aborted) {
        setError('Comparison could not be loaded. Please retry.');
        setLoading(false);
      }
    });
    return () => controller.abort();
  }, [accounts, base, managementKey, days, revision]);

  return (
    <Modal open onClose={onClose} width={1180} title="Account value comparison">
      <div className={styles.comparison}>
        <p className={styles.intro}>
          Compare the usage each account actually delivers. Choose your Max 20x account as the
          baseline. Organization allowances may be shared or unavailable.
        </p>
        <div className={styles.controls}>
          <label>
            Baseline account
            <select
              value={baselineKey}
              onChange={(event) => setPreferences({ ...preferences, baseline: event.target.value })}
            >
              {accounts.map((row) => (
                <option key={row.selectionKey} value={row.selectionKey}>
                  {row.accountLabel} · {row.provider}
                </option>
              ))}
            </select>
          </label>
          <label>
            Show accounts
            <select value={provider} onChange={(event) => setProvider(event.target.value)}>
              <option value="all">All providers</option>
              {Array.from(new Set(accounts.map((row) => row.provider))).sort().map((name) => (
                <option key={name} value={name}>{name}</option>
              ))}
            </select>
          </label>
          <label>
            Comparison period
            <select
              value={days}
              onChange={(event) => {
                setLoading(true);
                setError('');
                setQuotaError('');
                setDays(Number(event.target.value));
              }}
            >
              <option value={7}>Last 7 days</option>
              <option value={30}>Last 30 days</option>
              <option value={1}>Last 24 hours</option>
            </select>
          </label>
          <label>
            Compare by
            <select
              value={metric}
              onChange={(event) => setMetric(event.target.value as typeof metric)}
            >
              <option value="output_tokens">Output tokens</option>
              <option value="total_tokens">Total tokens</option>
              <option value="total_cost">API-equivalent value</option>
            </select>
          </label>
          <Button
            variant="secondary"
            onClick={() => {
              setLoading(true);
              setError('');
              setQuotaError('');
              setRevision((value) => value + 1);
            }}
            loading={loading}
          >
            Refresh
          </Button>
        </div>
        <p className={styles.note}>
          All accounts use the same time range, ending {new Date(asOf).toLocaleString()}. A new
          account starts with only the traffic recorded since it was added.
        </p>
        {error && <p role="alert">{error}</p>}
        {quotaError && <p role="status">{quotaError}</p>}
        {loading ? (
          <p role="status">Loading recorded usage and weekly allowances…</p>
        ) : accounts.length === 0 ? (
          <p>No accounts available. Add an account to begin measuring.</p>
        ) : (
          <div className={styles.grid}>
            {displayedAccounts.map((row) => {
              const result = results[row.selectionKey] ?? {};
              const usage = result.summary;
              const quota = result.quota;
              const pricedValue =
                usage && (usage.total_cost > 0 || usage.total_tokens === 0)
                  ? usage.total_cost
                  : undefined;
              const baselineValue =
                metric === 'total_cost' && !baseline?.total_cost ? undefined : baseline?.[metric];
              const ratio = comparisonRatio(
                metric === 'total_cost' ? pricedValue : usage?.[metric],
                baselineValue
              );
              const monthly = Number(preferences.prices[row.selectionKey]);
              const paid =
                Number.isFinite(monthly) && monthly > 0
                  ? (monthly * days) / (365.25 / 12)
                  : undefined;
              const valueRatio = comparisonRatio(pricedValue, paid);
              const remaining = remainingTokenEstimate(
                quota,
                result.cycle?.total_tokens,
                !!preferences.exclusive[row.selectionKey],
                asOf
              );
              const fresh = quota && !quota.stale && asOf - quota.observed_at_ms <= 15 * 60_000;
              return (
                <article
                  key={row.selectionKey}
                  className={styles.card}
                  data-baseline={row.selectionKey === baselineKey}
                >
                  <header>
                    <div>
                      <h3>{row.accountLabel}</h3>
                      <span>
                        {row.provider}
                        {row.selectionKey === baselineKey ? ' · Baseline' : ''}
                      </span>
                    </div>
                  </header>
                  {result.error ? (
                    <p role="alert">{result.error}</p>
                  ) : (
                    <>
                      <div className={styles.value}>
                        <strong>{dollars(pricedValue)}</strong>
                        <span>API-equivalent value delivered</span>
                      </div>
                      {usage && pricedValue == null && (
                        <p className={styles.note}>
                          Model pricing is missing. Token comparisons remain available.
                        </p>
                      )}
                      <p className={styles.ratio}>
                        {row.selectionKey === baselineKey
                          ? 'Comparison baseline'
                          : ratio == null
                            ? 'Baseline value not yet available'
                            : `${ratio.toFixed(2)}× baseline ${metric === 'total_cost' ? 'API value' : metric === 'output_tokens' ? 'output tokens' : 'total tokens'}`}
                      </p>
                      <dl className={styles.metrics}>
                        <div>
                          <dt>Output tokens</dt>
                          <dd>{number(usage?.output_tokens)}</dd>
                        </div>
                        <div>
                          <dt>Input tokens</dt>
                          <dd>{number(usage?.input_tokens)}</dd>
                        </div>
                        <div>
                          <dt>Cache reads</dt>
                          <dd>{number(usage?.cache_read_tokens)}</dd>
                        </div>
                        <div>
                          <dt>Cache writes</dt>
                          <dd>{number(usage?.cache_creation_tokens)}</dd>
                        </div>
                        <div>
                          <dt>Total recorded tokens</dt>
                          <dd>{number(usage?.total_tokens)}</dd>
                        </div>
                        <div>
                          <dt>Requests / failed</dt>
                          <dd>
                            {number(usage?.total_calls)} / {number(usage?.failure_calls)}
                          </dd>
                        </div>
                      </dl>
                    </>
                  )}
                  <section className={styles.quota}>
                    <h4>Current weekly allowance</h4>
                    {quota ? (
                      <>
                        <strong>
                          {quota.remaining_percent != null
                            ? `${quota.remaining_percent.toFixed(1)}% remaining`
                            : 'Remaining allowance unknown'}
                          {fresh ? '' : ' · stale'}
                        </strong>
                        <p>Resets {new Date(quota.cycle_end_ms!).toLocaleString()}</p>
                        <p>Observed {new Date(quota.observed_at_ms).toLocaleString()}</p>
                        <p>
                          {number(result.cycle?.total_tokens)} tokens recorded this cycle through
                          that observation.
                        </p>
                      </>
                    ) : (
                      <p>No current weekly limit reported. This does not mean unlimited usage.</p>
                    )}
                    <label className={styles.check}>
                      <input
                        type="checkbox"
                        checked={!!preferences.exclusive[row.selectionKey]}
                        onChange={(event) =>
                          setPreferences({
                            ...preferences,
                            exclusive: {
                              ...preferences.exclusive,
                              [row.selectionKey]: event.target.checked,
                            },
                          })
                        }
                      />
                      This quota is personal and all usage goes through CPA
                    </label>
                    <p>
                      <b>
                        Estimated tokens left:{' '}
                        {remaining == null ? 'Not enough evidence' : number(remaining)}
                      </b>
                    </p>
                    <p className={styles.note}>
                      Requires a fresh quota, a usable cycle, and at least 5% consumed. Assumes a
                      similar model and cache mix. Shared organization quotas cannot estimate your
                      personal capacity.
                    </p>
                  </section>
                  <label className={styles.price}>
                    Monthly account cost, USD
                    <input
                      type="number"
                      min="0"
                      step="0.01"
                      placeholder="Optional"
                      value={preferences.prices[row.selectionKey] ?? ''}
                      onChange={(event) =>
                        setPreferences({
                          ...preferences,
                          prices: { ...preferences.prices, [row.selectionKey]: event.target.value },
                        })
                      }
                    />
                  </label>
                  {valueRatio != null && (
                    <p>
                      {valueRatio.toFixed(2)}× API-equivalent value per subscription dollar during
                      this period. Subscription cost is prorated using an average month.
                    </p>
                  )}
                </article>
              );
            })}
          </div>
        )}
        <p className={styles.note}>
          Value uses your configured model prices, including cache pricing. Missing or outdated
          prices can understate value. It is a pricing comparison, not money saved or a guaranteed
          allowance. Usage outside CPA is excluded. Baseline, cost, and quota assumptions are saved
          in this browser.
        </p>
      </div>
    </Modal>
  );
}
