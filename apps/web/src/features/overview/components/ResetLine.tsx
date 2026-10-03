import { formatResetParts } from '../model/overviewModel';
import styles from '../OverviewPage.module.scss';

interface ResetLineProps {
  resetAtMs: number | null;
  nowMs: number;
  locale: string;
  emptyText?: string;
}

export function ResetLine({ resetAtMs, nowMs, locale, emptyText = 'No reset pending' }: ResetLineProps) {
  const parts = formatResetParts(resetAtMs, nowMs, locale);
  if (!parts) return <div className={styles.reset}>{emptyText}</div>;
  return (
    <div className={styles.reset}>
      <span className={styles.resetRelative}>{parts.relative}</span>
      <span className={styles.resetDot}>·</span>
      <span>{parts.absolute}</span>
    </div>
  );
}
