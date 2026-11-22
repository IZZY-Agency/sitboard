import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App.svelte';
import { card, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle, text } from './test/render';
import type { Leftover, LeftoverPart, Leftovers, LeftoversSummary } from './contract.gen';
import type { CatchupClient, CatchupLoad } from './data/catchup';
import { createLeftoversClient, heldLine, leftoversLink, prHref, readLeftovers, verdict, type LeftoversClient, type LeftoversLoad } from './data/leftovers';
import { clock } from './data/time';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
  vi.unstubAllGlobals();
});
beforeEach(() => localStorage.clear());

const P = 'p_000000000001';

function item(over: Partial<Leftover> = {}): Leftover {
  return {
    projectId: P,
    pullRequest: {
      forge: 'github' as const, host: 'github.com', repo: 'example-org/example-repo',
      number: 41,
      title: 'Retry the webhook',
      url: 'https://github.com/example-org/example-repo/pull/41',
      state: 'merged',
      checks: 'passing',
      review: 'approved',
      draft: false,
    },
    mergedAt: '2026-11-15T10:00:00Z',
    parts: [{ kind: 'branch', path: '', branch: 'fix-x', cardId: '', name: '', ignored: 0 }],
    waiting: false,
    ownSession: false,
    ...over,
  };
}

const ready = (items: Leftover[], over: Partial<Leftovers> = {}): Leftovers => ({ state: 'ready', asOf: '2026-11-17T10:00:00Z', items, held: [], partial: false, ...over });

describe('readLeftovers', () => {
  it('keeps what it can show and drops the rest', () => {
    const raw = {
      state: 'ready',
      asOf: '2026-11-17T10:00:00Z',
      partial: true,
      held: [{ reason: 'commits', count: 2 }, { reason: 'newReason', count: 1 }, { reason: 'stash', count: 0 }],
      items: [
        { ...item(), ownSession: true }, // not waiting: no session it waits for
        { ...item(), parts: [{ kind: 'somethingNew', path: 'x' }] }, // no part it knows
        { ...item(), pullRequest: { forge: 'github' as const, host: 'github.com', repo: 'r', number: 'x' } },
        { ...item(), pullRequest: { ...item().pullRequest, forge: 'gitlab' } }, // not GitHub's
        { ...item(), pullRequest: { ...item().pullRequest, forge: 'bitbucket' } }, // a forge it does not know
        { ...item(), pullRequest: { ...item().pullRequest, host: 'ghe.example.com' } }, // another host
        { ...item(), waiting: true, ownSession: true, command: 'rm -rf ~', remoteCommand: 'x' }, // fields this page does not know are dropped
        null,
      ],
    };
    const l = readLeftovers(raw)!;
    expect(l.state).toBe('ready');
    expect(l.partial).toBe(true);
    expect(l.held).toEqual([{ reason: 'commits', count: 2 }]);
    expect(l.items).toHaveLength(2);
    expect(l.items[0].waiting).toBe(false);
    expect(l.items[0].ownSession).toBe(false);
    expect(l.items[1].waiting).toBe(true);
    expect(l.items[1].ownSession).toBe(true);
    expect(Object.keys(l.items[1])).not.toContain('command');
    expect(Object.keys(l.items[1])).not.toContain('remoteCommand');
  });
  it('refuses a list in a state it does not know', () => {
    expect(readLeftovers({ state: 'later', items: [], held: [] })).toBeNull();
    expect(readLeftovers(null)).toBeNull();
  });
});

describe('the client', () => {
  it('reads 401 and 403 as a lost key, other failures as failed', async () => {
    for (const [status, kind] of [
      [401, 'unauthorized'],
      [403, 'unauthorized'],
      [500, 'failed'],
    ] as const) {
      const c = createLeftoversClient(async () => new Response('{}', { status }));
      expect(await c.load()).toMatchObject({ ok: false, kind });
    }
    const down = createLeftoversClient(async () => {
      throw new Error('down');
    });
    expect(await down.load()).toEqual({ ok: false, kind: 'failed', message: 'sitboard did not answer' });
    const ok = createLeftoversClient(async () => new Response(JSON.stringify(ready([item()])), { status: 200 }));
    expect(await ok.load()).toMatchObject({ ok: true, leftovers: { state: 'ready' } });
  });
});

