import { describe, expect, it } from 'vitest';
import type { Issues, OpenIssue, Project, RepoIssues } from '../contract.gen';
import { project } from '../test/fixtures';
import { byMilestone, createIssuesClient, issuesShown, openIssueHref, openIssuesOf, openIssuesText, readIssues, repoIssuesHref, sectionsFor, type IssuesLoad } from './issues';
import { IssuesState } from './issues.svelte';

const REPO = 'example-org/example-repo';
const HOST = 'gitlab.example.com';
const GL = 'example-group/example-project';

const gh = (p: Project, open: number | null, repo = REPO): Project => ({
  ...p,
  repo,
  repoForge: 'github',
  repoHost: 'github.com',
  issuesKey: repo.toLowerCase(), // as the server serves it
  openIssues: open === null ? { known: false, open: 0 } : { known: true, open },
});

const issue = (n: number, milestone = '', due?: string): OpenIssue => ({
  number: n,
  title: `Sample ${n}`,
  url: `https://github.com/${REPO}/issues/${n}`,
  milestone,
  labels: [],
  agents: [],
  pinned: false,
  ...(due ? { milestoneDue: due } : {}),
});

const repo = (over: Partial<RepoIssues> = {}): RepoIssues => {
  const r = { forge: 'github' as const, host: 'github.com', repo: REPO, state: 'ok' as const, open: 1, issues: [issue(1)], pinned: [], ...over };
  return { ...r, key: over.key ?? r.repo.toLowerCase() }; // as the server serves it
};

describe('readIssues', () => {
  it('keeps what the page can show and drops the rest', () => {
    const l = readIssues({
      state: 'ready',
      asOf: '2026-11-17T10:00:00Z',
      repos: [
        { forge: 'github', host: 'github.com', repo: REPO, state: 'ok', open: 1, issues: [{ number: 4, title: 't', url: 'u', milestone: 'm', labels: ['a', 3, ' ', 'b'], updatedAt: 'nonsense' }, { number: 0 }] },
        { forge: 'nope', repo: REPO, state: 'ok' },
        { forge: 'github', repo: REPO, state: 'weird' },
        { forge: '', repo: REPO, state: 'ok' },
        { forge: 'github', repo: '', state: 'ok' },
      ],
    });
    expect(l?.repos.length).toBe(1);
    const r = l!.repos[0];
    expect(r.issues.map((i) => i.number)).toEqual([4]);
    expect(r.issues[0].labels).toEqual(['a', 'b']);
    expect(r.issues[0].updatedAt).toBeUndefined();
    expect(l!.asOf).toBe('2026-11-17T10:00:00Z');
  });

  it('counts at least the listed issues as open', () => {
    const l = readIssues({ state: 'ready', repos: [{ forge: 'github', repo: REPO, state: 'ok', open: 0, issues: [{ number: 1 }, { number: 2 }] }] });
    expect(l!.repos[0].open).toBe(2);
  });

  it('refuses a state it does not know', () => {
    expect(readIssues({ state: 'later', repos: [] })).toBeNull();
    expect(readIssues([])).toBeNull();
  });
});

describe('createIssuesClient', () => {
  const answer = (status: number, body: unknown) => async () => new Response(JSON.stringify(body), { status });
  it('loads the list', async () => {
    const r = await createIssuesClient(answer(200, { state: 'off', repos: [] }) as typeof fetch).load();
    expect(r).toEqual({ ok: true, issues: { state: 'off', repos: [], rev: 0, freshMs: 0, unsaved: false } });
    const full = await createIssuesClient(answer(200, { state: 'ready', repos: [], rev: 7, freshMs: 41_500 }) as typeof fetch).load();
    expect(full).toEqual({ ok: true, issues: { state: 'ready', repos: [], rev: 7, freshMs: 41_500, unsaved: false } });
    const bad = await createIssuesClient(answer(200, { state: 'ready', repos: [], rev: -1, freshMs: 'soon' }) as typeof fetch).load();
    expect(bad).toEqual({ ok: true, issues: { state: 'ready', repos: [], rev: 0, freshMs: 0, unsaved: false } });
  });
  it('tells a lost key from a failure', async () => {
    expect(await createIssuesClient(answer(401, {}) as typeof fetch).load()).toEqual({ ok: false, kind: 'unauthorized' });
    expect(await createIssuesClient(answer(500, {}) as typeof fetch).load()).toEqual({ ok: false, kind: 'failed', message: 'sitboard answered 500' });
    expect(await createIssuesClient(answer(200, { state: 'x' }) as typeof fetch).load()).toEqual({ ok: false, kind: 'failed', message: 'sitboard sent something unreadable' });
  });
});

