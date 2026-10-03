import { toneForRemaining, type OverviewSegment } from '../model/overviewModel';
import styles from '../OverviewPage.module.scss';

interface UsageBarProps {
  remainingPercent: number | null;
  stale?: boolean;
}

/** One credential window: fill shows what is still available. */
export function UsageBar({ remainingPercent, stale = false }: UsageBarProps) {
  const tone = toneForRemaining(remainingPercent);
  const width = remainingPercent === null ? 0 : Math.max(0, Math.min(100, remainingPercent));
  return (
    <div className={`${styles.track} ${stale ? styles.trackStale : ''}`} aria-hidden="true">
      <div className={`${styles.fill} ${styles[`tone_${tone}`]}`} style={{ width: `${width}%` }} />
    </div>
  );
}

interface SegmentedBarProps {
  segments: OverviewSegment[];
}

/** Provider summary: one segment per enabled credential. */
export function SegmentedBar({ segments }: SegmentedBarProps) {
  if (segments.length === 0) return <div className={styles.track} aria-hidden="true" />;
  return (
    <div className={styles.segments} aria-hidden="true">
      {segments.map((segment) => {
        const tone = toneForRemaining(segment.remainingPercent);
        const width = segment.remainingPercent === null ? 0 : Math.max(0, Math.min(100, segment.remainingPercent));
        return (
          <div key={segment.key} className={styles.track}>
            <div className={`${styles.fill} ${styles[`tone_${tone}`]}`} style={{ width: `${width}%` }} />
          </div>
        );
      })}
    </div>
  );
}