describe('words', () => {
  it('counts held-back work by reason', () => {
    expect(heldLine([])).toBe('');
    expect(
      heldLine([
        { reason: 'commits', count: 1 },
        { reason: 'changes', count: 2 },
      ]),
    ).toBe('3 not listed: 1 has commits after the merge, 2 have uncommitted changes.');
    expect(heldLine([{ reason: 'nested', count: 1 }])).toBe('1 not listed: 1 holds another checkout.');
    expect(heldLine([{ reason: 'nested', count: 2 }])).toBe('2 not listed: 2 hold another checkout.');
  });
  it('says in one line what sitboard checked, and promises nothing', () => {
    const wt: LeftoverPart = { kind: 'worktree', path: '~/projects/harbor/.worktrees/fix-x', branch: '', cardId: '', name: '', ignored: 0 };
    const ses: LeftoverPart = { kind: 'session', path: '', branch: '', cardId: 'claude:x', name: 'a', ignored: 0 };
    const remote: LeftoverPart = { kind: 'remoteBranch', path: '', branch: 'fix-x', cardId: '', name: '', ignored: 0 };
    // a worktree: its files and processes were checked
    expect(verdict(item({ parts: [wt, ...item().parts, remote] }))).toBe('merged · nothing uncommitted · not in use');
    expect(verdict(item({ parts: [wt, ses] }))).toBe('merged · nothing uncommitted · not in use');
    expect(verdict(item({ waiting: true, ownSession: true, parts: [ses, wt] }))).toBe('merged · nothing uncommitted · in use by its session');
    // the session in it is listed under another pull request; the one shown here sits elsewhere
    expect(verdict(item({ waiting: true, parts: [wt] }))).toBe('merged · nothing uncommitted · in use by a finished session');
    expect(verdict(item({ waiting: true, parts: [ses, wt] }))).toBe('merged · nothing uncommitted · in use by a finished session');
    // no worktree: only that the branch is not checked out
    expect(verdict(item())).toBe('merged · branch not checked out');
    expect(verdict(item({ parts: [...item().parts, remote] }))).toBe('merged · branch not checked out · branch still on GitHub');
    expect(verdict(item({ parts: [ses] }))).toBe('merged · finished');
    expect(verdict(item({ parts: [remote] }))).toBe('merged · branch still on GitHub');
    expect(verdict(item({ parts: [ses, remote] }))).toBe('merged · finished · branch still on GitHub');
  });
  it('words the status bar link: leftovers, held back, incomplete', () => {
    expect(leftoversLink({ items: 0, held: 0, partial: false })).toBe('');
    expect(leftoversLink({ items: 1, held: 0, partial: false })).toBe('1 leftover');
    expect(leftoversLink({ items: 2, held: 0, partial: false })).toBe('2 leftovers');
    expect(leftoversLink({ items: 0, held: 4, partial: false })).toBe('0 leftovers · 4 held');
    expect(leftoversLink({ items: 0, held: 0, partial: true })).toBe('0 leftovers · incomplete');
    expect(leftoversLink({ items: 3, held: 1, partial: true })).toBe('3 leftovers · 1 held · incomplete');
    // an older server's plain number, or nonsense: nothing to show
    expect(leftoversLink(2 as unknown as LeftoversSummary)).toBe('');
    expect(leftoversLink(undefined)).toBe('');
    expect(leftoversLink({ items: -1, held: 1.5, partial: 'yes' } as unknown as LeftoversSummary)).toBe('');
  });
  it('links only to a GitHub pull request', () => {
    expect(prHref(item())).toBe('https://github.com/example-org/example-repo/pull/41');
    expect(prHref(item({ pullRequest: { ...item().pullRequest, url: 'javascript:alert(1)' } }))).toBe('');
    // An address that is not the one for the row's own repository and number.
    expect(prHref(item({ pullRequest: { ...item().pullRequest, url: 'https://github.com/other-org/other-repo/pull/41' } }))).toBe('');
    expect(prHref(item({ pullRequest: { ...item().pullRequest, url: 'https://github.com/example-org/example-repo/pull/42' } }))).toBe('');
  });
});

function client(loads: LeftoversLoad[]) {
  let n = 0;
  const c: LeftoversClient = {
    async load() {
      n++;
      return loads.length > 1 ? loads.shift()! : loads[0];
    },
  };
  return { client: c, loads: () => n };
}

const summary = (s: number | Partial<LeftoversSummary>): LeftoversSummary => (typeof s === 'number' ? { items: s, held: 0, partial: false } : { items: 0, held: 0, partial: false, ...s });
const snap = (s: number | Partial<LeftoversSummary>) => snapshot([card({ id: 'claude:a' })], [project(P, 'harbor')], { leftovers: summary(s) });

