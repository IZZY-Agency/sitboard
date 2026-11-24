import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import App from './App.svelte';
import { card, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle } from './test/render';
import type { Issues, IssuesSummary, OpenIssue, Project, RepoIssues, Snapshot } from './contract.gen';
import type { IssuesClient, IssuesLoad } from './data/issues';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});
beforeEach(() => localStorage.clear());

const REPO_A = 'example-org/example-repo';
const REPO_B = 'example-org/other-repo';

function withRepo(p: Project, repo: string, open: number | null): Project {
  return { ...p, repo, repoForge: 'github', repoHost: 'github.com', openIssues: open === null ? { known: false, open: 0 } : { known: true, open } };
}

const PA = withRepo(project('p_a', 'alpha'), REPO_A, 12);
const PB = withRepo(project('p_b', 'beta'), REPO_B, 3);
const CARDS = [card({ id: 'claude:a', projectId: 'p_a' }), card({ id: 'claude:b', projectId: 'p_b' })];

function issue(n: number, milestone = '', over: Partial<OpenIssue> = {}): OpenIssue {
  return { number: n, title: `Sample issue ${n}`, url: `https://github.com/${REPO_A}/issues/${n}`, milestone, labels: [], ...over };
}

function repoList(repo: string, open: number, issues: OpenIssue[]): RepoIssues {
  return { forge: 'github', host: 'github.com', repo, state: 'ok', open, issues };
}

const LIST: Issues = {
  state: 'ready',
  rev: 1,
  freshMs: 60_000,
  asOf: '2026-11-17T09:58:00Z',
  repos: [
    repoList(REPO_A, 12, [
      issue(48, '0.5.0', { milestoneDue: '2026-12-01T00:00:00Z', labels: ['bug'] }),
      issue(33, 'Later'),
      issue(5),
    ]),
    repoList(REPO_B, 3, [{ ...issue(7, '0.5.0'), url: `https://github.com/${REPO_B}/issues/7` }]),
  ],
};

const READY: IssuesSummary = { state: 'ready', asOf: '2026-11-17T09:58:00Z', open: 15, rev: 1 };

function client(loads: IssuesLoad[]) {
  const calls: string[] = [];
  const c: IssuesClient = {
    async load() {
      calls.push('load');
      return loads.length > 1 ? (loads.shift() as IssuesLoad) : (loads[0] ?? { ok: true, issues: LIST });
    },
  };
  return { client: c, calls };
}

function app(s: Snapshot, loads: IssuesLoad[] = [{ ok: true, issues: LIST }]) {
  const c = client(loads);
  const f = feedOf({ conn: 'live', snapshot: s });
  r = render(App, { feed: f.feed, actions: recordActions().actions, issues: c.client });
  return { ...c, push: (next: Snapshot) => f.push({ conn: 'live', snapshot: next }) };
}

const snap = (over: Partial<Snapshot> = {}, projects = [PA, PB]) => snapshot(CARDS, projects, { issues: READY, ...over });
const btn = () => document.querySelector<HTMLButtonElement>('header button.issues');
const panel = () => document.getElementById('issues-panel');
async function open() {
  btn()!.click();
  await settle();
}

