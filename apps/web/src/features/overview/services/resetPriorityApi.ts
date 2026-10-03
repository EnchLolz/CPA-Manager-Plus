import axios from 'axios';

export interface ClaudeResetPriorityAccount {
  name: string;
  auth_index: string;
  priority: number;
  cycle_end_ms: number;
  used_percent: number;
}

export interface ClaudeResetPriorityStatus {
  enabled: boolean;
  last_poll_at_ms: number;
  last_applied_at_ms: number;
  last_error?: string;
  order: ClaudeResetPriorityAccount[];
}

/** Reads the reset-priority routing status from the personal manager-server endpoint. */
export const fetchClaudeResetPriorityStatus = async (
  managerServiceBase: string,
  managementKey: string | null | undefined,
  signal?: AbortSignal
): Promise<ClaudeResetPriorityStatus> => {
  const base = managerServiceBase.replace(/\/+$/, '');
  const response = await axios.get<ClaudeResetPriorityStatus>(
    `${base}/v0/management/claude-reset-priority`,
    {
      timeout: 10_000,
      headers: managementKey ? { Authorization: `Bearer ${managementKey}` } : undefined,
      signal,
    }
  );
  return response.data;
};