function app(s: number | Partial<LeftoversSummary>, loads: LeftoversLoad[], catchup?: CatchupClient) {
  const f = feedOf({ conn: 'live', snapshot: snap(s) });
  const c = client(loads);
  r = render(App, { feed: f.feed, actions: recordActions().actions, leftovers: c.client, ...(catchup ? { catchup } : {}) });
  return { ...c, push: f.push };
}

const q = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const link = () => [...document.querySelectorAll<HTMLButtonElement>('footer button')].find((b) => /leftover/.test(b.textContent ?? '')) ?? null;
const sheet = () => q('#leftovers-sheet');
async function open() {
  link()!.click();
  await settle();
  await settle();
}

describe('the status bar link', () => {
  it('is there only while there is a leftover, and loads nothing until opened', async () => {
    const c = app(0, [{ ok: true, leftovers: ready([]) }]);
    await settle();
    expect(link()).toBeNull();
    r!.destroy();
    const c2 = app(1, [{ ok: true, leftovers: ready([item()]) }]);
    await settle();
    expect(text(link())).toBe('1 leftover');
    expect(link()!.getAttribute('aria-expanded')).toBe('false');
    expect(c.loads() + c2.loads()).toBe(0);
  });

  it('stays when every candidate is held back or the look was incomplete, and the sheet says so', async () => {
    app({ held: 4 }, [{ ok: true, leftovers: ready([], { held: [{ reason: 'inUse', count: 3 }, { reason: 'nested', count: 1 }] }) }]);
    await settle();
    expect(text(link())).toBe('0 leftovers · 4 held');
    await open();
    expect(text(sheet())).toContain('Nothing to list.');
    expect(text(sheet())).not.toContain('Nothing left over.');
    expect(text(sheet())).toContain('Held back: 4 not listed: 3 are in use, 1 holds another checkout.');
    r!.destroy();
    app({ partial: true }, [{ ok: true, leftovers: ready([], { partial: true }) }]);
    await settle();
    expect(text(link())).toBe('0 leftovers · incomplete');
    await open();
    expect(text(sheet())).toContain('GitHub may not have given every pull request');
  });

  it('goes with the sheet when the next look has nothing to say', async () => {
    const c = app({ held: 1 }, [{ ok: true, leftovers: ready([], { held: [{ reason: 'commits', count: 1 }] }) }]);
    await settle();
    await open();
    expect(sheet()).not.toBeNull();
    c.push({ conn: 'live', snapshot: snap(0) });
    await settle();
    expect(sheet()).toBeNull();
    expect(link()).toBeNull();
  });

  it('is off while the page is older than the server', async () => {
    const c = app(1, [{ ok: true, leftovers: ready([item()]) }]);
    await settle();
    await open();
    expect(sheet()).not.toBeNull();
    c.push({ conn: 'outdated', snapshot: snap(1) });
    await settle();
    expect(sheet()).toBeNull();
    expect(link()).toBeNull();
  });
});

