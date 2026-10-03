import { useState } from 'react';
import { IconChevronDown } from '@/components/ui/icons';
import type { RoutingStatus } from '../services/routingApi';
import { formatResetParts, maskCredentialName } from '../model/overviewModel';
import styles from '../OverviewPage.module.scss';

interface RoutingDisclosureProps {
  status: RoutingStatus;
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

/**
 * One collapsed line under a provider heading: what the routing worker is
 * doing and who is first. Expands to the full order.
 */
export function RoutingDisclosure({ status, nowMs, locale, showEmails }: RoutingDisclosureProps) {
  const [open, setOpen] = useState(false);
  const first = status.order[0];
  const name = (value: string) => (showEmails ? value : maskCredentialName(value));
  const summary = !status.enabled
    ? 'Routing: static priorities'
    : status.last_error
      ? 'Routing paused'
      : first
        ? `Routing: ${status.strategy}`
        : 'Routing: waiting for first poll';
  return (
    <div className={`${styles.routing} ${open ? styles.routingOpen : ''}`}>
      <button
        type="button"
        className={styles.routingToggle}
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <span className={`${styles.routingState} ${status.enabled && !status.last_error ? styles.routingOn : styles.routingOff}`} />
        <span className={styles.routingSummary}>{summary}</span>
        {status.enabled && first && !open && (
          <span className={styles.routingFirst}>
            #1 <span className={styles.mono}>{name(first.name)}</span>
          </span>
        )}
        <IconChevronDown size={14} className={styles.routingChevron} />
      </button>
      {open && (
        <div className={styles.routingBody}>
          {status.enabled ? (
            <ol className={styles.routingOrder}>
              {status.order.map((account, index) => {
                const reset = account.cycle_end_ms ? formatResetParts(account.cycle_end_ms, nowMs, locale) : null;
                const detail = [
                  account.tier ? account.tier : null,
                  typeof account.used_percent === 'number' ? `${Math.round(100 - account.used_percent)}% left` : null,
                  reset ? `resets ${reset.relative}` : null,
                  `priority ${account.priority}`,
                ]
                  .filter(Boolean)
                  .join(' · ');
                return (
                  <li key={account.auth_index || account.name}>
                    <span className={styles.rank}>#{index + 1}</span>
                    <span className={`${styles.mono} ${styles.routingName}`}>{name(account.name)}</span>
                    <span className={styles.routingDetail}>{detail}</span>
                  </li>
                );
              })}
              {status.order.length === 0 && <li className={styles.routingDetail}>No order applied yet</li>}
            </ol>
          ) : (
            <div className={styles.routingDetail}>
              The priority worker is off for this provider. CPA uses the priorities set on each credential.
            </div>
          )}
          <div className={styles.routingFoot}>
            <span>polled {ago(status.last_poll_at_ms, nowMs)}</span>
            {status.enabled && <span>applied {ago(status.last_applied_at_ms, nowMs)}</span>}
            {status.last_error && <span className={styles.routingError}>{status.last_error}</span>}
          </div>
        </div>
      )}
    </div>
  );
}
