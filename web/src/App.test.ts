import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import App from './App.svelte';
import { card, golden, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle, text } from './test/render';
import type { Snapshot } from './contract.gen';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});
beforeEach(() => localStorage.clear());

function app(s: Snapshot | null, conn: Parameters<typeof feedOf>[0]['conn'] = 'live', answers = {}) {
  const f = feedOf({ conn, snapshot: s });
  const a = recordActions(answers);
  r = render(App, { feed: f.feed, actions: a.actions });
  return { ...r, ...f, calls: a.calls };
}

const q = (sel: string) => r!.target.querySelector(sel);
const qa = (sel: string) => [...r!.target.querySelectorAll(sel)];

describe('shell', () => {
  it('the status bar shows the version from the snapshot, and nothing before the first one', () => {
    const a = app(null, 'connecting');
    const name = () => text(q('footer button.me .name'));
    expect(name()).toBe('sitboard');
    a.push({ conn: 'live', snapshot: snapshot([], [], { build: { version: '0.1.0', commit: 'abc1234', os: 'linux', arch: 'amd64' } }) });
    expect(name()).toBe('sitboard 0.1.0');
    a.push({ conn: 'live', snapshot: golden() }); // a development build
    expect(name()).toBe('sitboard dev');
  });

  it('switcher has Board, Lanes, Teams; the current one has aria-current', () => {
    app(golden());
    const tabs = qa('nav[aria-label="View"] button');
    expect(tabs.map(text)).toEqual(['Board', 'Lanes', 'Teams']);
    expect(tabs[0].getAttribute('aria-current')).toBe('page');
    expect(tabs[1].hasAttribute('aria-current')).toBe(false);
  });

  it('switching views is remembered under "sitboard.view"', async () => {
    app(golden());
    (qa('nav[aria-label="View"] button')[2] as HTMLButtonElement).click();
    await settle();
    expect(localStorage.getItem('sitboard.view')).toBe('teams');
    expect(qa('nav[aria-label="View"] button')[2].getAttribute('aria-current')).toBe('page');
    expect(q('aside[aria-label="Your queue"]')).not.toBeNull();
  });

  it('opens the remembered view', () => {
    localStorage.setItem('sitboard.view', 'lanes');
    app(golden());
    expect(q('section[aria-label="All projects"]')).not.toBeNull();
  });

  it('has no Catch-up or Leftovers button in this cut', () => {
    app(golden());
    expect(text(q('header'))).not.toMatch(/catch-up|leftovers/i);
  });

  it('header shows agent and project counts', () => {
    app(golden());
    expect(text(q('header'))).toContain('7 agents · 2 projects');
  });

  it('status bar shows one entry per source with its state word', () => {
    app(golden());
    const items = qa('footer ul[aria-label="Sources"] li').map(text);
    expect(items).toEqual([
      'claude code 5 live', // the sixth Claude session has ended
      'codex reading… · status unknown for Codex sessions',
      'agterm found',
      'git found',
      'github failing · not logged in',
      '3 plain shells hidden',
    ]);
  });

  it('a missing or failing source shows its notice', () => {
    const s = golden();
    s.sources = s.sources.map((x) =>
      x.kind === 'agterm' ? { ...x, state: 'missing', detail: '' } : x.kind === 'github' ? { ...x, state: 'off', detail: '' } : x,
    );
    app(s);
    const items = qa('footer li').map(text);
    expect(items).toContain('agterm not found');
    expect(items).toContain('github off');
  });

  it('zero cards shows the empty state and names which sources were found', () => {
    app(
      snapshot([], [], {
        sources: [
          { kind: 'claude', state: 'ok', version: '2', detail: '', tested: true, ready: true },
          { kind: 'codex', state: 'missing', version: '', detail: '', tested: false, ready: true },
          { kind: 'github', state: 'off', version: '', detail: '', tested: false, ready: true },
        ],
        plainShells: 2,
      }),
    );
    const t = text(q('.empty'));
    expect(t).toContain('No agent sessions running');
    expect(t).toMatch(/Found\s*claude code/);
    expect(t).toMatch(/Not found\s*codex/);
    expect(t).toMatch(/Off\s*github/);
    expect(t).toContain('2 plain shells are open');
  });

  it('while a source is on its first pass it says so, never "no sessions"', () => {
    const reading: Snapshot['sources'][number] = { kind: 'claude', state: 'ok', version: '2', detail: '', tested: true, ready: false };
    const h = app(snapshot([], [], { sources: [reading] }));
    expect(text(r!.target)).toContain('Reading sessions…');
    expect(text(r!.target)).not.toContain('No agent sessions');
    h.push({ conn: 'live', snapshot: snapshot([], [], { sources: [{ ...reading, ready: true }] }) });
    expect(text(r!.target)).toContain('No agent sessions running');
  });

  it('a source on its first pass does not hide the cards already found', () => {
    const s = golden(); // the golden Codex source is still on its first pass
    expect(s.sources.some((x) => !x.ready)).toBe(true);
    app(s);
    expect(qa('[data-card]')).toHaveLength(7);
  });

  it('plain shell count is not shown when zero', () => {
    const s = golden();
    s.plainShells = 0;
    app(s);
    expect(text(q('footer'))).not.toContain('plain');
  });

  it('shows loading before the first snapshot', () => {
    app(null, 'connecting');
    expect(text(r!.target)).toContain('Connecting');
  });

  it('a 401 shows the re-entry message, not an empty board', () => {
    app(null, 'unauthorized');
    expect(text(r!.target)).toContain('This tab lost its key');
    // The link is private and works for as long as this run is alive.
    expect(text(r!.target)).toContain('sitboard opens with a private link that works while this run is alive, and forgets it when it stops.');
    expect(text(r!.target)).not.toMatch(/one[- ]time|single[- ]use/i);
    expect(text(r!.target)).toContain('Open the link sitboard printed');
  });

  it('a newer version shows "reload the page"', () => {
    app(null, 'outdated');
    expect(text(r!.target)).toContain('Reload the page');
  });

  it('cannot reach sitboard shows why and keeps trying', () => {
    app(null, 'unreachable');
    expect(text(r!.target)).toContain('Cannot reach sitboard');
  });

  it('a snapshot with a lower seq after a reconnect is taken as the truth', () => {
    const first = golden();
    first.seq = 40;
    const h = app(first);
    const restarted = snapshot([], [], { seq: 0, sources: first.sources.map((x) => ({ ...x, ready: true })) });
    h.push({ conn: 'live', snapshot: restarted });
    expect(qa('[data-card]')).toHaveLength(0);
    expect(text(r!.target)).toContain('No agent sessions running');
  });

  it('while not live the board says the connection is lost, as of when, and greys out', () => {
    const h = app(golden());
    expect(q('.lost')).toBeNull();
    expect(q('.content')!.classList.contains('frozen')).toBe(false);
    h.push({ conn: 'reconnecting', snapshot: golden() });
    expect(text(q('.lost'))).toMatch(/^Connection lost · board as of \d\d:\d\d · retrying$/);
    expect(q('.content')!.classList.contains('frozen')).toBe(true);
    h.push({ conn: 'live', snapshot: golden() });
    expect(q('.lost')).toBeNull();
    expect(q('.content')!.classList.contains('frozen')).toBe(false);
  });

  it('the empty state does not stay up as if current while the connection is lost', () => {
    const s = snapshot([], [], { sources: [{ kind: 'claude', state: 'ok', version: '', detail: '', tested: true, ready: true }] });
    const h = app(s);
    h.push({ conn: 'reconnecting', snapshot: s });
    expect(text(q('.lost'))).toContain('Connection lost');
    expect(q('.content')!.classList.contains('frozen')).toBe(true);
  });

  it('a failing session source is named, and the page does not claim there are no sessions', () => {
    app(
      snapshot([], [], {
        sources: [
          { kind: 'claude', state: 'failing', version: '2', detail: 'cannot read the session folder', tested: true, ready: true },
          { kind: 'codex', state: 'missing', version: '', detail: '', tested: false, ready: true },
        ],
      }),
    );
    const t = text(q('.empty'));
    expect(t).toContain('Cannot read the agent sessions');
    expect(t).toMatch(/claude code failing\s*· cannot read the session folder/);
    expect(t).not.toContain('No agent sessions');
    expect(t).not.toContain('Neither Claude Code nor Codex');
  });

  it('a source version is cleaned before it reaches the title', () => {
    const s = golden();
    s.sources[0].version = '2.1\u202e.0';
    app(s);
    expect(q('footer li')!.getAttribute('title')).toBe('claude code 2.1.0');
  });

  it('reconnecting keeps the last board and says so', () => {
    const h = app(golden());
    h.push({ conn: 'reconnecting', snapshot: golden() });
    expect(text(q('header [role="status"]'))).toBe('Reconnecting');
    expect(qa('[data-card]').length).toBe(7);
  });
});