describe('Issues', () => {
  it('has no button while issues are off', async () => {
    app(snap({ issues: { state: 'off', open: 0, rev: 0 } }));
    await settle();
    expect(btn()).toBeNull();
  });

  it('counts the open issues of every repository once', async () => {
    app(snap({}, [PA, PB, withRepo(project('p_c', 'alpha-copy'), REPO_A, 12)]));
    await settle();
    expect(btn()!.getAttribute('aria-label')).toBe('Issues, 15 open issues');
    expect(btn()!.textContent).toContain('15');
  });

  it('shows no number while no count is known', async () => {
    app(snap({ issues: { state: 'loading', open: 0, rev: 0 } }, [withRepo(PA, REPO_A, null)]));
    await settle();
    expect(btn()!.getAttribute('aria-label')).toBe('Issues');
  });

  it('loads the list when it opens, grouped by milestone', async () => {
    const c = app(snap());
    await settle();
    expect(c.calls).toEqual([]);
    await open();
    expect(c.calls).toEqual(['load']);
    const p = panel()!;
    expect(p.querySelector('.since')!.textContent).toContain('15 open · 4 listed here · read only');
    expect(p.querySelector('.filter .fl')!.textContent).toBe('Listed');
    const sec = p.querySelector(`[data-repo="github|github.com|${REPO_A}"]`) ?? p.querySelectorAll('section')[0];
    expect(sec.querySelector('.rh .n')!.textContent).toBe('12');
    expect([...sec.querySelectorAll('.msh')].map((h) => h.textContent?.trim())).toEqual(['0.5.0due 1 Dec 2026', 'Later', 'No milestone']);
    const link = sec.querySelector<HTMLAnchorElement>('a.num')!;
    expect(link.getAttribute('href')).toBe(`https://github.com/${REPO_A}/issues/48`);
    expect(link.target).toBe('_blank');
    expect(sec.querySelector('.more')!.getAttribute('href')).toBe(`https://github.com/${REPO_A}/issues`);
    expect(sec.querySelector('.more')!.textContent).toContain('+9 more on GitHub');
  });

  it('lists only the selected project', async () => {
    app(snap());
    await settle();
    [...document.querySelectorAll<HTMLButtonElement>('button.tile')].find((b) => b.textContent?.includes('beta'))!.click();
    await settle();
    await open();
    const names = [...panel()!.querySelectorAll('.pname')].map((e) => e.textContent);
    expect(names).toEqual(['beta']);
    expect(panel()!.querySelector('.title')!.textContent).toContain('beta');
  });

  it('filters by milestone', async () => {
    app(snap());
    await settle();
    await open();
    const chip = [...panel()!.querySelectorAll<HTMLButtonElement>('.chip')].find((b) => b.textContent?.startsWith('Later'))!;
    expect(chip.textContent).toBe('Later1');
    chip.click();
    await settle();
    const nums = [...panel()!.querySelectorAll('.num')].map((e) => e.textContent);
    expect(nums).toEqual(['#33 on GitHub, opens in a new tab']);
  });

  it('loads again when the snapshot announces a newer list while it is open', async () => {
    const c = app(snap());
    await settle();
    await open();
    c.push(snap({ seq: 2, issues: { ...READY, asOf: '2026-11-17T10:08:00Z', rev: 2 } }));
    await settle();
    expect(c.calls).toEqual(['load', 'load']);
    c.push(snap({ seq: 3, issues: { ...READY, asOf: '2026-11-17T10:08:00Z', rev: 2 } }));
    await settle();
    expect(c.calls).toEqual(['load', 'load']);
    c.push(snap({ seq: 4, issues: { ...READY, asOf: '2026-11-17T10:09:00Z', rev: 2 } })); // older or newer by the clock only: not a new list
    await settle();
    expect(c.calls).toEqual(['load', 'load']);
  });

  it('does not load while it is closed', async () => {
    const c = app(snap());
    await settle();
    c.push(snap({ seq: 2, issues: { ...READY, asOf: '2026-11-17T10:08:00Z', rev: 2 } }));
    await settle();
    expect(c.calls).toEqual([]);
  });

  it('Escape closes it and gives focus back to the button', async () => {
    app(snap());
    await settle();
    await open();
    panel()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(btn());
  });

  it('closes Catch-up when it opens', async () => {
    app(snap());
    await settle();
    document.querySelector<HTMLButtonElement>('header button.catchup')!.click();
    await settle();
    expect(document.getElementById('catchup-panel')).not.toBeNull();
    await open();
    expect(document.getElementById('catchup-panel')).toBeNull();
    expect(panel()).not.toBeNull();
  });

  it('says what went wrong and keeps the list', async () => {
    const c = app(snap(), [{ ok: true, issues: LIST }, { ok: false, kind: 'failed', message: 'sitboard answered 500' }]);
    await settle();
    await open();
    c.push(snap({ seq: 2, issues: { ...READY, asOf: '2026-11-17T10:08:00Z', rev: 2 } }));
    await settle();
    expect(panel()!.querySelector('[role=alert]')!.textContent).toContain('Could not load it again: sitboard answered 500');
    expect(panel()!.querySelectorAll('.num').length).toBe(4);
  });

  it('shows the count on a tile', async () => {
    app(snap());
    await settle();
    const tile = [...document.querySelectorAll<HTMLButtonElement>('button.tile')].find((b) => b.textContent?.includes('alpha'))!;
    expect(tile.querySelector('.iss')!.textContent).toBe('12 open issues');
    const all = [...document.querySelectorAll<HTMLButtonElement>('button.tile')].find((b) => b.textContent?.includes('all projects'))!;
    expect(all.querySelector('.iss')!.textContent).toBe('15 open issues');
  });

  it('shows no count on a tile whose repository has none known', async () => {
    app(snap({}, [PA, withRepo(PB, REPO_B, null)]));
    await settle();
    const tile = [...document.querySelectorAll<HTMLButtonElement>('button.tile')].find((b) => b.textContent?.includes('beta'))!;
    expect(tile.querySelector('.iss')).toBeNull();
  });
});

