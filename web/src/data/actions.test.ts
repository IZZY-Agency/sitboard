import { describe, expect, it } from 'vitest';
import { createActions } from './actions';

function recorder(code: number | 'down') {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const fetchFn = ((url: string, init: RequestInit) => {
    calls.push({ url, init });
    if (code === 'down') return Promise.reject(new TypeError('network'));
    return Promise.resolve(new Response(null, { status: code }));
  }) as unknown as typeof fetch;
  return { calls, actions: createActions(fetchFn) };
}

describe('actions', () => {
  it('jump posts the card id only', async () => {
    const r = recorder(204);
    expect(await r.actions.jump('claude:1')).toEqual({ ok: true });
    expect(r.calls[0].url).toBe('/api/v1/jump');
    expect(r.calls[0].init.method).toBe('POST');
    expect(JSON.parse(r.calls[0].init.body as string)).toEqual({ cardId: 'claude:1' });
  });
  it('seen posts to the seen path', async () => {
    const r = recorder(204);
    await r.actions.seen('claude:1');
    expect(r.calls[0].url).toBe('/api/v1/seen');
  });
  it('a 409 from jump means no terminal', async () => {
    expect(await recorder(409).actions.jump('x')).toEqual({ ok: false, kind: 'no_terminal' });
  });
  it('a 401 means the key is lost', async () => {
    expect(await recorder(401).actions.seen('x')).toEqual({ ok: false, kind: 'unauthorized' });
  });
  it('a server error or no answer says what failed', async () => {
    expect(await recorder(500).actions.jump('x')).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard answered 500' });
    expect(await recorder('down').actions.jump('x')).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard did not answer' });
  });
});
