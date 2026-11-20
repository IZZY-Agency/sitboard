import { describe, expect, it } from 'vitest';
import type { CatchupItem } from '../contract.gen';
import { createCatchupClient, groupCatchup, readCatchup, rowLink, rowWord, type CatchupClient, type CatchupLoad, type ReadResult } from './catchup';
import { CatchupState } from './catchup.svelte';

function respond(code: number | 'down', body?: unknown) {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const fetchFn = ((url: string, init: RequestInit) => {
    calls.push({ url, init });
    if (code === 'down') return Promise.reject(new TypeError('network'));
    return Promise.resolve(new Response(body === undefined ? null : JSON.stringify(body), { status: code }));
  }) as unknown as typeof fetch;
  return { calls, client: createCatchupClient(fetchFn) };
}

const turn = (at: string, agent: string, project = 'p_aaaaaaaaaaaa', cardId = `claude:${agent}`): CatchupItem => ({
  at,
  kind: 'turn_finished',
  cardId,
  projectId: project,
  projectName: project === 'p_aaaaaaaaaaaa' ? 'harbor' : 'lumen',
  agentName: agent,
  title: `${agent} title`,
});

const pr = (at: string, kind: 'pr_opened' | 'pr_merged', n: number, project = 'p_aaaaaaaaaaaa'): CatchupItem => ({
  at,
  kind,
  cardId: '',
  projectId: project,
  projectName: 'harbor',
  agentName: 'worker-c',
  title: `Placeholder ${n}`,
  pullRequest: {
    repo: 'example-org/example-repo',
    number: n,
    title: `Placeholder ${n}`,
    url: `https://github.com/example-org/example-repo/pull/${n}`,
    state: kind === 'pr_merged' ? 'merged' : 'open',
    checks: 'passing',
    review: '',
    draft: false,
  },
});

describe('catch-up client', () => {
  it('loads the list without moving the marker', async () => {
    const r = respond(200, { marker: '2026-11-17T08:00:00Z', asOf: '2026-11-17T10:00:00Z', total: 1, items: [turn('2026-11-17T09:00:00Z', 'a')] });
    const got = await r.client.load();
    expect(got).toEqual({ ok: true, catchup: { marker: '2026-11-17T08:00:00Z', asOf: '2026-11-17T10:00:00Z', total: 1, items: [turn('2026-11-17T09:00:00Z', 'a')] } });
    expect(r.calls).toHaveLength(1);
    expect(r.calls[0].url).toBe('/api/v1/catchup');
    expect(r.calls[0].init.method ?? 'GET').toBe('GET');
  });
  it('marks read up to the time it is given, as JSON', async () => {
    const r = respond(204);
    expect(await r.client.markRead('2026-11-17T09:55:00Z')).toEqual({ ok: true });
    expect(r.calls[0].url).toBe('/api/v1/catchup/read');
    expect(r.calls[0].init.method).toBe('POST');
    expect(new Headers(r.calls[0].init.headers).get('Content-Type')).toBe('application/json');
    expect(JSON.parse(r.calls[0].init.body as string)).toEqual({ upTo: '2026-11-17T09:55:00Z' });
  });
  it('says why it failed', async () => {
    expect(await respond(401).client.load()).toEqual({ ok: false, kind: 'unauthorized' });
    expect(await respond(403).client.markRead('2026-11-17T09:55:00Z')).toEqual({ ok: false, kind: 'unauthorized' });
    expect(await respond('down').client.load()).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard did not answer' });
    expect(await respond(500).client.markRead('2026-11-17T09:55:00Z')).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard could not save it' });
    expect(await respond(502).client.load()).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard answered 502' });
    expect(await respond(200, { nope: 1 }).client.load()).toMatchObject({ ok: false, kind: 'failed' });
  });
  it('leaves out items it cannot show', () => {
    const good = turn('2026-11-17T09:00:00Z', 'a');
    const c = readCatchup({
      marker: 'yesterday',
      asOf: '2026-11-17T10:00:00Z',
      total: 'many',
      items: [good, { ...good, kind: 'checks_failed' }, { ...good, at: 'soon' }, null, { ...good, projectId: 3 }],
    });
    expect(c).toEqual({ asOf: '2026-11-17T10:00:00Z', total: 1, items: [good] });
  });
  it('refuses a list it could not mark read, and keeps a total past the cap', () => {
    const good = turn('2026-11-17T09:00:00Z', 'a');
    expect(readCatchup({ items: [good] })).toBeNull();
    expect(readCatchup({ asOf: 'soon', items: [good] })).toBeNull();
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', total: 1234, items: [good] })).toEqual({ asOf: '2026-11-17T10:00:00Z', total: 1234, items: [good] });
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', total: 0, items: [good] })!.total).toBe(1);
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', unsaved: true, items: [good] })!.unsaved).toBe(true);
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', unsaved: 'yes', items: [good] })).not.toHaveProperty('unsaved');
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', pending: true, items: [good] })!.pending).toBe(true);
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', pending: 1, items: [good] })).not.toHaveProperty('pending');
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', total: 2.5, items: [good] })!.total).toBe(1);
  });
});