describe('counts', () => {
  it('shows the chip unless issues are off', () => {
    expect(issuesShown(undefined)).toBe(false);
    expect(issuesShown({ state: 'off', open: 0, rev: 0, noAgent: 0 })).toBe(false);
    expect(issuesShown({ state: 'loading', open: 0, rev: 0, noAgent: 0 })).toBe(true);
    expect(issuesShown({ state: 'ready', open: 0, rev: 0, noAgent: 0 })).toBe(true);
  });

  it('counts each repository once and only known counts', () => {
    const a = gh(project('p_a', 'a'), 12);
    const b = gh(project('p_b', 'b'), 12); // the same repository
    const c = gh(project('p_c', 'c'), 3, 'example-org/other-repo');
    const d = gh(project('p_d', 'd'), null, 'example-org/third-repo');
    expect(openIssuesOf([a, b, c, d])).toBe(15);
    expect(openIssuesOf([d, project('p_e', 'e')])).toBeNull();
    expect(openIssuesOf([gh(project('p_f', 'f'), 0)])).toBe(0);
  });

  it('says the count in words', () => {
    expect(openIssuesText(0)).toBe('no open issues');
    expect(openIssuesText(1)).toBe('1 open issue');
    expect(openIssuesText(12)).toBe('12 open issues');
  });
});

describe('sectionsFor', () => {
  it('lists the repositories of the projects in their order, each once', () => {
    const other = repo({ repo: 'example-org/other-repo' });
    const list: Issues = { state: 'ready', unsaved: false, rev: 1, freshMs: 60_000, repos: [repo(), other] };
    const ps = [gh(project('p_b', 'beta'), 1, 'example-org/other-repo'), gh(project('p_a', 'alpha'), 1), gh(project('p_c', 'alpha-copy'), 1), project('p_d', 'none')];
    expect(sectionsFor(list, ps).map((s) => s.name)).toEqual(['beta', 'alpha']);
    expect(sectionsFor(null, ps)).toEqual([]);
    expect(sectionsFor(list, [gh(project('p_x', 'x'), 1, 'example-org/unlisted')])).toEqual([]);
  });

  // Review round 5, P3: the page folds nothing itself. GitLab under the
  // mount /Tools and under /tools are two projects (the server's keys
  // differ), counted and listed apart.
  it('keeps two GitLab mounts that differ only in case apart, by the server key', () => {
    const gl = (p: Project, path: string, open: number): Project => ({
      ...p,
      repo: path,
      repoForge: 'gitlab',
      repoHost: HOST,
      issuesKey: `gitlab:${HOST}/${path}`,
      openIssues: { known: true, open },
    });
    const upper = gl(project('p_u', 'upper'), 'Tools/group/app', 2);
    const lower = gl(project('p_l', 'lower'), 'tools/group/app', 3);
    expect(openIssuesOf([upper, lower])).toBe(5);
    const list: Issues = {
      state: 'ready',
      unsaved: false,
      rev: 1,
      freshMs: 60_000,
      repos: [
        repo({ forge: 'gitlab', host: HOST, repo: 'Tools/group/app', key: upper.issuesKey }),
        repo({ forge: 'gitlab', host: HOST, repo: 'tools/group/app', key: lower.issuesKey }),
      ],
    };
    expect(sectionsFor(list, [upper, lower]).map((s) => [s.name, s.repo.repo])).toEqual([
      ['upper', 'Tools/group/app'],
      ['lower', 'tools/group/app'],
    ]);
  });
});

describe('byMilestone', () => {
  it('puts due milestones first, soonest first, then by title, then no milestone', () => {
    const gs = byMilestone([
      issue(1),
      issue(2, 'Later'),
      issue(3, '0.6.0', '2026-11-20T00:00:00Z'),
      issue(4, '0.5.0', '2026-12-01T00:00:00Z'),
      issue(5, 'Backlog'),
      issue(6, '0.5.0', '2026-12-01T00:00:00Z'),
    ]);
    expect(gs.map((g) => g.milestone)).toEqual(['0.6.0', '0.5.0', 'Backlog', 'Later', '']);
    expect(gs[1].issues.map((i) => i.number)).toEqual([4, 6]);
  });
});