describe('Issues, review', () => {
  it('clears the titles when Catch-up finds the key lost', async () => {
    let lost = false;
    const catchup = {
      async load() {
        return lost ? ({ ok: false, kind: 'unauthorized' } as const) : ({ ok: true, catchup: { asOf: '2026-11-17T10:00:00Z', total: 0, items: [] } } as never);
      },
      async markRead() {
        return { ok: true } as const;
      },
    };
    const c = client([{ ok: true, issues: LIST }]);
    const f = feedOf({ conn: 'live', snapshot: snap() });
    r = render(App, { feed: f.feed, actions: recordActions().actions, issues: c.client, catchup });
    await settle();
    await open();
    expect(panel()!.querySelectorAll('.num').length).toBe(4);
    lost = true;
    f.push({ conn: 'live', snapshot: snap({ seq: 2, gitlabHosts: ['gitlab.example.com'] }) }); // Catch-up reads again
    await settle();
    expect(document.body.textContent).not.toContain('Sample issue 48');
    expect(panel()?.querySelectorAll('.num').length ?? 0).toBe(0);
  });

  it('says the count is not known while no repository answered', async () => {
    const pending: Issues = { state: 'ready', rev: 1, freshMs: 60_000, repos: [{ ...repoList(REPO_A, 0, []), state: 'pending' }] };
    app(snap({}, [withRepo(PA, REPO_A, null)]), [{ ok: true, issues: pending }]);
    await settle();
    await open();
    const since = panel()!.querySelector('.since')!.textContent!;
    expect(since).toContain('not answered yet');
    expect(since).not.toContain('0 open');
  });

  it('says a filtered section may have unlisted issues', async () => {
    const list: Issues = { state: 'ready', rev: 1, freshMs: 60_000, repos: [repoList(REPO_A, 12, [issue(48, '0.5.0'), issue(33, 'Later')]), repoList(REPO_B, 1, [issue(7, '0.5.0')])] };
    app(snap(), [{ ok: true, issues: list }]);
    await settle();
    await open();
    [...panel()!.querySelectorAll<HTMLButtonElement>('.chip')].find((b) => b.textContent?.startsWith('Later'))!.click();
    await settle();
    const notes = [...panel()!.querySelectorAll('.note')].map((n) => n.textContent);
    expect(notes).toContain('None in Later.'); // beta: everything listed
    const alpha = [...panel()!.querySelectorAll('section')][0];
    expect(alpha.querySelector('.note')).toBeNull(); // alpha has #33 in Later
  });

  it('says it when the listed issues of a truncated repository are not in the milestone', async () => {
    const list: Issues = { state: 'ready', rev: 1, freshMs: 60_000, repos: [repoList(REPO_A, 12, [issue(48, '0.5.0')]), repoList(REPO_B, 1, [issue(7, 'Later')])] };
    app(snap(), [{ ok: true, issues: list }]);
    await settle();
    await open();
    [...panel()!.querySelectorAll<HTMLButtonElement>('.chip')].find((b) => b.textContent?.startsWith('Later'))!.click();
    await settle();
    expect(panel()!.querySelector('section .note')!.textContent).toBe('None of the listed issues is in Later.');
  });

  it('keeps the count on a project that went idle', async () => {
    const idle = [card({ id: 'claude:a', projectId: 'p_a', column: 'idle' }), card({ id: 'claude:b', projectId: 'p_b' })];
    app(snapshot(idle, [PA, PB], { issues: READY }));
    await settle();
    const ib = [...document.querySelectorAll<HTMLButtonElement>('button.ibtn')].find((b) => b.textContent?.includes('alpha'))!;
    expect(ib.querySelector('.iss')!.textContent).toBe(', 12 open issues');
  });

  it('gives focus back to the panel when a new list takes the focused control away', async () => {
    const c = app(snap(), [{ ok: true, issues: LIST }, { ok: true, issues: { ...LIST, repos: [repoList(REPO_A, 1, [issue(5)]), LIST.repos[1]] } }]);
    await settle();
    await open();
    const later = [...panel()!.querySelectorAll<HTMLButtonElement>('.chip')].find((b) => b.textContent?.startsWith('Later'))!;
    later.focus();
    expect(document.activeElement).toBe(later);
    c.push(snap({ seq: 2, issues: { ...READY, asOf: '2026-11-17T10:08:00Z', rev: 2 } }));
    await settle();
    expect(later.isConnected).toBe(false);
    expect(panel()!.contains(document.activeElement)).toBe(true);
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(panel()).toBeNull();
  });

  it('keeps the panel focused when the browser reports the focus lost on removal', async () => {
    const c = app(snap(), [{ ok: true, issues: LIST }, { ok: true, issues: { ...LIST, repos: [repoList(REPO_A, 1, [issue(5)]), LIST.repos[1]] } }]);
    await settle();
    await open();
    const later = [...panel()!.querySelectorAll<HTMLButtonElement>('.chip')].find((b) => b.textContent?.startsWith('Later'))!;
    later.focus();
    // Some browsers fire focusout at the moment a focused node is taken out
    // (the event path is still the panel's, the node is already gone).
    const remove = later.remove.bind(later);
    later.remove = () => {
      later.addEventListener('focusout', remove, { once: true });
      later.dispatchEvent(new FocusEvent('focusout', { bubbles: true }));
    };
    c.push(snap({ seq: 2, issues: { ...READY, asOf: '2026-11-17T10:08:00Z', rev: 2 } }));
    await settle();
    expect(later.isConnected).toBe(false);
    expect(panel()!.contains(document.activeElement)).toBe(true);
  });

  it('shows two labels that read the same', async () => {
    const list: Issues = { state: 'ready', rev: 1, freshMs: 60_000, repos: [repoList(REPO_A, 1, [issue(9, '', { labels: ['bug', 'bug'] })])] };
    app(snap({}, [PA]), [{ ok: true, issues: list }]);
    await settle();
    await open();
    expect([...panel()!.querySelectorAll('.lb')].map((l) => l.textContent)).toEqual(['bug', 'bug']);
  });

  it('reads once when the panel is opened and closed quickly', async () => {
    let release: () => void = () => {};
    const calls: string[] = [];
    const slow: IssuesClient = {
      load: () =>
        new Promise<IssuesLoad>((res) => {
          calls.push('load');
          release = () => res({ ok: true, issues: LIST });
        }),
    };
    const f = feedOf({ conn: 'live', snapshot: snap() });
    r = render(App, { feed: f.feed, actions: recordActions().actions, issues: slow });
    await settle();
    for (let i = 0; i < 3; i++) {
      btn()!.click();
      await settle();
    }
    expect(calls).toEqual(['load']);
    release();
    await settle();
    expect(calls).toEqual(['load']); // the opens that shared the read did not queue another
  });
});