describe('grouping', () => {
  it('folds one agent’s turns and keeps each pull request', () => {
    const groups = groupCatchup([
      turn('2026-11-17T09:50:00Z', 'lead'),
      pr('2026-11-17T09:40:00Z', 'pr_merged', 2),
      turn('2026-11-17T09:30:00Z', 'lead'),
      pr('2026-11-17T09:20:00Z', 'pr_opened', 2),
      turn('2026-11-17T09:10:00Z', 'lead'),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].total).toBe(5);
    expect(groups[0].rows.map((r) => `${r.kind}:${r.count}@${r.at.slice(11, 16)}`)).toEqual([
      'turn_finished:3@09:50',
      'pr_merged:1@09:40',
      'pr_opened:1@09:20',
    ]);
  });
  it('puts the project with the newest item first', () => {
    const groups = groupCatchup([
      turn('2026-11-17T09:00:00Z', 'a', 'p_bbbbbbbbbbbb'),
      turn('2026-11-17T08:00:00Z', 'b', 'p_aaaaaaaaaaaa'),
      turn('2026-11-17T07:00:00Z', 'c', 'p_bbbbbbbbbbbb'),
      turn('2026-11-17T09:30:00Z', 'd', 'p_aaaaaaaaaaaa'),
    ]);
    expect(groups.map((g) => `${g.projectId}:${g.name}`)).toEqual(['p_aaaaaaaaaaaa:harbor', 'p_bbbbbbbbbbbb:lumen']);
    expect(groups[1].rows.map((r) => r.agentName)).toEqual(['a', 'c']);
  });
  it('keeps two agents with no card apart by name', () => {
    const groups = groupCatchup([turn('2026-11-17T09:00:00Z', 'x', 'p_aaaaaaaaaaaa', ''), turn('2026-11-17T08:00:00Z', 'y', 'p_aaaaaaaaaaaa', '')]);
    expect(groups[0].rows).toHaveLength(2);
  });
  it('a folded row shows the newest turn, in whatever order items come', () => {
    const groups = groupCatchup([turn('2026-11-17T08:00:00Z', 'lead'), turn('2026-11-17T09:00:00Z', 'lead')]);
    expect(groups[0].rows[0]).toMatchObject({ count: 2, at: '2026-11-17T09:00:00Z' });
  });
  it('gives each artifact its own row, with its link', () => {
    const a = (at: string, n: number): CatchupItem => ({
      ...turn(at, 'lead'),
      kind: 'artifact_published',
      title: `Plan ${n}`,
      artifact: { title: `Plan ${n}`, url: `https://claude.ai/artifact/exampleArtifact000${n}` },
    });
    const rows = groupCatchup([a('2026-11-17T09:00:00Z', 1), a('2026-11-17T08:00:00Z', 2)])[0].rows;
    expect(rows.map((r) => [r.kind, r.count, r.artifact?.url])).toEqual([
      ['artifact_published', 1, 'https://claude.ai/artifact/exampleArtifact0001'],
      ['artifact_published', 1, 'https://claude.ai/artifact/exampleArtifact0002'],
    ]);
  });
  it('is empty for no items', () => {
    expect(groupCatchup([])).toEqual([]);
  });
});

function fakeClient(loads: CatchupLoad[], reads: ReadResult[] = []) {
  const calls: string[] = [];
  const pending: Array<(r: CatchupLoad) => void> = [];
  let manual = false;
  const client: CatchupClient = {
    load() {
      calls.push('load');
      if (manual) return new Promise((res) => pending.push(res));
      return Promise.resolve(loads.shift() ?? { ok: false, kind: 'failed', message: 'none left' });
    },
    markRead(upTo: string) {
      calls.push(`read ${upTo}`);
      return Promise.resolve(reads.shift() ?? { ok: true });
    },
  };
  return {
    calls,
    client,
    hold() {
      manual = true;
    },
    pending,
  };
}