describe('byMilestone, order of titles', () => {
  it('reads the numbers in titles as numbers', () => {
    const gs = byMilestone([issue(1, 'Sprint 10'), issue(2, 'Sprint 2'), issue(3, 'sprint 1'), issue(4, 'Sprint 11'), issue(5)]);
    expect(gs.map((g) => g.milestone)).toEqual(['sprint 1', 'Sprint 2', 'Sprint 10', 'Sprint 11', '']);
  });

  it('keeps the due date first, then the numbers', () => {
    const gs = byMilestone([issue(1, 'Sprint 2'), issue(2, 'Sprint 10', '2026-11-01T00:00:00Z'), issue(3, 'Sprint 3', '2026-11-01T00:00:00Z'), issue(4, 'Sprint 1')]);
    expect(gs.map((g) => g.milestone)).toEqual(['Sprint 3', 'Sprint 10', 'Sprint 1', 'Sprint 2']);
  });

  it('gives a shared milestone the earliest due date of its issues, whatever comes first', () => {
    const gs = byMilestone([issue(1, 'B', '2026-12-20T00:00:00Z'), issue(2, 'A', '2026-12-10T00:00:00Z'), issue(3, 'B', '2026-12-01T00:00:00Z'), issue(4, 'B')]);
    expect(gs.map((g) => g.milestone)).toEqual(['B', 'A']);
    expect(gs[0].issues.map((i) => i.number)).toEqual([1, 3, 4]);
  });

  it('puts a milestone without any due date after those with one', () => {
    const gs = byMilestone([issue(1, 'A'), issue(2, 'Z', '2026-12-10T00:00:00Z'), issue(3, 'A', 'not a date')]);
    expect(gs.map((g) => g.milestone)).toEqual(['Z', 'A']);
  });
});

describe('links', () => {
  it('links an issue only to its own address', () => {
    expect(openIssueHref(repo(), issue(7), [])).toBe(`https://github.com/${REPO}/issues/7`);
    expect(openIssueHref(repo(), { ...issue(7), url: 'https://example.com/x' }, [])).toBeNull();
    expect(openIssueHref(repo(), { ...issue(7), url: `https://github.com/${REPO}/issues/8` }, [])).toBeNull();
  });

  it('links a GitLab issue only on a host the snapshot lists', () => {
    const r = repo({ forge: 'gitlab', host: HOST, repo: GL });
    const is = { ...issue(3), url: `https://${HOST}/${GL}/-/issues/3` };
    expect(openIssueHref(r, is, [HOST])).toBe(is.url);
    expect(openIssueHref(r, is, [])).toBeNull();
  });

  it('cuts the repository list from a checked issue address', () => {
    expect(repoIssuesHref(repo({ issues: [{ ...issue(7), url: 'bad' }, issue(9)] }), [])).toBe(`https://github.com/${REPO}/issues`);
    expect(repoIssuesHref(repo({ issues: [] }), [])).toBeNull();
  });
});

describe('IssuesState', () => {
  const LIST: Issues = { state: 'ready', unsaved: false, rev: 1, freshMs: 60_000, repos: [] };
  function deferred() {
    const waits: Array<(l: IssuesLoad) => void> = [];
    return {
      client: { load: () => new Promise<IssuesLoad>((res) => waits.push(res)) },
      answer: (i: number, l: IssuesLoad) => waits[i](l),
    };
  }

  it('shares a read that is out, and reads once more after it', async () => {
    const d = deferred();
    let loads = 0;
    const client = { load: () => (loads++, d.client.load()) };
    const s = new IssuesState(() => client);
    const first = s.refresh();
    const second = s.refresh();
    const third = s.refresh();
    expect(second).toBe(first);
    expect(third).toBe(first);
    expect(loads).toBe(1);
    d.answer(0, { ok: true, issues: LIST });
    await Promise.resolve();
    await Promise.resolve();
    expect(loads).toBe(2); // the snapshot announced something newer while the first was out
    d.answer(1, { ok: true, issues: { ...LIST, asOf: '2026-11-17T10:00:00Z' } });
    await first;
    expect(loads).toBe(2);
    expect(s.data?.asOf).toBe('2026-11-17T10:00:00Z');
  });

  it('reads no more after stop', async () => {
    const d = deferred();
    let loads = 0;
    const client = { load: () => (loads++, d.client.load()) };
    const s = new IssuesState(() => client);
    const p = s.refresh();
    void s.refresh();
    s.stop();
    d.answer(0, { ok: true, issues: LIST });
    await p;
    expect(loads).toBe(1);
  });

  it('drops the answer still out when another read lost the key', async () => {
    const d = deferred();
    const s = new IssuesState(() => d.client);
    const p = s.refresh();
    s.lose();
    d.answer(0, { ok: true, issues: LIST });
    await p;
    expect(s.data).toBeNull();
    expect(s.status).toBe('unauthorized');
  });

  it('keeps the last list when a load fails, and clears it on a lost key', async () => {
    const d = deferred();
    const s = new IssuesState(() => d.client);
    const a = s.refresh();
    d.answer(0, { ok: true, issues: LIST });
    await a;
    const b = s.refresh();
    d.answer(1, { ok: false, kind: 'failed', message: 'down' });
    await b;
    expect(s.status).toBe('failed');
    expect(s.data).toEqual(LIST);
    const c = s.refresh();
    d.answer(2, { ok: false, kind: 'unauthorized' });
    await c;
    expect(s.status).toBe('unauthorized');
    expect(s.data).toBeNull();
    await s.refresh(); // no more loads once the key is lost
    expect(s.status).toBe('unauthorized');
  });
});

