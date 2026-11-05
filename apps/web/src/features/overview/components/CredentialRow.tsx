import { IconRefreshCw } from '@/components/ui/icons';
import {
  describeQuotaIssue,
  formatPercentValue,
  formatPlanType,
  formatResetParts,
  maskCredentialName,
  toneForRemaining,
  type OverviewCredential,
  type OverviewWindow,
  type RoutingSlot,
} from '../model/overviewModel';
import { IconPin, IconPinOff } from './PinIcons';
import { ProviderGlyph } from './ProviderGlyph';
import { UsageBar } from './UsageBar';
import { ResetLine } from './ResetLine';
import styles from '../OverviewPage.module.scss';

interface CredentialRowProps {
  credential: OverviewCredential;
  /** Window ids shared with the provider's other rows; null lays out this row's own. */
  columns: string[] | null;
  nowMs: number;
  locale: string;
  showEmails: boolean;
  refreshing: boolean;
  canRefresh: boolean;
  routing: RoutingSlot | null;
  pinBusy: boolean;
  onRefresh: () => void;
  onPin: (authIndex: string | null) => void;
}

export function CredentialRow({
  credential,
  columns,
  nowMs,
  locale,
  showEmails,
  refreshing,
  canRefresh,
  routing,
  pinBusy,
  onRefresh,
  onPin,
}: CredentialRowProps) {
  const name = showEmails ? credential.fileName : maskCredentialName(credential.fileName);
  const quotaState = credential.raw.quota;
  const errorText = quotaState.status === 'error' ? quotaState.error : null;
  const refreshInSeconds = Math.max(0, Math.ceil(((credential.refreshAtMs ?? 0) - nowMs) / 1_000));
  // The routing worker's applied priority is fresher than the page's file list.
  const priority = routing?.account.priority ?? credential.priority;
  const pinned = routing?.account.pinned === true;
  const pinEnds = pinned ? formatResetParts(routing?.pinUntilMs ?? null, nowMs, locale) : null;
  // The first account needs no pin, and an exhausted one cannot serve traffic.
  const canUseFirst =
    routing !== null && !pinned && routing.rank > 1 && (routing.account.used_percent ?? 0) < 100;
  const quotaIssue = routing ? describeQuotaIssue(routing.account) : null;
  const slots: Array<{ id: string; window: OverviewWindow | null }> = columns
    ? columns.map((id) => ({ id, window: credential.windows.find((w) => w.id === id) ?? null }))
    : credential.windows.map((window) => ({ id: window.id, window }));
  return (
    <div className={`${styles.row} ${credential.disabled ? styles.rowDisabled : ''}`}>
      <div className={styles.identity}>
        <div className={styles.fileName} title={credential.fileName}>
          <ProviderGlyph provider={credential.provider} size="sm" />
          {routing && (
            <span
              className={`${styles.rank} ${pinned ? styles.rankPinned : ''}`}
              title={pinned ? 'Pinned first' : `Routing order (${routing.strategy})`}
            >
              {pinned && <IconPin size={10} />}#{routing.rank}
            </span>
          )}
          <span className={styles.fileNameText}>{name}</span>
        </div>
        <div className={styles.meta}>
          {formatPlanType(credential.planType) && (
            <span className={styles.plan}>{formatPlanType(credential.planType)}</span>
          )}
          {credential.disabled && <span className={styles.badgeMuted}>disabled</span>}
          {priority !== null && <span className={styles.badgeMuted}>priority {priority}</span>}
          {pinned && (
            <span
              className={styles.badgePinned}
              title={
                pinEnds
                  ? `Pinned until this account's weekly reset, ${pinEnds.absolute}`
                  : 'Pinned until you unpin it'
              }
            >
              pinned{pinEnds ? ` · ends ${pinEnds.relative}` : ''}
            </span>
          )}
          {errorText && (
            <span className={styles.badgeError} title={errorText}>
              {quotaState.errorStatus === 429 ? 'refresh rate limited' : 'refresh failed'}
            </span>
          )}
          {errorText && credential.windows.length > 0 && (
            <span className={styles.badgeMuted}>last known quota</span>
          )}
          {quotaIssue && (
            <span className={styles.badgeError} title={quotaIssue.title}>
              {quotaIssue.label}
            </span>
          )}
        </div>
      </div>
      <div className={styles.windows}>
        {credential.windows.length === 0 ? (
          <div className={styles.noData}>No quota observed yet</div>
        ) : (
          slots.map(({ id, window }) =>
            window === null ? (
              <div key={id} className={styles.windowEmpty} aria-hidden="true" />
            ) : (
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
            )
          )
        )}
      </div>
      <div className={styles.actions}>
        {pinned && (
          <button
            type="button"
            className={styles.refreshButton}
            onClick={() => onPin(null)}
            disabled={pinBusy}
            aria-label={`Unpin ${credential.fileName}`}
          >
            <IconPinOff size={14} />
            <span>Unpin</span>
          </button>
        )}
        {canUseFirst && (
          <button
            type="button"
            className={styles.refreshButton}
            onClick={() => onPin(routing.account.auth_index)}
            disabled={pinBusy}
            aria-label={`Use ${credential.fileName} first`}
            title={
              routing.account.cycle_end_ms
                ? 'Route new requests to this account first until its weekly reset'
                : 'Route new requests to this account first until you unpin it'
            }
          >
            <IconPin size={14} />
            <span>Use first</span>
          </button>
        )}
        {canRefresh && (
          <button
            type="button"
            className={styles.refreshButton}
            onClick={onRefresh}
            disabled={refreshing || refreshInSeconds > 0}
            aria-label={`Refresh quota for ${credential.fileName}`}
          >
            <IconRefreshCw size={14} className={refreshing ? styles.spin : undefined} />
            <span>
              {refreshInSeconds > 0 ? `Refresh in ${refreshInSeconds}s` : 'Refresh quota'}
            </span>
          </button>
        )}
      </div>
    </div>
  );
}
