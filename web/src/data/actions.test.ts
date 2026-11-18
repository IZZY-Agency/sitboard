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

describe('forget grouping', () => {
  it('posts the project id only', async () => {
    const r = recorder(204);
    expect(await r.actions.forgetGrouping('p_0123456789ab')).toEqual({ ok: true });
    expect(r.calls[0].url).toBe('/api/v1/grouping/forget');
    expect(r.calls[0].init.method).toBe('POST');
    expect(JSON.parse(r.calls[0].init.body as string)).toEqual({ projectId: 'p_0123456789ab' });
  });
  it('a 409 means it is no longer grouped, a 404 that the project is gone: both final, no retry', async () => {
    expect(await recorder(409).actions.forgetGrouping('x')).toEqual({ ok: false, kind: 'failed', message: 'it is no longer grouped', final: true });
    expect(await recorder(404).actions.forgetGrouping('x')).toEqual({ ok: false, kind: 'failed', message: 'this project is no longer on the board', final: true });
  });
  it('a 403 means the key is lost; a 500 is a state file sitboard could not write, worth a retry', async () => {
    expect(await recorder(403).actions.forgetGrouping('x')).toEqual({ ok: false, kind: 'unauthorized' });
    expect(await recorder(500).actions.forgetGrouping('x')).toEqual({ ok: false, kind: 'failed', message: 'sitboard could not save it' });
  });
  it('the other actions keep their answers', async () => {
    expect(await recorder(409).actions.jump('x')).toEqual({ ok: false, kind: 'no_terminal' });
    expect(await recorder(404).actions.seen('x')).toEqual({ ok: false, kind: 'failed', message: 'this card is no longer on the board' });
    expect(await recorder(500).actions.seen('x')).toEqual({ ok: false, kind: 'failed', message: 'sitboard answered 500' });
  });
});