describe('IssuesState, reuse', () => {
  const LIST: Issues = { state: 'ready', unsaved: false, rev: 1, freshMs: 60_000, repos: [] };
  function rig() {
    const waits: Array<(l: IssuesLoad) => void> = [];
    let t = 1_000_000;
    const s = new IssuesState(
      () => ({ load: () => new Promise<IssuesLoad>((res) => waits.push(res)) }),
      () => t,
    );
    return { s, waits, advance: (ms: number) => (t += ms) };
  }
  const ok: IssuesLoad = { ok: true, issues: LIST };
  /** The server's answer at rev, fresh for freshMs more on its clock. */
  const at = (rev: number, freshMs = 60_000): IssuesLoad => ({ ok: true, issues: { ...LIST, rev, freshMs } });
  const settleAll = async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  };

  it('shares a read that is out with an open at the same rev, and queues nothing', async () => {
    const { s, waits } = rig();
    const a = s.refresh(3);
    const b = s.refresh(3);
    const c = s.refresh(2);
    expect(b).toBe(a);
    expect(c).toBe(a);
    waits[0](ok);
    await a;
    await settleAll();
    expect(waits.length).toBe(1);
  });

  it('queues one more read only when the announced rev is newer than the read out', async () => {
    const { s, waits } = rig();
    const a = s.refresh(3);
    void s.refresh(4);
    void s.refresh(4);
    waits[0](at(3));
    await settleAll();
    expect(waits.length).toBe(2);
    void s.refresh(4); // the follow-up out reads rev 4 already: nothing more is queued
    waits[1](at(4));
    await a;
    await settleAll();
    expect(waits.length).toBe(2);
    // the follow-up was asked at rev 4: an open at 4 is fresh, at 5 is not
    void s.refresh(4);
    expect(waits.length).toBe(2);
    void s.refresh(5);
    expect(waits.length).toBe(3);
  });

  it('reuses a finished read for a minute at the same rev, and not after it', async () => {
    const { s, waits, advance } = rig();
    const a = s.refresh(3);
    waits[0](at(3));
    await a;
    advance(59_000);
    await s.refresh(3);
    expect(waits.length).toBe(1);
    advance(2_000); // 61 s: the server's one-minute gate must run
    const b = s.refresh(3);
    expect(waits.length).toBe(2);
    waits[1](at(3));
    await b;
  });

  // Rule: an open reuses a list only while the server would answer a GET
  // without a new look, as the server's clock counts it, not since the
  // browser got the list.
  it("reads again once the server's freshness ended, however recent the answer is here", async () => {
    const { s, waits, advance } = rig();
    const a = s.refresh(3);
    waits[0](at(3, 1_000)); // the server looked 59 s before this answer
    await a;
    advance(999);
    await s.refresh(3);
    expect(waits.length).toBe(1);
    advance(1); // 1 s after the answer: the server's gate (age >= a minute) is open
    const b = s.refresh(3);
    expect(waits.length).toBe(2);
    waits[1](at(3));
    await b;
  });

  it('counts the freshness from when the read was sent, not when the answer came', async () => {
    const { s, waits, advance } = rig();
    const a = s.refresh(3);
    advance(5_000); // the answer takes 5 s; the server had 10 s of freshness left when it answered
    waits[0](at(3, 10_000));
    await a;
    advance(4_999);
    await s.refresh(3);
    expect(waits.length).toBe(1);
    advance(1); // 10 s after the read was sent: past the server's deadline at the latest
    const b = s.refresh(3);
    expect(waits.length).toBe(2);
    waits[1](at(3));
    await b;
  });

  // Rule: a list counts for the rev the server built it at, whoever asked for it.
  it('marks a successful Try again with the rev it answered', async () => {
    const { s, waits } = rig();
    const a = s.refresh(1);
    waits[0](at(1));
    await a;
    const b = s.refresh(2);
    waits[1]({ ok: false, kind: 'failed', message: 'down' });
    await b;
    expect(s.status).toBe('failed');
    const c = s.refresh(); // Try again: the server has moved on to rev 3 meanwhile
    waits[2](at(3));
    await c;
    await s.refresh(2); // closed and opened again at once
    await s.refresh(3); // the snapshot that announces rev 3 comes after the list
    expect(waits.length).toBe(3);
    expect(s.status).toBe('ready');
  });

  // Rule: rows the server dropped (a purge) leave at once, and no answer the
  // server built before the purge is installed, even one that arrives later.
  it('drops the list on a purge and refuses a delayed answer from before it', async () => {
    const { s, waits } = rig();
    const title = (rev: number): IssuesLoad => ({
      ok: true,
      issues: { ...LIST, rev, repos: [{ forge: 'github', host: 'github.com', repo: 'example-org/example-repo', key: 'example-org/example-repo', state: 'ok', open: 1, issues: [{ number: 1, title: 'Sample secret', milestone: '', labels: [], agents: [], pinned: false, updatedAt: '2026-11-17T09:00:00Z', url: '' }], pinned: [] }] },
    });
    const a = s.refresh(1);
    waits[0](title(1));
    await a;
    expect(s.data?.repos.length).toBe(1);
    const b = s.refresh(2); // asked before the purge; the server answers late
    s.purge(3);
    expect(s.data).toBeNull();
    expect(s.status).toBe('loading');
    void s.refresh(3);
    waits[1](title(2));
    await settleAll();
    expect(s.data).toBeNull();
    expect(waits.length).toBe(3); // read again
    waits[2]({ ok: false, kind: 'failed', message: 'down' });
    await b;
    expect(s.data).toBeNull();
    expect(s.status).toBe('failed');
    s.purge(3); // the same purge again changes nothing
    const c = s.refresh(3);
    waits[3](title(3));
    await c;
    expect(s.data?.repos.length).toBe(1);
  });

  it('keeps a list built after the purge a late snapshot announces', async () => {
    const { s, waits } = rig();
    const a = s.refresh(4);
    waits[0](at(4));
    await a;
    s.purge(3);
    expect(s.data).not.toBeNull();
    expect(s.status).toBe('ready');
  });

  it('gives up after reads in a row that all predate the purge', async () => {
    const { s, waits } = rig();
    s.purge(5);
    const a = s.refresh(5);
    for (let i = 0; i < 3; i++) {
      await settleAll();
      waits[i](at(4));
    }
    await a;
    expect(waits.length).toBe(3);
    expect(s.data).toBeNull();
    expect(s.status).toBe('failed');
  });

  it('reads again when the rev is newer than the finished read', async () => {
    const { s, waits } = rig();
    const a = s.refresh(3);
    waits[0](ok);
    await a;
    const b = s.refresh(4);
    expect(waits.length).toBe(2);
    waits[1](ok);
    await b;
  });

  it('does not take a failed read for a fresh one, nor Try again', async () => {
    const { s, waits } = rig();
    const a = s.refresh(3);
    waits[0](at(3));
    await a;
    const b = s.refresh(); // Try again (or any read) that fails, with the list of rev 3 still on the page
    waits[1]({ ok: false, kind: 'failed', message: 'down' });
    await b;
    expect(s.status).toBe('failed');
    const c = s.refresh(3); // the same rev, well inside the minute: the failure is not a fresh answer
    expect(waits.length).toBe(3);
    waits[2](at(3));
    await c;
    expect(s.status).toBe('ready');
    const d = s.refresh(); // Try again always reads
    expect(waits.length).toBe(4);
    waits[3](ok);
    await d;
  });
});

describe('part counts', () => {
  it('gives each part its own repository count', async () => {
    const { grouping } = await import('./groups');
    const { card, snapshot } = await import('../test/fixtures');
    const top = gh(project('p_a', 'alpha'), 12);
    const part = { ...gh(project('p_a2', 'alpha-web'), 3, 'example-org/other-repo'), parentId: 'p_a' };
    const g = grouping(snapshot([card({ projectId: 'p_a' }), card({ projectId: 'p_a2' })], [top, part]));
    const entries = g.tops.find((t) => t.id === 'p_a')!.entries;
    expect(entries.find((e) => e.id === 'p_a2')?.issues).toBe(3);
    expect(entries.find((e) => e.id === 'p_a')?.issues).toBe(12);
  });
});
