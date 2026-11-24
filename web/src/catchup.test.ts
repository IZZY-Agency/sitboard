import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import App from './App.svelte';
import { card, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle, text } from './test/render';
import type { Catchup, CatchupItem } from './contract.gen';
import type { CatchupClient, CatchupLoad, ReadResult } from './data/catchup';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
  vi.useRealTimers();
});
// The fixtures are dated 17 Nov 2026; the clock stands just after the board's
// "as of" time, so "since" and the 7-day window never depend on the real day.
// Only Date is faked: timers and animation frames stay real for settle().
const FIXED_NOW = Date.parse('2026-11-17T10:05:00Z');
beforeEach(() => {
  localStorage.clear();
  vi.useFakeTimers({ toFake: ['Date'], now: FIXED_NOW });
});

const P = 'p_000000000001';
const onBoardCard = card({ id: 'claude:lead', agent: { name: 'harbor-lead' } });
const other = card({ id: 'claude:other', agent: { name: 'web-search' } });

const turn = (at: string, agent: string, cardId = ''): CatchupItem => ({
  at,
  kind: 'turn_finished',
  cardId,
  projectId: P,
  projectName: 'harbor',
  agentName: agent,
  title: `${agent} title`,
});
const merged = (at: string, n: number): CatchupItem => ({
  at,
  kind: 'pr_merged',
  cardId: '',
  projectId: 'p_000000000002',
  projectName: 'lumen',
  agentName: 'docs-intro',
  title: 'Getting started page',
  pullRequest: {
    forge: 'github' as const, host: 'github.com', repo: 'example-org/example-repo',
    number: n,
    title: 'Getting started page',
    url: `https://github.com/example-org/example-repo/pull/${n}`,
    state: 'merged',
    checks: 'passing',
    review: 'approved',
    draft: false,
  },
});

type Partial = Omit<Catchup, 'asOf' | 'total' | 'list'> & { asOf?: string; total?: number; list?: string };
const AS_OF = '2026-11-17T10:00:00Z';

function client(loads: Array<CatchupLoad | Partial>, reads: ReadResult[] = []) {
  const calls: string[] = [];
  const c: CatchupClient = {
    async load() {
      calls.push('load');
      const next = loads.length > 1 ? loads.shift()! : loads[0];
      return 'ok' in next ? next : { ok: true, catchup: { list: 'r-1', asOf: AS_OF, total: next.items.length, ...next } };
    },
    async markRead(list) {
      calls.push(`read ${list}`);
      return reads.shift() ?? { ok: true };
    },
  };
  return { calls, client: c };
}

function app(loads: Array<CatchupLoad | Partial>, reads: ReadResult[] = [], conn: 'live' | 'unauthorized' = 'live') {
  const f = feedOf({ conn, snapshot: snapshot([onBoardCard, other], [project(P, 'harbor')]) });
  const c = client(loads, reads);
  r = render(App, { feed: f.feed, actions: recordActions().actions, catchup: c.client });
  return c;
}

const q = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const btn = () => q<HTMLButtonElement>('header button.catchup');
const panel = () => q('#catchup-panel');
async function open() {
  btn()!.click();
  await settle();
}

describe('catch-up button', () => {
  it('says how many items are new, in words for screen readers', async () => {
    app([{ items: [turn('2026-11-17T09:00:00Z', 'a'), turn('2026-11-17T09:10:00Z', 'b')] }]);
    await settle();
    expect(text(btn())).toBe('Catch-up2');
    expect(btn()!.getAttribute('aria-label')).toBe('Catch-up, 2 new items');
    expect(btn()!.getAttribute('aria-expanded')).toBe('false');
  });
  it('shows no number before the first list, and none for nothing new', async () => {
    const c = app([{ items: [] }]);
    expect(btn()!.getAttribute('aria-label')).toBe('Catch-up');
    await settle();
    expect(text(btn())).toBe('Catch-up');
    expect(btn()!.getAttribute('aria-label')).toBe('Catch-up, nothing new');
    expect(c.calls).toEqual(['load']);
  });
  it('is not there when the tab lost its key', async () => {
    app([{ items: [] }], [], 'unauthorized');
    await settle();
    expect(btn()).toBeNull();
  });
});