const list = (n: number): CatchupLoad => ({
  ok: true,
  catchup: { asOf: `2026-11-17T10:00:0${n % 10}Z`, total: n, items: Array.from({ length: n }, (_, i) => turn(`2026-11-17T09:${String(i).padStart(2, '0')}:00Z`, `a${i}`)) },
});

describe('catch-up state', () => {
  it('loads on start and counts the items', async () => {
    const f = fakeClient([list(3)]);
    const s = new CatchupState(() => f.client);
    expect(s.status).toBe('loading');
    const stop = s.start(1e9);
    await Promise.resolve();
    await Promise.resolve();
    expect(s.status).toBe('ready');
    expect(s.count).toBe(3);
    expect(f.calls).toEqual(['load']);
    stop();
  });
  it('keeps the last list when a later load fails', async () => {
    const f = fakeClient([list(2), { ok: false, kind: 'failed', message: 'sitboard did not answer' }]);
    const s = new CatchupState(() => f.client);
    await s.refresh();
    await s.refresh();
    expect(s.status).toBe('failed');
    expect(s.loadError).toBe('sitboard did not answer');
    expect(s.count).toBe(2);
  });
  it('marks read only when asked, then loads again', async () => {
    const f = fakeClient([list(2), list(0)]);
    const s = new CatchupState(() => f.client);
    await s.refresh();
    expect(f.calls).toEqual(['load']);
    expect(await s.markRead()).toBe(true);
    expect(f.calls).toEqual(['load', 'read 2026-11-17T10:00:02Z', 'load']);
    expect(s.count).toBe(0);
  });
  it('keeps the list when marking read fails', async () => {
    const f = fakeClient([list(2)], [{ ok: false, kind: 'failed', message: 'sitboard could not save it' }]);
    const s = new CatchupState(() => f.client);
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(s.markError).toBe('sitboard could not save it');
    expect(s.count).toBe(2);
    expect(f.calls).toEqual(['load', 'read 2026-11-17T10:00:02Z']);
  });
  it('with nothing shown there is nothing to mark', async () => {
    const f = fakeClient([list(0)]);
    const s = new CatchupState(() => f.client);
    expect(await s.markRead()).toBe(false);
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(f.calls).toEqual(['load']);
  });
  it('a lost key stops it', async () => {
    const f = fakeClient([{ ok: false, kind: 'unauthorized' }]);
    const s = new CatchupState(() => f.client);
    await s.refresh();
    expect(s.status).toBe('unauthorized');
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(f.calls).toEqual(['load']);
  });
  it('keeps only the answer to the latest load', async () => {
    const f = fakeClient([]);
    f.hold();
    const s = new CatchupState(() => f.client);
    const first = s.refresh();
    const second = s.refresh();
    f.pending[1](list(1));
    await second;
    f.pending[0](list(5));
    await first;
    expect(s.count).toBe(1);
  });
});

describe('row words and links', () => {
  const base = groupCatchup([turn('2026-11-17T09:00:00Z', 'lead')])[0].rows[0];
  it('says the kind in words', () => {
    expect(rowWord(base)).toBe('Turn done');
    expect(rowWord({ ...base, count: 3 })).toBe('3 turns');
    expect(rowWord({ ...base, kind: 'pr_opened' })).toBe('PR opened');
    expect(rowWord({ ...base, kind: 'pr_merged' })).toBe('Merged');
    expect(rowWord({ ...base, kind: 'artifact_published' })).toBe('Artifact');
  });
  it('leads to the pull request, the artifact, or the card while it is on the board', () => {
    const p = groupCatchup([pr('2026-11-17T09:00:00Z', 'pr_merged', 5)])[0].rows[0];
    expect(rowLink(p, new Set())).toEqual({ kind: 'url', href: 'https://github.com/example-org/example-repo/pull/5', text: '#5' });
    const art = { ...base, kind: 'artifact_published' as const, artifact: { title: 't', url: 'https://claude.ai/artifact/exampleArtifact0001' } };
    expect(rowLink(art, new Set())).toEqual({ kind: 'url', href: 'https://claude.ai/artifact/exampleArtifact0001', text: 'Open' });
    expect(rowLink(base, new Set([base.cardId]))).toEqual({ kind: 'card', cardId: base.cardId });
    expect(rowLink(base, new Set())).toEqual({ kind: 'none' });
  });
  it('never links an address that is not https', () => {
    const p = groupCatchup([pr('2026-11-17T09:00:00Z', 'pr_merged', 5)])[0].rows[0];
    const bad = { ...p, pullRequest: { ...p.pullRequest!, url: 'javascript:alert(1)' }, cardId: '' };
    expect(rowLink(bad, new Set())).toEqual({ kind: 'none' });
  });
});