describe('actions', () => {
  it('activating a card posts jump with its cardId and marks it seen locally', async () => {
    const s = golden();
    const h = app(s);
    const target = s.cards[0];
    const btn = q(`[data-card="${target.id}"] .main`) as HTMLButtonElement;
    expect(q(`[data-card="${target.id}"] .seen`)).not.toBeNull();
    btn.click();
    await settle();
    expect(h.calls).toEqual([['jump', target.id]]);
    expect(q(`[data-card="${target.id}"] .seen`)).toBeNull();
  });

  it('"Mark seen" posts seen with the cardId, does not jump, and is a sibling of the jump button', async () => {
    const s = golden();
    const h = app(s);
    const id = s.cards[0].id;
    const seen = q(`[data-card="${id}"] .seen`) as HTMLButtonElement;
    expect(seen.closest('.main')).toBeNull();
    expect(seen.parentElement).toBe(q(`[data-card="${id}"] .main`)!.parentElement);
    seen.click();
    await settle();
    expect(h.calls).toEqual([['seen', id]]);
  });

  it('"Mark seen" is absent on cards without the "unseen" reason', () => {
    const s = golden();
    app(s);
    for (const c of s.cards.filter((x) => !x.attention.includes('unseen'))) {
      expect(q(`[data-card="${c.id}"] .seen`)).toBeNull();
    }
  });

  it('a 409 from jump (no terminal) shows the folder and session name with a copy control', async () => {
    const s = golden();
    app(s, 'live', { jump: { ok: false, kind: 'no_terminal' } });
    (q(`[data-card="${s.cards[1].id}"] .main`) as HTMLButtonElement).click();
    await settle();
    const n = text(q('.notice'));
    expect(n).toContain('~/projects/alpha/.worktrees/worker-a');
    expect(n).toContain(s.cards[1].agent.name);
    expect(qa('.notice button').map(text)).toContain('Copy');
  });

  it('when agterm is absent a card shows folder and name to copy instead of jump', async () => {
    const s = golden();
    s.agtermPresent = false;
    const h = app(s);
    const btn = q(`[data-card="${s.cards[1].id}"] .main`) as HTMLButtonElement;
    expect(text(btn.querySelector('.visually-hidden'))).toBe('Show where it runs:');
    btn.click();
    await settle();
    expect(h.calls).toEqual([]);
    expect(text(q('.notice'))).toContain('agterm not found');
  });

  it('the action result is announced in a live region that is always there, and Escape closes it', async () => {
    const s = golden();
    app(s, 'live', { jump: { ok: false, kind: 'no_terminal' } });
    const region = q('[role="status"][aria-live="polite"]')!;
    expect(region).not.toBeNull();
    expect(text(region)).toBe('');
    (q(`[data-card="${s.cards[1].id}"] .main`) as HTMLButtonElement).click();
    await settle();
    expect(q('[role="status"][aria-live="polite"]')).toBe(region);
    expect(text(region)).toContain('Not in agterm. Folder ~/projects/alpha/.worktrees/worker-a.');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await settle();
    expect(q('.notice')).toBeNull();
    expect(text(region)).toBe('');
  });

  it('a card keeps its text for screen readers: no aria-label, an action prefix inside', () => {
    const s = golden();
    app(s);
    const btn = q(`[data-card="${s.cards[0].id}"] .main`)!;
    expect(btn.hasAttribute('aria-label')).toBe(false);
    expect(text(btn)).toMatch(/^Jump to agterm: coordinator-a/);
    expect(r!.target.querySelectorAll('button[aria-label^="Jump"], button[aria-label^="Show"]')).toHaveLength(0);
  });

  it('two identical pull requests on one card do not break the page', () => {
    const pr = { repo: 'o/r', number: 4, title: 't', url: 'https://github.com/o/r/pull/4', state: 'open' as const, checks: 'passing' as const, review: '' as const, draft: false };
    const c = card({ pullRequests: [pr, pr] });
    app(snapshot([c], [project(c.projectId, 'one')]));
    expect(qa(`[data-card="${c.id}"] .pr`)).toHaveLength(2);
  });

  it('a failed action shows what failed and a retry', async () => {
    const s = golden();
    const h = app(s, 'live', { jump: { ok: false, kind: 'failed', message: 'sitboard answered 500' } });
    (q(`[data-card="${s.cards[1].id}"] .main`) as HTMLButtonElement).click();
    await settle();
    expect(text(q('.notice'))).toContain('failed: sitboard answered 500');
    const retry = qa('.notice button').find((b) => text(b) === 'Retry') as HTMLButtonElement;
    retry.click();
    await settle();
    expect(h.calls.filter((c) => c[0] === 'jump')).toHaveLength(2);
  });

  it('Escape in the About panel closes the panel only, not an open action notice', async () => {
    const s = golden();
    app(s, 'live', { jump: { ok: false, kind: 'failed', message: 'sitboard answered 500' } });
    (q(`[data-card="${s.cards[1].id}"] .main`) as HTMLButtonElement).click();
    await settle();
    expect(q('.notice')).not.toBeNull();
    (q('footer button.me') as HTMLButtonElement).click();
    await settle();
    const panel = q('[role="dialog"]')!;
    panel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(q('[role="dialog"]')).toBeNull();
    expect(q('.notice')).not.toBeNull();
  });

  it('the status bar is raised above the action notice while About is open', async () => {
    app(golden());
    const footer = q('footer')!;
    expect(footer.classList.contains('raised')).toBe(false);
    (q('footer button.me') as HTMLButtonElement).click();
    await settle();
    expect(footer.classList.contains('raised')).toBe(true);
  });

  it('a 401 from an action shows the re-entry message', async () => {
    const s = golden();
    app(s, 'live', { seen: { ok: false, kind: 'unauthorized' } });
    (q(`[data-card="${s.cards[0].id}"] .seen`) as HTMLButtonElement).click();
    await settle();
    expect(text(r!.target)).toContain('This tab lost its key');
  });
});