describe('catch-up panel', () => {
  it('opens on the button, loads again, and marks nothing read', async () => {
    const c = app([{ items: [turn('2026-11-17T09:00:00Z', 'a')] }]);
    await settle();
    await open();
    expect(panel()).not.toBeNull();
    expect(panel()!.getAttribute('role')).toBe('dialog');
    expect(document.activeElement).toBe(panel());
    expect(btn()!.getAttribute('aria-expanded')).toBe('true');
    expect(btn()!.getAttribute('aria-controls')).toBe('catchup-panel');
    expect(c.calls).toEqual(['load', 'load']);
  });

  it('Escape closes it and gives focus back to the button', async () => {
    app([{ items: [turn('2026-11-17T09:00:00Z', 'a')] }]);
    await settle();
    await open();
    panel()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(btn());
  });

  it('closes when focus moves out of it, and stays when it moves inside', async () => {
    app([{ items: [turn('2026-11-17T09:00:00Z', 'a')] }]);
    await settle();
    await open();
    const done = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!;
    panel()!.dispatchEvent(new FocusEvent('focusout', { relatedTarget: done, bubbles: true }));
    await settle();
    expect(panel()).not.toBeNull();
    const outside = document.querySelector<HTMLButtonElement>('nav[aria-label="View"] button')!;
    panel()!.dispatchEvent(new FocusEvent('focusout', { relatedTarget: outside, bubbles: true }));
    await settle();
    expect(panel()).toBeNull();
  });

  it('Close closes it too', async () => {
    app([{ items: [] }]);
    await settle();
    await open();
    [...panel()!.querySelectorAll('button')].find((b) => text(b).startsWith('Close'))!.click();
    await settle();
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(btn());
  });

  it('groups by project, newest first, and folds one agent’s turns', async () => {
    app([
      {
        marker: '2026-11-17T08:00:00Z',
        items: [turn('2026-11-17T09:50:00Z', 'harbor-lead', 'claude:lead'), merged('2026-11-17T09:40:00Z', 88), turn('2026-11-17T09:30:00Z', 'harbor-lead', 'claude:lead')],
      },
    ]);
    await settle();
    await open();
    const groups = [...panel()!.querySelectorAll('section')];
    expect(groups.map((g) => text(g.querySelector('h3')))).toEqual(['harbor2', 'lumen1']);
    const rows = [...panel()!.querySelectorAll('li')].map((li) => text(li.querySelector('.k')));
    expect(rows).toEqual(['2 turns', 'Merged']);
    expect(text(panel()!.querySelector('h2'))).toBe('Since you looked');
  });

  it('a pull request row links to GitHub in a new tab; a card row shows the card', async () => {
    app([{ items: [turn('2026-11-17T09:50:00Z', 'harbor-lead', 'claude:lead'), merged('2026-11-17T09:40:00Z', 88), turn('2026-11-17T09:00:00Z', 'gone', 'claude:gone')] }]);
    await settle();
    await open();
    const a = panel()!.querySelector<HTMLAnchorElement>('a')!;
    expect(a.getAttribute('href')).toBe('https://github.com/example-org/example-repo/pull/88');
    expect(a.target).toBe('_blank');
    expect(a.rel).toContain('noopener');
    expect(text(a)).toBe('#88 on GitHub, opens in a new tab');
    expect(a.classList.contains('ext')).toBe(true); // wears the ↗ of a link that opens elsewhere
    const cardButtons = [...panel()!.querySelectorAll<HTMLButtonElement>('li button')];
    expect(cardButtons.map(text)).toEqual(['Card: show harbor-lead on the board']); // the gone card has none

    cardButtons[0].click();
    await settle();
    expect(panel()).toBeNull();
    const shown = q('[data-card="claude:lead"]')!;
    expect(shown.contains(document.activeElement)).toBe(true);
  });

  for (const view of ['board', 'lanes', 'teams']) {
    it(`"Card" finds and focuses the card in ${view}`, async () => {
      localStorage.setItem('sitboard.view', view);
      app([{ items: [turn('2026-11-17T09:50:00Z', 'harbor-lead', 'claude:lead')] }]);
      await settle();
      await open();
      panel()!.querySelector<HTMLButtonElement>('li button')!.click();
      await settle();
      const shown = [...document.querySelectorAll<HTMLElement>('[data-card="claude:lead"]')];
      expect(shown.length).toBeGreaterThan(0);
      expect(shown.some((e) => e.contains(document.activeElement))).toBe(true);
    });
  }

  it('"Caught up" marks read up to the newest item shown, then loads again', async () => {
    const two = { items: [turn('2026-11-17T09:50:00Z', 'a'), turn('2026-11-17T09:20:00Z', 'b')] };
    const c = app([two, two, { marker: '2026-11-17T09:50:00Z', items: [] }]);
    await settle();
    await open();
    const done = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!;
    done.click();
    await settle();
    expect(c.calls).toEqual(['load', 'load', 'read r-1', 'load']);
    expect(text(panel()!.querySelector('.empty'))).toMatch(/^Nothing new since (\w+ )*\d\d:\d\d\.$/);
    expect(text(panel()!.querySelector('[role="status"]'))).toMatch(/Marked read/);
    expect(done.disabled).toBe(true);
  });

  it('a list that is not current marks nothing: it says so, never "Marked read", and shows the list again', async () => {
    const two = { items: [turn('2026-11-17T09:50:00Z', 'a'), turn('2026-11-17T09:20:00Z', 'b')] };
    const three = { list: 'r-2', items: [turn('2026-11-17T09:55:00Z', 'c'), ...two.items] };
    const c = app([two, two, three], [{ ok: false, kind: 'stale' }]);
    await settle();
    await open();
    const done = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!;
    done.click();
    await settle();
    expect(c.calls).toEqual(['load', 'load', 'read r-1', 'load']);
    expect(text(panel()!.querySelector('[role="alert"]'))).toMatch(/^Could not mark it read: the list changed since this tab loaded it .*; this is the list as it is now\. Nothing was lost\.$/);
    expect(text(panel()!.querySelector('[role="status"]'))).not.toMatch(/Marked read/);
    expect(panel()!.querySelectorAll('li')).toHaveLength(3); // the fresh list: a new agent's turn, then the two
    expect(done.disabled).toBe(false);
    done.click();
    await settle();
    expect(c.calls.at(-2)).toBe('read r-2');
  });

  it('a list that is not current and a reload that fails: the button waits for a list that can be marked', async () => {
    const two = { items: [turn('2026-11-17T09:50:00Z', 'a'), turn('2026-11-17T09:20:00Z', 'b')] };
    const down: CatchupLoad = { ok: false, kind: 'failed', message: 'sitboard did not answer' };
    const c = app([two, two, down], [{ ok: false, kind: 'stale' }]);
    await settle();
    await open();
    const done = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!;
    done.click();
    await settle();
    const alerts = [...panel()!.querySelectorAll('[role="alert"]')].map(text);
    expect(alerts[0]).toMatch(/^Could not mark it read: the list changed since this tab loaded it \([^)]*\)\. Nothing was lost\.$/);
    expect(alerts[1]).toMatch(/^Could not load it again: sitboard did not answer\. This is the list as it was\./);
    expect(done.disabled).toBe(true);
    expect(c.calls.filter((x) => x.startsWith('read'))).toEqual(['read r-1']);
  });

  it('at the cap nothing is marked: it says why and keeps the list', async () => {
    const two = { items: [turn('2026-11-17T09:50:00Z', 'a'), turn('2026-11-17T09:20:00Z', 'b')] };
    const c = app([two], [{ ok: false, kind: 'full' }]);
    await settle();
    await open();
    const done = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!;
    done.click();
    await settle();
    expect(c.calls.slice(-2)).toEqual(['read r-1', 'load']);
    expect(text(panel()!.querySelector('[role="alert"]'))).toMatch(/^Could not mark it read: marking this list read would keep more than 20,000 items .* Nothing was lost\.$/);
    expect(text(panel()!.querySelector('[role="status"]'))).not.toMatch(/Marked read/);
    expect(done.disabled).toBe(false);
  });

  it('says when there is nothing new, and offers nothing to mark', async () => {
    app([{ items: [] }]);
    await settle();
    await open();
    expect(text(panel()!.querySelector('.empty'))).toBe('Nothing new in the last 7 days.');
    expect([...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!.disabled).toBe(true);
  });

  it('folds a long project behind "Show N more"', async () => {
    const items = Array.from({ length: 9 }, (_, i) => turn(`2026-11-17T09:${String(10 + i)}:00Z`, `agent-${i}`));
    app([{ items }]);
    await settle();
    await open();
    expect(panel()!.querySelectorAll('li')).toHaveLength(4);
    const more = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b).startsWith('Show'))!;
    expect(text(more)).toBe('Show 5 more in harbor');
    more.click();
    flushSync();
    expect(panel()!.querySelectorAll('li')).toHaveLength(9);
  });

  it('when the server cannot be reached it says so and offers to try again', async () => {
    const c = app([{ ok: false, kind: 'failed', message: 'sitboard did not answer' }]);
    await settle();
    await open();
    const alert = panel()!.querySelector('[role="alert"]')!;
    expect(text(alert)).toBe('Could not load catch-up: sitboard did not answer. Try again');
    alert.querySelector('button')!.click();
    await settle();
    expect(c.calls).toEqual(['load', 'load', 'load']);
  });

  it('keeps the last list when a later load fails', async () => {
    app([{ items: [turn('2026-11-17T09:00:00Z', 'a')] }, { ok: false, kind: 'failed', message: 'sitboard did not answer' }]);
    await settle();
    await open();
    expect(text(panel()!.querySelector('[role="alert"]'))).toMatch(/^Could not load it again: sitboard did not answer\. This is the list as it was\./);
    expect(panel()!.querySelectorAll('li')).toHaveLength(1);
  });

  it('a failed "Caught up" keeps every item', async () => {
    app([{ items: [turn('2026-11-17T09:00:00Z', 'a')] }], [{ ok: false, kind: 'failed', message: 'sitboard could not save it' }]);
    await settle();
    await open();
    [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!.click();
    await settle();
    expect(text(panel()!.querySelector('[role="alert"]'))).toBe('Could not mark it read: sitboard could not save it. Nothing was lost.');
    expect(panel()!.querySelectorAll('li')).toHaveLength(1);
  });

  it('after "Caught up" focus goes to the heading, not to the page', async () => {
    const one = { items: [turn('2026-11-17T09:00:00Z', 'a')] };
    app([one, one, { marker: AS_OF, items: [] }]);
    await settle();
    await open();
    const done = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!;
    done.focus();
    done.click();
    await settle();
    expect(done.disabled).toBe(true);
    expect(document.activeElement).toBe(panel()!.querySelector('h2'));
    panel()!.querySelector('h2')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(panel()).toBeNull();
  });

  it('after "Show more" focus goes to the first row it showed', async () => {
    const items = Array.from({ length: 9 }, (_, i) => turn(`2026-11-17T09:${String(10 + i)}:00Z`, `agent-${i}`));
    app([{ items }]);
    await settle();
    await open();
    [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b).startsWith('Show'))!.click();
    await settle();
    const rows = [...panel()!.querySelectorAll('li')];
    expect(document.activeElement).toBe(rows[4]);
  });

  it('at the cap the button and the panel say only the newest are listed', async () => {
    app([{ total: 5, items: [turn('2026-11-17T09:10:00Z', 'a'), turn('2026-11-17T09:00:00Z', 'b')] }]);
    await settle();
    expect(text(btn())).toBe('Catch-up2+');
    expect(btn()!.getAttribute('aria-label')).toBe('Catch-up, 5 new items, the newest 2 listed');
    await open();
    expect(text(panel()!.querySelector('.since'))).toBe('the last 7 days · newest 2 of 5');
  });

  it('with --no-state the panel says nothing is saved', async () => {
    app([{ unsaved: true, items: [turn('2026-11-17T09:00:00Z', 'a')] }]);
    await settle();
    await open();
    expect(text(panel()!.querySelector('.note'))).toBe('Nothing here is saved: sitboard runs with --no-state, or could not write its state file when it started, so what you mark read lasts until it stops.');
  });

  it('says so while earlier sessions are still being read', async () => {
    const pending = { pending: true, items: [turn('2026-11-17T09:00:00Z', 'a')] };
    app([pending, pending]);
    await settle();
    await open();
    const notes = () => [...panel()!.querySelectorAll('.note')].map(text);
    expect(notes()).toEqual(['Still reading the sessions that ended before sitboard started: more may appear here.']);
    const done = [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!;
    expect(done.disabled).toBe(false); // never blocked
  });

  it('says so when the read of earlier sessions was cut short', async () => {
    const partial = { partial: true, items: [turn('2026-11-17T09:00:00Z', 'a')] };
    app([partial, partial]);
    await settle();
    await open();
    const notes = () => [...panel()!.querySelectorAll('.note')].map(text);
    expect(notes()).toEqual(['Some sessions that ended before sitboard started were too large or too slow to read in full: items from them may be missing here.']);
  });

  it('with a state file it says nothing about saving', async () => {
    app([{ items: [turn('2026-11-17T09:00:00Z', 'a')] }]);
    await settle();
    await open();
    expect(panel()!.querySelector('.note')).toBeNull();
  });

  it('a marker older than the 7 days kept says "the last 7 days"', async () => {
    app([{ marker: '2020-01-01T00:00:00Z', items: [turn('2026-11-17T09:00:00Z', 'a')] }]);
    await settle();
    await open();
    expect(text(panel()!.querySelector('.since'))).toBe('the last 7 days · 1');
  });

  it('a list that failed to load again and a failed "Caught up" both say so', async () => {
    app(
      [{ items: [turn('2026-11-17T09:00:00Z', 'a')] }, { ok: false, kind: 'failed', message: 'sitboard did not answer' }],
      [{ ok: false, kind: 'failed', message: 'sitboard could not save it' }],
    );
    await settle();
    await open();
    [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!.click();
    await settle();
    const alerts = [...panel()!.querySelectorAll('[role="alert"]')].map(text);
    expect(alerts).toEqual([
      'Could not mark it read: sitboard could not save it. Nothing was lost.',
      'Could not load it again: sitboard did not answer. This is the list as it was. Try again',
    ]);
    expect(panel()!.querySelectorAll('li')).toHaveLength(1);
  });

  it('a "Caught up" that worked, then a failed load, does not show the read items as unread', async () => {
    const one = { items: [turn('2026-11-17T09:00:00Z', 'a')] };
    app([one, one, { ok: false, kind: 'failed', message: 'sitboard did not answer' }]);
    await settle();
    await open();
    [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => text(b) === 'Caught up')!.click();
    await settle();
    expect(text(panel()!.querySelector('[role="alert"]'))).toBe('Marked read, but could not load what came after: sitboard did not answer. Try again');
    expect(panel()!.querySelectorAll('li')).toHaveLength(0);
    expect(text(btn())).toBe('Catch-up');
  });

  it('a session with no name reads "ended session" off the board and "unnamed agent" on it', async () => {
    app([{ items: [turn('2026-11-17T09:10:00Z', ''), turn('2026-11-17T09:00:00Z', '', 'claude:on-board')] }]);
    await settle();
    await open();
    expect([...panel()!.querySelectorAll('.ag')].map(text)).toEqual(['ended session', 'unnamed agent']);
  });

  it('untrusted names are text, never markup', async () => {
    app([{ items: [{ ...turn('2026-11-17T09:00:00Z', '<img src=x onerror=alert(1)>\u202e'), projectName: '<b>x</b>' }] }]);
    await settle();
    await open();
    expect(panel()!.querySelector('img, b')).toBeNull();
    expect(text(panel()!.querySelector('.ag'))).toBe('<img src=x onerror=alert(1)>');
  });
});

