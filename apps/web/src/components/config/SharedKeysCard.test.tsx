import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { SharedKeysCard } from './SharedKeysCard';

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  revoke: vi.fn(),
  notify: vi.fn(),
  confirm: vi.fn(),
}));
vi.mock('@/services/api/sharedKeys', () => ({ sharedKeysApi: () => mocks }));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('@/stores', () => ({
  useAuthStore: (select: (state: unknown) => unknown) => select({ managementKey: 'admin' }),
  useNotificationStore: (select: (state: unknown) => unknown) =>
    select({ showNotification: mocks.notify, showConfirmation: mocks.confirm }),
}));
vi.mock('@/components/ui/Modal', () => ({
  Modal: ({ open, children, footer }: { open: boolean; children: ReactNode; footer: ReactNode }) =>
    open ? (
      <div>
        {children}
        {footer}
      </div>
    ) : null,
}));

let renderer: ReactTestRenderer;
const click = async (text: string) => {
  await act(async () => {
    const matches = renderer.root
      .findAllByType(Button)
      .filter((button) => button.props.children === text);
    await matches[matches.length - 1].props.onClick();
  });
};
const fill = async (suffix: string, value: string) => {
  await act(async () => {
    renderer.root
      .find((node) => typeof node.props.id === 'string' && node.props.id.endsWith(`-${suffix}`))
      .props.onChange({ target: { value } });
  });
};

beforeEach(async () => {
  vi.clearAllMocks();
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('window', { setInterval: () => 1, clearInterval: () => {} });
  mocks.list.mockResolvedValue({ items: [] });
  mocks.create.mockResolvedValue({
    key: 'sk-share-once',
    item: {
      id: 'test',
      name: 'Friend',
      models: ['test-model'],
      expires_at: Date.now() + 86400000,
      max_tokens: 100,
      max_cost_micros: 0,
      used_tokens: 0,
      used_cost_micros: 0,
    },
  });
  await act(async () => {
    renderer = create(<SharedKeysCard serviceBase="https://manager.example" />);
  });
});
afterEach(async () => {
  await act(async () => renderer.unmount());
  vi.unstubAllGlobals();
});

it('creates an expiring capped key and shows its secret only until the dialog closes', async () => {
  await click('shared_keys.create');
  await fill('name', 'Friend');
  await fill('models', 'test-model');
  await fill('cost', '');
  await fill('tokens', '100');
  const before = Date.now();
  await click('shared_keys.create');
  expect(mocks.create).toHaveBeenCalledWith(
    expect.objectContaining({
      name: 'Friend',
      models: ['test-model'],
      max_tokens: 100,
      max_cost_micros: 0,
      expires_at: expect.any(Number),
    })
  );
  expect(mocks.create.mock.calls[0][0].expires_at).toBeGreaterThan(before);
  expect(renderer.root.findAllByType('input').map((node) => node.props.value)).toEqual([
    'https://manager.example/share/v1',
    'sk-share-once',
  ]);
  await click('common.close');
  expect(JSON.stringify(renderer.toJSON())).not.toContain('sk-share-once');
});

it('rejects an unlimited key and fractional token allowances before sending a request', async () => {
  await click('shared_keys.create');
  await fill('name', 'Friend');
  await fill('models', 'test-model');
  await fill('cost', '0');
  await click('shared_keys.create');
  expect(mocks.create).not.toHaveBeenCalled();
  await fill('tokens', '1.5');
  await click('shared_keys.create');
  expect(mocks.create).not.toHaveBeenCalled();
});

it('copies the configured public gateway instead of the private panel address', async () => {
  mocks.list.mockResolvedValue({
    items: [],
    gateway_base_url: 'https://kiwi.example.ts.net:8443/share/v1',
  });
  mocks.create.mockResolvedValueOnce({
    key: 'sk-share-public',
    item: {
      id: 'public',
      name: 'Friend',
      models: ['test-model'],
      expires_at: Date.now() + 86400000,
      max_tokens: 100,
      max_cost_micros: 0,
      used_tokens: 0,
      used_cost_micros: 0,
    },
    gateway_base_url: 'https://kiwi.example.ts.net:8443/share/v1',
  });
  await click('shared_keys.create');
  await fill('name', 'Friend');
  await fill('models', 'test-model');
  await fill('cost', '');
  await fill('tokens', '100');
  await click('shared_keys.create');
  expect(renderer.root.findAllByType('input').map((node) => node.props.value)).toContain(
    'https://kiwi.example.ts.net:8443/share/v1'
  );
});
