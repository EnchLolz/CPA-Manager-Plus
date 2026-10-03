import { IconRefreshCw } from '@/components/ui/icons';
import {
  formatPercentValue,
  maskCredentialName,
  toneForRemaining,
  type OverviewCredential,
} from '../model/overviewModel';
import { ProviderGlyph } from './ProviderGlyph';
import { UsageBar } from './UsageBar';
import { ResetLine } from './ResetLine';
import styles from '../OverviewPage.module.scss';

interface CredentialRowProps {
  credential: OverviewCredential;
  nowMs: number;
  locale: string;
  showEmails: boolean;
  refreshing: boolean;
  canRefresh: boolean;
  routingRank: number | null;
  onRefresh: () => void;
}

export function CredentialRow({
  credential,
  nowMs,
  locale,
  showEmails,
  refreshing,
  canRefresh,
  routingRank,
  onRefresh,
}: CredentialRowProps) {
  const name = showEmails ? credential.fileName : maskCredentialName(credential.fileName);
  const quotaState = credential.raw.quota;
  const errorText = quotaState.status === 'error' ? quotaState.error : null;
  const statusTone = credential.disabled
    ? 'none'
    : errorText
      ? 'bad'
      : toneForRemaining(credential.headline?.remainingPercent ?? null);
  const statusTitle = credential.disabled
    ? 'Disabled'
    : errorText
      ? 'Last refresh failed'
      : credential.headline
        ? `${credential.headline.label}: ${formatPercentValue(credential.headline.remainingPercent)} left`
        : 'No quota observed';
  return (
    <div className={`${styles.row} ${credential.disabled ? styles.rowDisabled : ''}`}>
      <div className={styles.identity}>
        <div className={styles.fileName} title={credential.fileName}>
          <span className={`${styles.statusDot} ${styles[`bg_${statusTone}`]}`} title={statusTitle} />
          <ProviderGlyph provider={credential.provider} size="sm" />
          {routingRank !== null && (
            <span className={styles.rank} title="Routing order (earliest weekly reset first)">
              #{routingRank}
            </span>
          )}
          {name}
        </div>
        <div className={styles.meta}>
          {credential.planType && <span className={styles.plan}>{credential.planType}</span>}
          {credential.disabled && <span className={styles.badgeMuted}>disabled</span>}
          {credential.priority !== null && (
            <span className={styles.badgeMuted}>priority {credential.priority}</span>
          )}
          {errorText && (
            <span className={styles.badgeError} title={errorText}>
              refresh failed
            </span>
          )}
        </div>
      </div>
      <div className={styles.windows}>
        {credential.windows.length === 0 ? (
          <div className={styles.noData}>No quota observed yet</div>
        ) : (
          credential.windows.map((window) => (
            <div key={window.id} className={styles.window}>
              <div className={styles.windowHead}>
                <span className={styles.windowLabel}>{window.label}</span>
                <span
                  className={`${styles.windowValue} ${styles[`text_${toneForRemaining(window.remainingPercent)}`]}`}
                >
                  {formatPercentValue(window.remainingPercent)}
                </span>
              </div>
              <UsageBar remainingPercent={window.remainingPercent} stale={window.stale} />
              <ResetLine resetAtMs={window.resetAtMs} nowMs={nowMs} locale={locale} />
            </div>
          ))
        )}
      </div>
      <div className={styles.actions}>
        {canRefresh && (
          <button
            type="button"
            className={styles.refreshButton}
            onClick={onRefresh}
            disabled={refreshing}
            aria-label={`Refresh quota for ${credential.fileName}`}
          >
            <IconRefreshCw size={14} className={refreshing ? styles.spin : undefined} />
            <span>Refresh quota</span>
          </button>
        )}
      </div>
    </div>
  );
}