describe('untrusted text', () => {
  it('a title with markup is rendered as text', () => {
    const c = card({ title: '<img src=x onerror="alert(1)"><b>bold</b>' });
    app(snapshot([c], [project(c.projectId, 'one')]));
    const title = q(`[data-card="${c.id}"] .title`)!;
    expect(title.textContent).toBe('<img src=x onerror="alert(1)"><b>bold</b>');
    expect(r!.target.querySelector('img')).toBeNull();
    expect(r!.target.querySelector('b')).toBeNull();
  });

  it('a 10,000-character title is clipped and the card keeps its width', () => {
    const c = card({ title: 'x'.repeat(10_000) });
    app(snapshot([c], [project(c.projectId, 'one')]));
    const title = q(`[data-card="${c.id}"] .title`)!;
    expect(title.textContent!.length).toBeLessThanOrEqual(501);
    expect(title.classList.contains('title')).toBe(true); // two-line clamp and overflow-wrap live on .title
  });

  it('control and right-to-left characters do not reorder neighbouring text', () => {
    const c = card({ agent: { name: 'abc\u202eevil\u0007' } });
    app(snapshot([c], [project(c.projectId, 'one')]));
    const name = q(`[data-card="${c.id}"] .agent`)!;
    expect(name.textContent).toBe('abcevil');
    expect(name.classList.contains('u')).toBe(true); // unicode-bidi: isolate
  });
});