describe('merge requests of a host the snapshot no longer lists', () => {
  const HOST = 'gitlab.example';
  const mr = (at: string, n: number, host = HOST): CatchupItem => ({
    at,
    kind: 'pr_opened',
    cardId: '',
    projectId: 'p_000000000003',
    projectName: 'quay',
    agentName: 'ci-fixer',
    title: `Placeholder merge request ${n}`,
    pullRequest: {
      forge: 'gitlab' as const, host, repo: 'example-org/example-repo',
      number: n,
      title: `Placeholder merge request ${n}`,
      url: `https://${host}/example-org/example-repo/-/merge_requests/${n}`,
      state: 'open',
      checks: 'passing',
      review: '',
      draft: false,
    },
  });

  function appOn(loads: Array<CatchupLoad | Partial>) {
    const f = feedOf({ conn: 'live', snapshot: snapshot([onBoardCard], [project(P, 'harbor')], { gitlabHosts: [HOST] }) });
    const c = client(loads);
    r = render(App, { feed: f.feed, actions: recordActions().actions, catchup: c.client });
    return { f, c };
  }
  const items = () => [mr('2026-11-17T09:30:00Z', 7), merged('2026-11-17T09:00:00Z', 9)];
  const body = () => panel()?.textContent ?? '';

  it('drops the row and its title when a snapshot arrives without the host', async () => {
    const { f } = appOn([{ items: items() }]);
    await settle();
    await open();
    expect(body()).toContain('Placeholder merge request 7');
    expect(panel()!.querySelector(`a[href="https://${HOST}/example-org/example-repo/-/merge_requests/7"]`)).not.toBeNull();

    f.push({ conn: 'live', snapshot: snapshot([onBoardCard], [project(P, 'harbor')], { gitlabHosts: [] }) });
    await settle();
    expect(body()).not.toContain('Placeholder merge request 7');
    expect(body()).not.toContain('ci-fixer');
    expect(panel()!.querySelector('a[href*="/-/merge_requests/"]')).toBeNull();
    // A GitHub row is untouched.
    expect(body()).toContain('Getting started page');
  });

  it('keeps them away when the poll that follows fails and the panel keeps its old list', async () => {
    const { f, c } = appOn([{ items: items() }, { ok: false, kind: 'failed', message: 'sitboard did not answer' }]);
    await settle();
    await open(); // this load fails: the list stays
    expect(c.calls).toEqual(['load', 'load']);
    expect(body()).toContain('Placeholder merge request 7');

    f.push({ conn: 'live', snapshot: snapshot([onBoardCard], [project(P, 'harbor')], { gitlabHosts: ['other.example'] }) });
    await settle();
    expect(body()).not.toContain('Placeholder merge request 7');
    expect(body()).toContain('Getting started page');
  });

  const withHosts = (gitlabHosts: string[]) => ({
    conn: 'live' as const,
    snapshot: snapshot([onBoardCard], [project(P, 'harbor')], { gitlabHosts }),
  });
  const failed: CatchupLoad = { ok: false, kind: 'failed', message: 'sitboard did not answer' };

  it('a host that leaves takes its rows out of the kept list: they do not come back with the host', async () => {
    // Every load after the first two fails, so the panel only has its kept list.
    const { f } = appOn([{ items: items() }, { items: items() }, failed]);
    await settle();
    await open();
    expect(body()).toContain('Placeholder merge request 7');

    f.push(withHosts([])); // logged out of the host
    await settle();
    expect(body()).not.toContain('Placeholder merge request 7');

    f.push(withHosts([HOST])); // back, maybe under another login, before a fresh list
    await settle();
    expect(body()).not.toContain('Placeholder merge request 7');
    expect(panel()!.querySelector('a[href*="/-/merge_requests/"]')).toBeNull();
    // The header's count no longer holds the row either.
    expect(btn()!.getAttribute('aria-label')).toBe('Catch-up, 1 new item');
    // A GitHub row is untouched.
    expect(body()).toContain('Getting started page');
  });

  it('a list asked for before the host came back does not bring its rows back', async () => {
    // Loads answer only when the test says so.
    const out: Array<(l: CatchupLoad) => void> = [];
    const c: CatchupClient = {
      load: () => new Promise<CatchupLoad>((res) => out.push(res)),
      markRead: async () => ({ ok: true }),
    };
    const ok = (): CatchupLoad => ({ ok: true, catchup: { list: 'r-1', asOf: AS_OF, total: 2, items: items() } });
    const f = feedOf(withHosts([HOST]));
    r = render(App, { feed: f.feed, actions: recordActions().actions, catchup: c });
    await settle();
    out[0](ok());
    await settle();
    await open(); // a second load goes out and stays out
    expect(body()).toContain('Placeholder merge request 7');
    const before = out.length;

    f.push(withHosts([])); // the host leaves while that load is out
    await settle();
    const away = out.length;
    f.push(withHosts([HOST])); // and comes back
    await settle();
    // Every list asked for before the host came back answers with the old rows.
    for (const res of out.slice(before - 1, away)) res(ok());
    await settle();
    expect(body()).not.toContain('Placeholder merge request 7');
    expect(body()).toContain('Getting started page');

    // Only a list asked for while the host is listed again shows its rows.
    expect(out.length).toBeGreaterThan(away);
    out[out.length - 1](ok());
    await settle();
    expect(body()).toContain('Placeholder merge request 7');
  });

  it('keeps a merge request of a host mounted under a subfolder, and only under that subfolder', async () => {
    const mounted = (n: number, repo: string): CatchupItem => {
      const it = mr('2026-11-17T09:30:00Z', n);
      return { ...it, pullRequest: { ...it.pullRequest!, repo, url: `https://${HOST}/${repo}/-/merge_requests/${n}` } };
    };
    const f = feedOf(withHosts([`${HOST}/gitlab`]));
    const c = client([{ items: [mounted(7, 'gitlab/example-org/example-repo'), mounted(8, 'example-org/example-repo'), merged('2026-11-17T09:00:00Z', 9)] }]);
    r = render(App, { feed: f.feed, actions: recordActions().actions, catchup: c.client });
    await settle();
    await open();
    expect(panel()!.querySelector(`a[href="https://${HOST}/gitlab/example-org/example-repo/-/merge_requests/7"]`)).not.toBeNull();
    // The same host with a path outside the mount is not shown at all.
    expect(panel()!.querySelector('a[href*="/-/merge_requests/8"]')).toBeNull();
    expect(body()).not.toContain('Placeholder merge request 8');
    expect(body()).toContain('Placeholder merge request 7');
    expect(body()).toContain('Getting started page');

    // The mount moves: the rows of the old one leave at once, before the list is asked for again.
    f.push(withHosts([`${HOST}/apps`]));
    await settle();
    expect(body()).not.toContain('Placeholder merge request 7');
    expect(body()).toContain('Getting started page');
  });

  it('shows no merge request of a host that was never listed', async () => {
    appOn([{ items: [mr('2026-11-17T09:30:00Z', 8, 'unknown.example'), merged('2026-11-17T09:00:00Z', 9)] }]);
    await settle();
    await open();
    expect(body()).not.toContain('Placeholder merge request 8');
    expect(body()).toContain('Getting started page');
  });

  it('a host listed later shows no row from a list that came while it was not', async () => {
    const unlisted = () => [mr('2026-11-17T09:30:00Z', 8, 'unknown.example'), merged('2026-11-17T09:00:00Z', 9)];
    const { f } = appOn([{ items: unlisted() }, { items: unlisted() }, failed]);
    await settle();
    await open();
    expect(body()).not.toContain('Placeholder merge request 8');

    f.push(withHosts([HOST, 'unknown.example'])); // logged in now; no fresh list yet
    await settle();
    expect(body()).not.toContain('Placeholder merge request 8');
    expect(body()).toContain('Getting started page');
  });
});
