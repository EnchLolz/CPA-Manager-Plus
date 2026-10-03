import type { ClaudeResetPriorityStatus } from '../services/resetPriorityApi';
import { formatResetParts, maskCredentialName } from '../model/overviewModel';
import styles from '../OverviewPage.module.scss';

interface RoutingCardProps {
  status: ClaudeResetPriorityStatus;
  nowMs: number;
  locale: string;
  showEmails: boolean;
}

const ago = (ms: number, nowMs: number): string => {
  if (!ms) return 'never';
  const delta = nowMs - ms;
  if (delta < 60_000) return 'just now';
  if (delta < 3_600_000) return `${Math.round(delta / 60_000)} min ago`;
  return `${Math.round(delta / 3_600_000)} h ago`;
};

export function RoutingCard({ status, nowMs, locale, showEmails }: RoutingCardProps) {
  const tone = !status.enabled ? styles.pillOff : status.last_error ? styles.pillWarn : styles.pillOn;
  return (
    <section className={styles.routingCard} aria-label="Claude routing">
      <div className={styles.routingHead}>
        <div>
          <div className={styles.routingTitle}>Claude routing</div>
          <div className={styles.routingSub}>
            Highest priority goes to the enabled account whose weekly limit resets soonest.
          </div>
        </div>
        <span className={`${styles.pill} ${tone}`}>
          {status.enabled ? 'earliest reset first' : 'static priorities'}
        </span>
      </div>
      {status.enabled && (
        <ol className={styles.routingOrder}>
          {status.order.map((account, index) => {
            const reset = formatResetParts(account.cycle_end_ms, nowMs, locale);
            return (
              <li key={account.auth_index}>
                <span className={styles.rank}>#{index + 1}</span>
                <span className={styles.routingName}>
                  {showEmails ? account.name : maskCredentialName(account.name)}
                </span>
                <span className={styles.routingDetail}>
                  {Math.round(100 - account.used_percent)}% left · resets {reset?.relative ?? '-'}
                </span>
              </li>
            );
          })}
          {status.order.length === 0 && <li className={styles.routingDetail}>No order applied yet</li>}
        </ol>
      )}
      <div className={styles.routingFoot}>
        <span>polled {ago(status.last_poll_at_ms, nowMs)}</span>
        {status.enabled && <span>applied {ago(status.last_applied_at_ms, nowMs)}</span>}
        {status.last_error && <span className={styles.routingError}>{status.last_error}</span>}
      </div>
    </section>
  );
}
