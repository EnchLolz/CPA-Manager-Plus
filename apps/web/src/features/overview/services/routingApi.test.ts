import axios from 'axios';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fetchRoutingStatus, setRoutingPin } from './routingApi';

vi.mock('axios', () => ({ default: { get: vi.fn(), put: vi.fn(), delete: vi.fn() } }));
vi.mock('@/features/demo/demoMode', () => ({ isDemoMode: () => false }));

const status = { enabled: true, strategy: 'earliest weekly reset first', last_poll_at_ms: 0, last_applied_at_ms: 0 };

describe('fetchRoutingStatus', () => {
  beforeEach(() => vi.clearAllMocks());

  it('normalizes a null worker order so the overview can map it before the first successful poll', async () => {
    const account = { name: 'codex.json', auth_index: '2', priority: 10 };
    vi.mocked(axios.get).mockResolvedValue({ data: { providers: {
      claude: { ...status, order: null },
      codex: { ...status, order: [account] },
    } } });

    const result = await fetchRoutingStatus('http://manager/', 'test-key');

    expect(result.claude.order.map((item) => item.name)).toEqual([]);
    expect(result.codex.order).toEqual([account]);
    expect(axios.get).toHaveBeenCalledWith('http://manager/v0/management/routing-priority', {
      timeout: 10_000, headers: { Authorization: 'Bearer test-key' }, signal: undefined,
    });
  });

  it('handles absent orders and an empty provider response', async () => {
    vi.mocked(axios.get).mockResolvedValueOnce({ data: { providers: { claude: status } } });
    expect((await fetchRoutingStatus('http://manager', null)).claude.order).toEqual([]);
    vi.mocked(axios.get).mockResolvedValueOnce({ data: { providers: null } });
    expect(await fetchRoutingStatus('http://manager', null)).toEqual({});
  });
});

describe('setRoutingPin', () => {
  beforeEach(() => vi.clearAllMocks());

  it('pins with PUT and clears with DELETE, returning the applied routing', async () => {
    const pinned = { name: 'nine-hour.json', auth_index: 'b', priority: 20, pinned: true };
    vi.mocked(axios.put).mockResolvedValue({ data: { providers: { claude: { ...status, order: [pinned] } } } });
    const result = await setRoutingPin('http://manager/', 'test-key', 'claude', 'b');
    expect(result.claude.order).toEqual([pinned]);
    expect(axios.put).toHaveBeenCalledWith(
      'http://manager/v0/management/routing-priority/pin',
      { provider: 'claude', auth_index: 'b' },
      { timeout: 60_000, headers: { Authorization: 'Bearer test-key' } }
    );

    vi.mocked(axios.delete).mockResolvedValue({ data: { providers: { claude: { ...status, order: null } } } });
    expect((await setRoutingPin('http://manager', null, 'claude', null)).claude.order).toEqual([]);
    expect(axios.delete).toHaveBeenCalledWith('http://manager/v0/management/routing-priority/pin', {
      timeout: 60_000, headers: undefined, params: { provider: 'claude' },
    });
  });
});
