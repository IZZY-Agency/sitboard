import { afterEach, beforeEach, describe, expect, it } from 'vitest';
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
});
beforeEach(() => localStorage.clear());

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
    repo: 'example-org/example-repo',
    number: n,
    title: 'Getting started page',
    url: `https://github.com/example-org/example-repo/pull/${n}`,
    state: 'merged',
    checks: 'passing',
    review: 'approved',
    draft: false,
  },
});

type Partial = Omit<Catchup, 'asOf' | 'total'> & { asOf?: string; total?: number };
const AS_OF = '2026-11-17T10:00:00Z';

function client(loads: Array<CatchupLoad | Partial>, reads: ReadResult[] = []) {
  const calls: string[] = [];
  const c: CatchupClient = {
    async load() {
      calls.push('load');
      const next = loads.length > 1 ? loads.shift()! : loads[0];
      return 'ok' in next ? next : { ok: true, catchup: { asOf: AS_OF, total: next.items.length, ...next } };
    },
    async markRead(upTo) {
      calls.push(`read ${upTo}`);
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
    expect(c.calls).toEqual(['load', 'load', `read ${AS_OF}`, 'load']);
    expect(text(panel()!.querySelector('.empty'))).toMatch(/^Nothing new since (\w+ )*\d\d:\d\d\.$/);
    expect(text(panel()!.querySelector('[role="status"]'))).toMatch(/Marked read/);
    expect(done.disabled).toBe(true);
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
    expect(text(panel()!.querySelector('.note'))).toBe('Nothing here is saved: sitboard runs with --no-state, so after every start this lists the last 7 days again.');
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
