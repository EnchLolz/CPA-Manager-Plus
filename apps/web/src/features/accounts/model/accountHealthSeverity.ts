export type AccountHealthSeverity =
  | 'ok'
  | 'warning'
  | 'cooldown'
  | 'critical'
  | 'disabled'
  | 'unknown';

export const severityFromQuotaStatus = (
  status: string | number | null | undefined,
  disabled?: boolean
): AccountHealthSeverity => {
  if (disabled) return 'disabled';
  const token = String(status ?? '').trim();
  switch (token) {
    case 'available':
      return 'ok';
    case 'warning':
      return 'warning';
    case 'cooldown':
      return 'cooldown';
    case 'exhausted':
      return 'critical';
    default:
      return 'unknown';
  }
};
