import { apiClient, createScopedApiRequestConfig } from './client';

export interface SharedKey {
  id: string;
  name: string;
  models: string[];
  created_at: number;
  expires_at: number;
  max_tokens: number;
  max_cost_micros: number;
  used_tokens: number;
  used_cost_micros: number;
  revoked: boolean;
  in_flight: boolean;
  blocked: boolean;
}

export type CreateSharedKey = Pick<
  SharedKey,
  'name' | 'models' | 'expires_at' | 'max_tokens' | 'max_cost_micros'
>;

export const sharedKeysApi = (apiBase: string, managementKey: string) => {
  const config = createScopedApiRequestConfig({ apiBase, managementKey });
  return {
    list: () =>
      apiClient.get<{ items: SharedKey[]; gateway_base_url?: string }>('/shared-keys', config),
    create: (input: CreateSharedKey) =>
      apiClient.post<{ item: SharedKey; key: string; gateway_base_url?: string }>(
        '/shared-keys',
        input,
        config
      ),
    revoke: (id: string) => apiClient.delete(`/shared-keys/${encodeURIComponent(id)}`, config),
  };
};