describe('Issues, round 2', () => {
  const NEWER = { ...READY, rev: 2 };

  it('loads again when only the rev moves, not when only the date does', async () => {
    const c = app(snap());
    await settle();
    await open();
    c.push(snap({ seq: 2, issues: NEWER })); // same asOf: a repository joined, a state changed
    await settle();
    expect(c.calls).toEqual(['load', 'load']);
    c.push(snap({ seq: 3, issues: { ...NEWER, asOf: '2026-11-17T10:30:00Z' } }));
    await settle();
    expect(c.calls).toEqual(['load', 'load']);
  });

  it('does not read again on a reopen when the list is fresh and the rev the same', async () => {
    const c = app(snap());
    await settle();
    await open();
    btn()!.click(); // close
    await settle();
    await open();
    expect(panel()).not.toBeNull();
    expect(panel()!.querySelectorAll('.num').length).toBe(4);
    expect(c.calls).toEqual(['load']);
  });

  it('reads again on a reopen when the rev moved while it was closed', async () => {
    const c = app(snap());
    await settle();
    await open();
    btn()!.click();
    await settle();
    c.push(snap({ seq: 2, issues: NEWER }));
    await settle();
    expect(c.calls).toEqual(['load']);
    await open();
    expect(c.calls).toEqual(['load', 'load']);
  });

  // Rule: after the server drops rows (a lost login, a denied host), none of
  // them is on screen or held by the page, whether the reload is out, fails,
  // or an answer from before the drop arrives late.
  describe('a purge', () => {
    const PURGED = { ...READY, rev: 3, purged: 3, open: 0 };
    it('takes the titles off an open panel at once and keeps them off when the reload fails', async () => {
      const c = app(snap(), [{ ok: true, issues: LIST }, { ok: false, kind: 'failed', message: 'down' }]);
      await settle();
      await open();
      expect(document.body.textContent).toContain('Sample issue 48');
      c.push(snap({ seq: 2, issues: PURGED }));
      await settle();
      expect(document.body.textContent).not.toContain('Sample issue 48');
      expect(c.calls).toEqual(['load', 'load']);
    });

    it('refuses an answer the server built before the purge', async () => {
      let release!: () => void;
      const late = new Promise<void>((res) => (release = res));
      const loads: IssuesLoad[] = [{ ok: true, issues: LIST }, { ok: true, issues: { ...LIST, rev: 2 } }, { ok: false, kind: 'failed', message: 'down' }];
      const calls: string[] = [];
      const c: IssuesClient = {
        async load() {
          calls.push('load');
          const l = loads.shift()!;
          if (calls.length === 2) await late; // asked at rev 2, answered after the purge
          return l;
        },
      };
      const f = feedOf({ conn: 'live', snapshot: snap() });
      r = render(App, { feed: f.feed, actions: recordActions().actions, issues: c });
      await settle();
      await open();
      f.push({ conn: 'live', snapshot: snap({ seq: 2, issues: { ...READY, rev: 2 } }) });
      await settle();
      f.push({ conn: 'live', snapshot: snap({ seq: 3, issues: PURGED }) });
      await settle();
      expect(document.body.textContent).not.toContain('Sample issue 48');
      release();
      await settle();
      expect(document.body.textContent).not.toContain('Sample issue 48');
      expect(calls).toEqual(['load', 'load', 'load']);
    });

    it('drops the list when issues are switched off', async () => {
      const c = app(snap(), [{ ok: true, issues: LIST }, { ok: false, kind: 'failed', message: 'down' }]);
      await settle();
      await open();
      btn()!.click();
      await settle();
      c.push(snap({ seq: 2, issues: { state: 'off', open: 0, rev: 1, purged: 2 } }));
      await settle();
      c.push(snap({ seq: 3, issues: { ...READY, rev: 3, purged: 2 } }));
      await settle();
      await open();
      expect(document.body.textContent).not.toContain('Sample issue 48');
    });

    it('drops the list held while the panel is closed', async () => {
      const c = app(snap(), [{ ok: true, issues: LIST }, { ok: false, kind: 'failed', message: 'down' }]);
      await settle();
      await open();
      btn()!.click();
      await settle();
      c.push(snap({ seq: 2, issues: PURGED }));
      await settle();
      await open();
      expect(document.body.textContent).not.toContain('Sample issue 48');
    });
  });

  describe('a lost key', () => {
    const catchupOf = (lost: () => boolean) => ({
      async load() {
        return lost() ? ({ ok: false, kind: 'unauthorized' } as const) : ({ ok: true, catchup: { asOf: '2026-11-17T10:00:00Z', total: 0, items: [] } } as never);
      },
      async markRead() {
        return { ok: true } as const;
      },
    });
    const counts = () => document.querySelectorAll('button.tile .iss, button.ibtn .iss').length;

    it('takes the counts off the page and keeps the alert and Close when Catch-up loses it', async () => {
      let lost = false;
      const c = client([{ ok: true, issues: LIST }]);
      const f = feedOf({ conn: 'live', snapshot: snap() });
      r = render(App, { feed: f.feed, actions: recordActions().actions, issues: c.client, catchup: catchupOf(() => lost) });
      await settle();
      await open();
      expect(counts()).toBeGreaterThan(0);
      expect(btn()!.getAttribute('aria-label')).toBe('Issues, 15 open issues');
      lost = true;
      f.push({ conn: 'live', snapshot: snap({ seq: 2, gitlabHosts: ['gitlab.example.com'] }) });
      await settle();
      expect(counts()).toBe(0);
      expect(btn()!.getAttribute('aria-label')).toBe('Issues');
      expect(panel()!.querySelector('[role=alert]')!.textContent).toContain('lost its key');
      expect(panel()!.querySelector('.close')).not.toBeNull();
    });

    it('does the same when the Issues read itself loses it', async () => {
      const c = app(snap(), [{ ok: false, kind: 'unauthorized' }]);
      await settle();
      expect(counts()).toBeGreaterThan(0);
      await open();
      expect(counts()).toBe(0);
      expect(btn()!.getAttribute('aria-label')).toBe('Issues');
      expect(panel()!.querySelector('[role=alert]')!.textContent).toContain('lost its key');
      expect(c.calls).toEqual(['load']);
    });

    it('clears the list when the board loses it, for good', async () => {
      const c = client([{ ok: true, issues: LIST }]);
      const f = feedOf({ conn: 'live', snapshot: snap() });
      r = render(App, { feed: f.feed, actions: recordActions().actions, issues: c.client });
      await settle();
      await open();
      expect(panel()!.querySelectorAll('.num').length).toBe(4);
      f.push({ conn: 'unauthorized', snapshot: null });
      await settle();
      expect(document.body.textContent).not.toContain('Sample issue 48');
      // Rule: an open panel stays through a lost key, cleared, with its alert and Close.
      expect(panel()!.querySelector('[role=alert]')!.textContent).toContain('lost its key');
      expect(panel()!.querySelector('.close')).not.toBeNull();
      f.push({ conn: 'live', snapshot: snap({ seq: 3 }) }); // whatever the board does next, the list does not come back
      await settle();
      expect(document.body.textContent).not.toContain('Sample issue 48');
      expect(panel()!.querySelector('[role=alert]')!.textContent).toContain('lost its key');
      expect(counts()).toBe(0);
      expect(c.calls).toEqual(['load']);
    });
  });

  describe('the focused control goes away', () => {
    it('gives focus back to the panel when Try again is pressed and the read starts', async () => {
      let release: () => void = () => {};
      let n = 0;
      const calls: string[] = [];
      const c: IssuesClient = {
        load: () => {
          calls.push('load');
          if (n++ === 0) return Promise.resolve({ ok: false, kind: 'failed', message: 'sitboard answered 500' } as IssuesLoad);
          return new Promise<IssuesLoad>((res) => (release = () => res({ ok: true, issues: LIST })));
        },
      };
      const f = feedOf({ conn: 'live', snapshot: snap() });
      r = render(App, { feed: f.feed, actions: recordActions().actions, issues: c });
      await settle();
      await open();
      const retry = panel()!.querySelector<HTMLButtonElement>('button.retry')!;
      retry.focus();
      expect(document.activeElement).toBe(retry);
      // A browser may report the focus lost before it detaches the node.
      retry.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: null }));
      retry.click();
      await settle();
      expect(retry.isConnected).toBe(false);
      expect(panel()).not.toBeNull();
      expect(panel()!.contains(document.activeElement)).toBe(true);
      document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await settle();
      expect(panel()).toBeNull();
      release();
      await settle();
    });

    it('leaves focus alone when it moved out of the panel to the button', async () => {
      const c = app(snap(), [{ ok: true, issues: LIST }, { ok: true, issues: { ...LIST, repos: [repoList(REPO_A, 1, [issue(5)]), LIST.repos[1]] } }]);
      await settle();
      await open();
      const later = [...panel()!.querySelectorAll<HTMLButtonElement>('.chip')].find((b) => b.textContent?.startsWith('Later'))!;
      later.focus();
      btn()!.focus(); // focus goes to the toggle: the panel stays open and does not take it back
      c.push(snap({ seq: 2, issues: NEWER }));
      await settle();
      expect(later.isConnected).toBe(false);
      expect(document.activeElement).toBe(btn());
      // Rule: Escape closes the open panel wherever focus is, the toggle too.
      btn()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await settle();
      expect(panel()).toBeNull();
      expect(document.activeElement).toBe(btn());
    });
  });

  describe('milestone order', () => {
    const SPRINTS: Issues = {
      state: 'ready',
      rev: 1,
      freshMs: 60_000,
      repos: [
        repoList(REPO_A, 4, [issue(1, 'Sample sprint 10'), issue(2, 'Sample sprint 2'), issue(3, 'Sample sprint 1'), issue(4, 'Sample sprint 11')]),
        repoList(REPO_B, 2, [{ ...issue(5, 'Sample sprint 2'), url: `https://github.com/${REPO_B}/issues/5` }, { ...issue(6), url: `https://github.com/${REPO_B}/issues/6` }]),
      ],
    };

    it('puts Sample sprint 2 before Sample sprint 10 in the chips and in the list', async () => {
      app(snap(), [{ ok: true, issues: SPRINTS }]);
      await settle();
      await open();
      const chips = [...panel()!.querySelectorAll('.filter .chip .u')].map((e) => e.textContent);
      expect(chips).toEqual(['Sample sprint 1', 'Sample sprint 2', 'Sample sprint 10', 'Sample sprint 11', 'No milestone']);
      const alpha = panel()!.querySelectorAll('section')[0];
      expect([...alpha.querySelectorAll('.msh')].map((h) => h.textContent?.trim())).toEqual(['Sample sprint 1', 'Sample sprint 2', 'Sample sprint 10', 'Sample sprint 11']);
    });

    it('orders a chip the repositories share by its earliest due date', async () => {
      const list: Issues = {
        state: 'ready',
        rev: 1,
        freshMs: 60_000,
        repos: [
          repoList(REPO_A, 2, [issue(1, 'Beta', { milestoneDue: '2026-12-20T00:00:00Z' }), issue(2, 'Alpha', { milestoneDue: '2026-12-10T00:00:00Z' })]),
          repoList(REPO_B, 1, [{ ...issue(3, 'Beta', { milestoneDue: '2026-12-01T00:00:00Z' }), url: `https://github.com/${REPO_B}/issues/3` }]),
        ],
      };
      app(snap(), [{ ok: true, issues: list }]);
      await settle();
      await open();
      expect([...panel()!.querySelectorAll('.filter .chip .u')].map((e) => e.textContent)).toEqual(['Beta', 'Alpha']);
      // Rule: chips and every repository's groups read in one order.
      const alpha = panel()!.querySelectorAll('section')[0];
      expect([...alpha.querySelectorAll('.msh .u')].map((h) => h.textContent)).toEqual(['Beta', 'Alpha']);
    });
  });
});
