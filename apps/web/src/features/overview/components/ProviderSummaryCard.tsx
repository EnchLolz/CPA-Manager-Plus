import type { OverviewProvider } from '../model/overviewModel';
import { formatPercentValue } from '../model/overviewModel';
import { SegmentedBar } from './UsageBar';
import { ResetLine } from './ResetLine';
import styles from '../OverviewPage.module.scss';

interface ProviderSummaryCardProps {
  provider: OverviewProvider;
  label: string;
  nowMs: number;
  locale: string;
  active: boolean;
  onSelect: () => void;
}

export function ProviderSummaryCard({ provider, label, nowMs, locale, active, onSelect }: ProviderSummaryCardProps) {
  const enabled = provider.credentials.filter((c) => !c.disabled).length;
  return (
    <button
      type="button"
      className={`${styles.summaryCard} ${active ? styles.summaryCardActive : ''}`}
      onClick={onSelect}
      aria-pressed={active}
    >
      <div className={styles.summaryHead}>
        <span className={styles.summaryName}>{label}</span>
        <span className={styles.summaryCount}>
          {enabled} credential{enabled === 1 ? '' : 's'}
        </span>
      </div>
      {provider.capacityPercent === 0 ? (
        <>
          <div className={styles.summaryWindowLabel}>No quota observed</div>
          <div className={styles.summaryValue}>
            <span className={`${styles.summaryBig} ${styles.summaryMuted}`}>--</span>
          </div>
          <SegmentedBar segments={provider.segments} />
          <div className={styles.reset}>Refresh to fetch quota</div>
        </>
      ) : (
        <>
          <div className={styles.summaryWindowLabel}>{provider.headlineLabel}</div>
          <div className={styles.summaryValue}>
            <span className={styles.summaryBig}>{formatPercentValue(provider.remainingPercent)}</span>
            <span className={styles.summaryOf}>of {provider.capacityPercent}% left</span>
          </div>
          <SegmentedBar segments={provider.segments} />
          <ResetLine resetAtMs={provider.nextResetAtMs} nowMs={nowMs} locale={locale} emptyText="No reset scheduled" />
        </>
      )}
    </button>
  );
}