describe('the sheet', () => {
  it('opens as a dialog with its title focused, and Escape gives focus back', async () => {
    const c = app(1, [{ ok: true, leftovers: ready([item()]) }]);
    await settle();
    await open();
    expect(sheet()!.getAttribute('role')).toBe('dialog');
    expect(link()!.getAttribute('aria-expanded')).toBe('true');
    expect(link()!.getAttribute('aria-controls')).toBe('leftovers-sheet');
    expect(document.activeElement?.id).toBe('leftovers-sheet-title');
    expect(c.loads()).toBe(1);
    expect(text(sheet()!.querySelector('h3'))).toContain('harbor');
    expect(text(sheet()!.querySelector('.verdict'))).toBe('merged · branch not checked out');
    expect(sheet()!.querySelector<HTMLAnchorElement>('a.num')!.href).toBe('https://github.com/example-org/example-repo/pull/41');
    sheet()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(sheet()).toBeNull();
    expect(document.activeElement).toBe(link());
  });

  it('closes when focus leaves it', async () => {
    app(1, [{ ok: true, leftovers: ready([item()]) }]);
    await settle();
    await open();
    const outside = document.querySelector<HTMLButtonElement>('nav[aria-label="View"] button')!;
    sheet()!.dispatchEvent(new FocusEvent('focusout', { relatedTarget: outside, bubbles: true }));
    await settle();
    expect(sheet()).toBeNull();
  });

  it('closes when focus leaves the sheet and its trigger, from either: Shift+Tab twice', async () => {
    app(1, [{ ok: true, leftovers: ready([item()]) }]);
    await settle();
    await open();
    const outside = document.querySelector<HTMLButtonElement>('nav[aria-label="View"] button')!;
    // the first Shift+Tab: sheet to trigger, which stays
    sheet()!.dispatchEvent(new FocusEvent('focusout', { relatedTarget: link(), bubbles: true }));
    await settle();
    expect(sheet()).not.toBeNull();
    // the second: from the trigger out
    link()!.dispatchEvent(new FocusEvent('focusout', { relatedTarget: outside, bubbles: true }));
    await settle();
    expect(sheet()).toBeNull();
  });

  it('focuses its title every time it is opened, also when it is opened again while going away', async () => {
    app(1, [{ ok: true, leftovers: ready([item()]) }]);
    await settle();
    await open();
    sheet()!.querySelector<HTMLButtonElement>('button.close')!.focus();
    expect(document.activeElement?.id).not.toBe('leftovers-sheet-title');
    link()!.click(); // closes
    link()!.click(); // opens again before the first has settled
    await settle();
    await settle();
    expect(sheet()).not.toBeNull();
    expect(document.activeElement?.id).toBe('leftovers-sheet-title');
  });

  it('keeps its list when a refresh fails, and drops everything when the key is lost', async () => {
    vi.useFakeTimers();
    try {
      app(1, [
        { ok: true, leftovers: ready([item()]) },
        { ok: false, kind: 'failed', message: 'sitboard did not answer' },
        { ok: true, leftovers: ready([item()]) },
        { ok: false, kind: 'unauthorized' },
      ]);
      await vi.advanceTimersByTimeAsync(10);
      link()!.click();
      await vi.advanceTimersByTimeAsync(50);
      expect(text(sheet())).toContain('#41');
      const at = clock('2026-11-17T10:00:00Z');
      expect(text(sheet()!.querySelector('.asof'))).toBe(`checked ${at}`);
      await vi.advanceTimersByTimeAsync(60_000); // the refresh fails
      expect(text(sheet()!.querySelector('[role=alert]'))).toContain('as it was');
      expect(text(sheet())).toContain('#41'); // the list stays
      expect(text(sheet()!.querySelector('.verdict'))).toBe('merged · branch not checked out');
      expect(text(sheet()!.querySelector('.asof'))).toBe(`checked ${at}, could not refresh`);
      await vi.advanceTimersByTimeAsync(60_000); // it loads again
      expect(sheet()!.querySelector('[role=alert]')).toBeNull();
      expect(text(sheet()!.querySelector('.asof'))).toBe(`checked ${at}`);
      expect(text(sheet())).toContain('#41');
      await vi.advanceTimersByTimeAsync(60_000); // the key is lost
      expect(text(sheet()!.querySelector('[role=alert]'))).toContain('lost its key');
      expect(text(sheet())).not.toContain('#41');
      expect(text(sheet())).not.toContain('harbor');
      expect(text(sheet())).not.toContain('fix-x');
    } finally {
      vi.useRealTimers();
    }
  });

  it('drops everything when the key is lost, also while a newer refresh is still out', async () => {
    vi.useFakeTimers();
    try {
      const f = feedOf({ conn: 'live', snapshot: snap(1) });
      // 1: answers at once. 2: answers 401 only after 3 started. 3: answers
      // with the list after that 401.
      const list = (): LeftoversLoad => ({ ok: true, leftovers: ready([item()]) });
      const answers: (() => Promise<LeftoversLoad>)[] = [
        async () => list(),
        () => new Promise((done) => setTimeout(() => done({ ok: false, kind: 'unauthorized' }), 90_000)),
        () => new Promise((done) => setTimeout(() => done(list()), 60_000)),
      ];
      let n = 0;
      r = render(App, { feed: f.feed, actions: recordActions().actions, leftovers: { load: () => answers[Math.min(n++, 2)]() } });
      await vi.advanceTimersByTimeAsync(10);
      link()!.click();
      await vi.advanceTimersByTimeAsync(50);
      expect(text(sheet())).toContain('#41');
      await vi.advanceTimersByTimeAsync(60_000); // 2 starts
      await vi.advanceTimersByTimeAsync(60_000); // 3 starts
      expect(n).toBe(3);
      expect(text(sheet())).toContain('#41');
      await vi.advanceTimersByTimeAsync(30_000); // 2 answers: the key is lost
      expect(text(sheet()!.querySelector('[role=alert]'))).toContain('lost its key');
      expect(text(sheet())).not.toContain('#41');
      await vi.advanceTimersByTimeAsync(30_000); // 3 answers with the list: dropped
      expect(text(sheet()!.querySelector('[role=alert]'))).toContain('lost its key');
      expect(text(sheet())).not.toContain('#41');
    } finally {
      vi.useRealTimers();
    }
  });

  it('goes at once when Catch-up finds the key lost, and loads nothing more', async () => {
    vi.useFakeTimers();
    try {
      let lost = false;
      const cu: CatchupClient = {
        load: async (): Promise<CatchupLoad> => (lost ? { ok: false, kind: 'unauthorized' } : { ok: false, kind: 'failed', message: 'sitboard did not answer' }),
        markRead: async () => ({ ok: true }),
      } as unknown as CatchupClient;
      const c = app(1, [{ ok: true, leftovers: ready([item()]) }], cu);
      await vi.advanceTimersByTimeAsync(10);
      link()!.click();
      await vi.advanceTimersByTimeAsync(50);
      expect(text(sheet())).toContain('#41');
      expect(c.loads()).toBe(1);
      lost = true;
      await vi.advanceTimersByTimeAsync(59_945); // Catch-up's next poll: 401
      expect(sheet()).toBeNull();
      expect(link()).toBeNull();
      await vi.advanceTimersByTimeAsync(180_000);
      expect(c.loads()).toBe(1);
      expect(document.body.textContent).not.toContain('#41');
    } finally {
      vi.useRealTimers();
    }
  });

  it('goes when an older Catch-up load answers 401 after a newer one started, and stays gone', async () => {
    vi.useFakeTimers();
    try {
      // 1: at once. 2 (at 60 s): 401 only at 150 s, after 3 started.
      // 3 (at 120 s): a plain failure at 200 s, after that 401.
      const failed = (): CatchupLoad => ({ ok: false, kind: 'failed', message: 'sitboard did not answer' });
      const answers: (() => Promise<CatchupLoad>)[] = [
        async () => failed(),
        () => new Promise((done) => setTimeout(() => done({ ok: false, kind: 'unauthorized' }), 90_000)),
        () => new Promise((done) => setTimeout(() => done(failed()), 80_000)),
      ];
      let n = 0;
      const cu = { load: () => answers[Math.min(n++, 2)](), markRead: async () => ({ ok: true }) } as unknown as CatchupClient;
      const c = app(1, [{ ok: true, leftovers: ready([item()]) }], cu);
      await vi.advanceTimersByTimeAsync(10);
      link()!.click();
      await vi.advanceTimersByTimeAsync(50);
      expect(text(sheet())).toContain('#41');
      await vi.advanceTimersByTimeAsync(120_000); // 2 and 3 are out
      expect(n).toBe(3);
      expect(sheet()).not.toBeNull();
      await vi.advanceTimersByTimeAsync(30_000); // 2 answers 401
      expect(sheet()).toBeNull();
      expect(link()).toBeNull();
      await vi.advanceTimersByTimeAsync(60_000); // 3 answers: the lost key stays
      expect(link()).toBeNull();
      const loads = c.loads();
      await vi.advanceTimersByTimeAsync(180_000);
      expect(c.loads()).toBe(loads);
      expect(n).toBe(3); // Catch-up asks nothing more either
    } finally {
      vi.useRealTimers();
    }
  });

  it('shows paths and branch names exactly, and the clone a branch is in', async () => {
    const parts: LeftoverPart[] = [
      { kind: 'worktree', path: '~/projects/harbor/.worktrees/fix  x', branch: '', cardId: '', name: '', ignored: 0 },
      { kind: 'branch', path: '~/projects/harbor', branch: 'fix\u200bx ', cardId: '', name: '', ignored: 0 },
      { kind: 'remoteBranch', path: '', branch: 'fix\u202ex', cardId: '', name: '', ignored: 0 },
    ];
    const twin = item({ projectId: 'p_000000000002', parts: [{ kind: 'branch', path: '~/work/harbor', branch: 'fixx', cardId: '', name: '', ignored: 0 }] });
    app(2, [{ ok: true, leftovers: ready([item({ parts }), twin]) }]);
    await settle();
    await open();
    const [a, b] = [...sheet()!.querySelectorAll('section.pr')];
    const vals = [...a.querySelectorAll('.v')].map((x) => x.textContent);
    expect(vals).toEqual(['~/projects/harbor/.worktrees/fix  x', 'fix\\u{200b}x\\x20', 'branch fix\\u{202e}x still on GitHub']);
    expect(a.querySelector('.where')!.textContent).toBe('in ~/projects/harbor');
    expect(b.querySelector('.v')!.textContent).toBe('fixx');
    expect(b.querySelector('.where')!.textContent).toBe('in ~/work/harbor');
    // shown as written: repeated spaces are not folded when drawn
    for (const el of [...sheet()!.querySelectorAll<HTMLElement>('.v, .where')]) expect(getComputedStyle(el).whiteSpace).toBe('pre-wrap');
  });

  it('says a worktree waits for its session, warns about ignored files, and offers nothing to run', async () => {
    const waiting = item({
      waiting: true,
      ownSession: true,
      parts: [
        { kind: 'session', path: '', branch: '', cardId: 'claude:a', name: 'sample-agent', ignored: 0, idleSince: '2026-11-15T10:00:00Z' },
        { kind: 'worktree', path: '~/projects/harbor/.worktrees/fix-x', branch: '', cardId: '', name: '', ignored: 3 },
        ...item().parts,
      ],
    });
    const remote = item({ pullRequest: { ...item().pullRequest, number: 29, url: 'https://github.com/example-org/example-repo/pull/29' }, parts: [{ kind: 'remoteBranch', path: '', branch: 'gone-x', cardId: '', name: '', ignored: 0 }] });
    app(2, [{ ok: true, leftovers: ready([waiting, remote], { held: [{ reason: 'inUse', count: 1 }], partial: true }) }]);
    await settle();
    await open();
    const [a, b] = [...sheet()!.querySelectorAll('section.pr')];
    expect(text(a.querySelector('.verdict'))).toBe('merged · nothing uncommitted · in use by its session');
    expect(text(a.querySelector('.warn-ign'))).toBe('It holds 3 git-ignored files or folders (such as .env or node_modules).');
    expect(text(a)).toContain('~/projects/harbor/.worktrees/fix-x');
    expect(text(a)).toContain('sample-agent');
    expect(text(b)).toContain('branch gone-x still on GitHub');
    expect(text(b.querySelector('.verdict'))).toBe('merged · branch still on GitHub');
    expect(b.querySelector<HTMLAnchorElement>('a.num')!.href).toMatch(/\/pull\/29$/);
    // A list only: no command, no copy button, no instruction.
    expect(sheet()!.querySelector('code')).toBeNull();
    expect(sheet()!.querySelector('button.copy')).toBeNull();
    expect([...sheet()!.querySelectorAll('button')].map((x) => text(x))).toEqual(['CloseEsc']);
    expect(text(sheet())).not.toMatch(/\bgit \w|\/exit|sitboard leftovers|command|safe|remov(e|ing) it/i);
    expect(text(sheet())).toContain('Held back: 1 not listed: 1 is in use.');
    expect(text(sheet())).toContain('may be incomplete');
    expect(text(sheet())).toContain('sitboard sees only your own processes');
  });

  it('gives every row its own identity: the same pull request in two clones, the same number in two repositories', async () => {
    const other = { ...item().pullRequest, repo: 'example-org/other-repo', url: 'https://github.com/example-org/other-repo/pull/41' };
    const twin = item({ projectId: 'p_000000000002' });
    app(3, [{ ok: true, leftovers: ready([item(), twin, item({ pullRequest: other })]) }]);
    await settle();
    await open();
    const rows = [...sheet()!.querySelectorAll('section.pr')];
    expect(rows).toHaveLength(3);
    const ids = rows.map((r) => r.querySelector('h3')!.id);
    expect(new Set(ids).size).toBe(3);
    rows.forEach((r, i) => expect(r.getAttribute('aria-labelledby')).toBe(ids[i]));
    expect(rows.map((r) => r.querySelector('a.num')!.getAttribute('href'))).toEqual([
      'https://github.com/example-org/example-repo/pull/41',
      'https://github.com/example-org/example-repo/pull/41',
      'https://github.com/example-org/other-repo/pull/41',
    ]);
  });

  it('says why it is empty: off without GitHub, or a load that failed', async () => {
    app(1, [{ ok: true, leftovers: { state: 'off', items: [], held: [], partial: false } }]);
    await settle();
    await open();
    expect(text(sheet())).toContain('--no-github');
    r!.destroy();
    app(1, [{ ok: false, kind: 'failed', message: 'sitboard did not answer' }]);
    await settle();
    await open();
    expect(text(sheet()!.querySelector('[role=alert]'))).toContain('Could not load leftovers: sitboard did not answer.');
  });
});
