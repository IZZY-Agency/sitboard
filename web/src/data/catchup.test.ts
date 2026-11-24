import { describe, expect, it } from 'vitest';
import type { Catchup, CatchupItem } from '../contract.gen';
import { createCatchupClient, groupCatchup, readCatchup, rowLink, rowWord, type CatchupClient, type CatchupLoad, type ReadResult } from './catchup';
import { CatchupState, FULL, STALE, STALE_RELOADED } from './catchup.svelte';

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
    forge: 'github' as const, host: 'github.com', repo: 'example-org/example-repo',
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
    const r = respond(200, { marker: '2026-11-17T08:00:00Z', list: 'r-1', asOf: '2026-11-17T10:00:00Z', total: 1, items: [turn('2026-11-17T09:00:00Z', 'a')] });
    const got = await r.client.load();
    expect(got).toEqual({ ok: true, catchup: { marker: '2026-11-17T08:00:00Z', list: 'r-1', asOf: '2026-11-17T10:00:00Z', total: 1, items: [turn('2026-11-17T09:00:00Z', 'a')] } });
    expect(r.calls).toHaveLength(1);
    expect(r.calls[0].url).toBe('/api/v1/catchup');
    expect(r.calls[0].init.method ?? 'GET').toBe('GET');
  });
  it('marks read the list it is given, as JSON', async () => {
    const r = respond(204);
    expect(await r.client.markRead('r-7')).toEqual({ ok: true });
    expect(r.calls[0].url).toBe('/api/v1/catchup/read');
    expect(r.calls[0].init.method).toBe('POST');
    expect(new Headers(r.calls[0].init.headers).get('Content-Type')).toBe('application/json');
    expect(JSON.parse(r.calls[0].init.body as string)).toEqual({ list: 'r-7' });
  });
  it('says why it failed', async () => {
    expect(await respond(401).client.load()).toEqual({ ok: false, kind: 'unauthorized' });
    expect(await respond(403).client.markRead('r-1')).toEqual({ ok: false, kind: 'unauthorized' });
    expect(await respond(409).client.markRead('r-1')).toEqual({ ok: false, kind: 'stale' });
    expect(await respond(507).client.markRead('r-1')).toEqual({ ok: false, kind: 'full' });
    expect(await respond('down').client.load()).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard did not answer' });
    expect(await respond(500).client.markRead('r-1')).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard could not save it' });
    expect(await respond(502).client.load()).toMatchObject({ ok: false, kind: 'failed', message: 'sitboard answered 502' });
    expect(await respond(200, { nope: 1 }).client.load()).toMatchObject({ ok: false, kind: 'failed' });
  });
  it('leaves out items it cannot show', () => {
    const good = turn('2026-11-17T09:00:00Z', 'a');
    const c = readCatchup({
      marker: 'yesterday',
      list: 'r-1',
      asOf: '2026-11-17T10:00:00Z',
      total: 'many',
      items: [good, { ...good, kind: 'checks_failed' }, { ...good, at: 'soon' }, null, { ...good, projectId: 3 }],
    });
    // Not the list the server sent: it has no id, so it cannot be marked read (round 6, finding 44).
    expect(c).toEqual({ list: '', asOf: '2026-11-17T10:00:00Z', total: 1, items: [good] });
  });
  it('refuses a list it could not mark read, and keeps a total past the cap', () => {
    const good = turn('2026-11-17T09:00:00Z', 'a');
    expect(readCatchup({ items: [good] })).toBeNull();
    expect(readCatchup({ list: 'r-1', asOf: 'soon', items: [good] })).toBeNull();
    expect(readCatchup({ asOf: '2026-11-17T10:00:00Z', items: [good] })).toBeNull();
    expect(readCatchup({ list: '', asOf: '2026-11-17T10:00:00Z', items: [good] })).toBeNull();
    expect(readCatchup({ list: 3, asOf: '2026-11-17T10:00:00Z', items: [good] })).toBeNull();
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', total: 1234, items: [good] })).toEqual({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', total: 1234, items: [good] });
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', total: 0, items: [good] })!.total).toBe(1);
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', unsaved: true, items: [good] })!.unsaved).toBe(true);
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', unsaved: 'yes', items: [good] })).not.toHaveProperty('unsaved');
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', pending: true, items: [good] })!.pending).toBe(true);
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', pending: 1, items: [good] })).not.toHaveProperty('pending');
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', partial: true, items: [good] })!.partial).toBe(true);
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', partial: 'yes', items: [good] })).not.toHaveProperty('partial');
    expect(readCatchup({ list: 'r-1', asOf: '2026-11-17T10:00:00Z', total: 2.5, items: [good] })!.total).toBe(1);
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
    // The second publish of artifact 1 is an item of its own: its own row (round 6, finding 37).
    const rows = groupCatchup([a('2026-11-17T09:00:00Z', 1), a('2026-11-17T08:00:00Z', 2), a('2026-11-17T07:00:00Z', 1)])[0].rows;
    expect(rows.map((r) => [r.kind, r.count, r.artifact?.url, r.at])).toEqual([
      ['artifact_published', 1, 'https://claude.ai/artifact/exampleArtifact0001', '2026-11-17T09:00:00Z'],
      ['artifact_published', 1, 'https://claude.ai/artifact/exampleArtifact0002', '2026-11-17T08:00:00Z'],
      ['artifact_published', 1, 'https://claude.ai/artifact/exampleArtifact0001', '2026-11-17T07:00:00Z'],
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
    markRead(id: string) {
      calls.push(`read ${id}`);
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
  catchup: { list: `r-${n}`, asOf: `2026-11-17T10:00:0${n % 10}Z`, total: n, items: Array.from({ length: n }, (_, i) => turn(`2026-11-17T09:${String(i).padStart(2, '0')}:00Z`, `a${i}`)) },
});

/** The state after the first board snapshot, which listed no GitLab host. */
function shown(s: CatchupState): CatchupState {
  s.setGitlabHosts([]);
  return s;
}

describe('catch-up state', () => {
  it('loads on start and counts the items', async () => {
    const f = fakeClient([list(3)]);
    const s = shown(new CatchupState(() => f.client));
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
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    await s.refresh();
    expect(s.status).toBe('failed');
    expect(s.loadError).toBe('sitboard did not answer');
    expect(s.count).toBe(2);
  });
  it('marks read only when asked, then loads again', async () => {
    const f = fakeClient([list(2), list(0)]);
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    expect(f.calls).toEqual(['load']);
    expect(await s.markRead()).toBe(true);
    expect(f.calls).toEqual(['load', 'read r-2', 'load']);
    expect(s.count).toBe(0);
  });
  it('keeps the list when marking read fails', async () => {
    const f = fakeClient([list(2)], [{ ok: false, kind: 'failed', message: 'sitboard could not save it' }]);
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(s.markError).toBe('sitboard could not save it');
    expect(s.count).toBe(2);
    expect(f.calls).toEqual(['load', 'read r-2']);
  });
  it('a list that is not current marks nothing: it says so and loads the list again', async () => {
    const f = fakeClient([list(2), list(3)], [{ ok: false, kind: 'stale' }]);
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(s.markError).toBe(STALE + STALE_RELOADED);
    expect(s.afterMark).toBe(false);
    expect(s.canMark).toBe(true);
    expect(s.count).toBe(3);
    expect(s.data?.list).toBe('r-3');
    expect(f.calls).toEqual(['load', 'read r-2', 'load']);
    // Pressed again, on the fresh list: taken, and the message goes.
    f.calls.length = 0;
    expect(await s.markRead()).toBe(true); // taken; no loads are left, so the reload fails
    expect(f.calls).toEqual(['read r-3', 'load']);
    expect(s.markError).toBe('');
    expect(s.afterMark).toBe(true);
  });
  it('a list that is not current, then a failed reload: the old list stays shown and cannot be marked again', async () => {
    const f = fakeClient([list(2), { ok: false, kind: 'failed', message: 'sitboard did not answer' }], [{ ok: false, kind: 'stale' }]);
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(s.markError).toBe(STALE); // never "this is the list as it is now"
    expect(s.status).toBe('failed');
    expect(s.count).toBe(2);
    expect(s.canMark).toBe(false);
    expect(await s.markRead()).toBe(false);
    expect(f.calls).toEqual(['load', 'read r-2', 'load']); // the refused id is not sent again
  });
  it('a list the page took a row out of cannot be marked until the next list loads', async () => {
    const mr: CatchupItem = { ...pr('2026-11-17T09:30:00Z', 'pr_opened', 4), pullRequest: { ...pr('2026-11-17T09:30:00Z', 'pr_opened', 4).pullRequest!, forge: 'gitlab', host: 'gitlab.example.com' } };
    const withMR: CatchupLoad = { ok: true, catchup: { list: 'r-9', asOf: '2026-11-17T10:00:00Z', total: 2, items: [turn('2026-11-17T09:40:00Z', 'a'), mr] } };
    const f = fakeClient([withMR, { ok: false, kind: 'failed', message: 'sitboard did not answer' }]);
    const s = new CatchupState(() => f.client);
    s.setGitlabHosts(['gitlab.example.com']);
    await s.refresh();
    expect(s.canMark).toBe(true);
    s.setGitlabHosts([]); // logged out of the host: its row goes, a reload is asked for (and fails)
    await Promise.resolve();
    await Promise.resolve();
    expect(s.count).toBe(1);
    expect(s.canMark).toBe(false);
    expect(await s.markRead()).toBe(false);
    expect(f.calls.filter((c) => c.startsWith('read'))).toEqual([]);
  });
  it('before the first snapshot nothing can be marked: the panel may hide a merge request (round 5, finding 33)', async () => {
    const mr: CatchupItem = { ...pr('2026-11-17T09:30:00Z', 'pr_opened', 4), pullRequest: { ...pr('2026-11-17T09:30:00Z', 'pr_opened', 4).pullRequest!, forge: 'gitlab', host: 'gitlab.example.com' } };
    const f = fakeClient([{ ok: true, catchup: { list: 'r-9', asOf: '2026-11-17T10:00:00Z', total: 2, items: [turn('2026-11-17T09:40:00Z', 'a'), mr] } }]);
    const s = new CatchupState(() => f.client);
    await s.refresh();
    expect(s.count).toBe(2);
    expect(s.canMark).toBe(false);
    expect(await s.markRead()).toBe(false);
    expect(f.calls).toEqual(['load']);
    s.setGitlabHosts(['gitlab.example.com']); // the first snapshot: the host is known, its row shows
    expect(s.canMark).toBe(true);
  });
  it('a list marked read keeps what it said of the read when the next load fails (round 5, finding 35)', async () => {
    const partial: CatchupLoad = { ok: true, catchup: { ...(list(2) as { catchup: Catchup }).catchup, partial: true, pending: true, unsaved: true } };
    const f = fakeClient([partial]);
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    expect(await s.markRead()).toBe(true); // taken; the reload fails
    expect(s.status).toBe('failed');
    expect(s.count).toBe(0);
    expect(s.data).toMatchObject({ partial: true, pending: true, unsaved: true });
  });
  it('at the cap nothing is marked: it says so and loads the list again', async () => {
    const f = fakeClient([list(2), list(2)], [{ ok: false, kind: 'full' }]);
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(s.markError).toBe(FULL);
    expect(s.afterMark).toBe(false);
    expect(s.count).toBe(2);
    expect(f.calls).toEqual(['load', 'read r-2', 'load']);
  });
  it('with nothing shown there is nothing to mark', async () => {
    const f = fakeClient([list(0)]);
    const s = shown(new CatchupState(() => f.client));
    expect(await s.markRead()).toBe(false);
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(f.calls).toEqual(['load']);
  });
  it('a lost key stops it', async () => {
    const f = fakeClient([{ ok: false, kind: 'unauthorized' }]);
    const s = shown(new CatchupState(() => f.client));
    await s.refresh();
    expect(s.status).toBe('unauthorized');
    await s.refresh();
    expect(await s.markRead()).toBe(false);
    expect(f.calls).toEqual(['load']);
  });
  it('keeps only the answer to the latest load', async () => {
    const f = fakeClient([]);
    f.hold();
    const s = shown(new CatchupState(() => f.client));
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
