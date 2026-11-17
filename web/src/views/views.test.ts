import { afterEach, describe, expect, it } from 'vitest';
import Board from './Board.svelte';
import Lanes from './Lanes.svelte';
import Teams from './Teams.svelte';
import { indexOf } from '../data/derive';
import { card, golden, project, snapshot } from '../test/fixtures';
import { render, text } from '../test/render';
import type { Snapshot } from '../contract.gen';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});
const NOW = Date.parse('2026-11-17T10:00:00Z');
const noop = () => {};

function view(C: typeof Board | typeof Lanes | typeof Teams, s: Snapshot) {
  r = render(C, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
  return r;
}
const q = (sel: string) => r!.target.querySelector(sel);
const qa = (sel: string) => [...r!.target.querySelectorAll(sel)];

describe('Board', () => {
  it('six columns in order, each with its count and one-line explanation', () => {
    view(Board, golden());
    const heads = qa('section[data-column] .head').map(text);
    expect(heads).toEqual(['Needs you 2', 'Working 1', 'With coordinator 1', 'In review 1', 'Done · 24h 1', 'Idle 1']);
    for (const s of qa('section[data-column]')) expect(text(s.querySelector('.hint')).length).toBeGreaterThan(0);
  });

  it('an empty column explains what lands there', () => {
    const c = card({ column: 'working' });
    view(Board, snapshot([c], [project(c.projectId, 'one')]));
    expect(text(q('section[data-column="in_review"] .empty'))).toContain('pull requests is still open');
    expect(q('section[data-column="working"] .empty')).toBeNull();
  });

  it('a card shows agent, time in state, title, project, model tag, coordinator line, pull requests and subagents', () => {
    const g = golden();
    view(Board, g);
    const boss = text(q(`[data-card="${g.cards[0].id}"]`));
    expect(boss).toContain('coordinator-a');
    expect(boss).toContain('5m');
    expect(boss).toContain('alpha');
    expect(boss).toContain('Opus 4.7');
    expect(boss).toContain('coordinator · 4 workers');
    expect(boss).toContain('2 subagents');
    const review = text(q(`[data-card="${g.cards[3].id}"]`));
    expect(review).toContain('Placeholder pull request title');
    expect(review).toContain('for coordinator-a');
    expect(review).toContain('#101 checks failing');
    expect(review).toContain('Checks failing on #101');
    const codex = text(q(`[data-card="${g.cards[6].id}"]`));
    expect(codex).toContain('model unknown');
    expect(codex).toContain('status unknown');
    expect(codex).toContain('coordinator: unknown');
    expect(text(q(`[data-card="${g.cards[1].id}"]`))).toContain('Stale · no activity for 25m');
  });

  it('a long pull request list stays at three lines on the card', () => {
    const many = Array.from({ length: 25 }, (_, i) => ({
      repo: 'o/r',
      number: 100 - i,
      title: 't',
      url: `https://github.com/o/r/pull/${100 - i}`,
      state: (i === 0 ? 'open' : 'merged') as 'open' | 'merged',
      checks: 'running' as const,
      review: '' as const,
      draft: false,
    }));
    const c = card({ pullRequests: many });
    view(Board, snapshot([c], [project(c.projectId, 'one')]));
    const lines = qa(`[data-card="${c.id}"] .pr`).map(text);
    expect(lines).toEqual(['#100 checks running', '#99 merged', '+23 more merged']);
  });

  it('a card lists the closed issues in one muted line under its pull requests', () => {
    const g = golden(); // the card in review has one pull request and closes one issue
    view(Board, g);
    const closes = q(`[data-card="${g.cards[3].id}"] .pr.closes`)!;
    expect(text(closes)).toBe('closes #7');
    expect(closes.closest('.prs')).not.toBeNull();
  });

  it('a card with issues but no pull request shows nothing extra', () => {
    const c = card({ issues: [{ repo: 'o/r', number: 7, url: 'https://github.com/o/r/issues/7' }] });
    view(Board, snapshot([c], [project(c.projectId, 'one')]));
    expect(q(`[data-card="${c.id}"] .prs`)).toBeNull();
    expect(text(q(`[data-card="${c.id}"]`))).not.toContain('closes');
  });

  it('cards are real buttons', () => {
    view(Board, golden());
    for (const b of qa('[data-card] .main')) expect(b.tagName).toBe('BUTTON');
  });
});

describe('Lanes', () => {
  it('one row per project with its path', () => {
    view(Lanes, golden());
    const names = qa('.pcell .pname').map(text);
    expect(names).toEqual(['alpha', 'beta']);
    expect(qa('.pcell .path').map(text)).toEqual(['~/projects/alpha', '~/projects/beta']);
  });

  it('overview: each project row shows its four counts with their words', () => {
    view(Lanes, golden());
    expect(text(qa('.row:not(.headrow) .pcell')[0])).toContain('1 working · 1 needs you · 1 in review · 1 stale');
  });

  it('the header totals match Snapshot.Counts', () => {
    const g = golden();
    view(Lanes, g);
    expect(qa('section[aria-label="All projects"] li').map(text)).toEqual([
      `${g.counts.working} working`,
      `${g.counts.needsYou} needs you`,
      `${g.counts.inReview} in review`,
      `${g.counts.stale} stale`,
    ]);
  });

  it('state columns follow Board order; empty ones hide except Needs you and Working', () => {
    view(Lanes, golden());
    expect(qa('.headrow .colhead .label').map(text)).toEqual([
      'Needs you',
      'Working',
      'With coordinator',
      'In review',
      'Done · 24h',
      'Idle',
    ]);
    r!.destroy();
    const c = card({ column: 'idle' });
    view(Lanes, snapshot([c], [project(c.projectId, 'one')]));
    expect(qa('.headrow .colhead .label').map(text)).toEqual(['Needs you', 'Working', 'Idle']);
  });

  it('the strip lists every card with an attention reason, with its reason as words', () => {
    view(Lanes, golden());
    const items = qa('.strip .item').map(text);
    expect(items).toHaveLength(4);
    expect(items[0]).toContain('Blocked on a question or a permission');
    expect(items.join(' ')).toContain('Finished a turn you have not seen');
    expect(items.join(' ')).toContain('Checks failing on #101');
    expect(items.join(' ')).toContain('Stale · no activity for 25m');
  });

  it('notes are never cut: no ellipsis on a note', () => {
    view(Lanes, golden());
    expect(qa('.note').length).toBeGreaterThan(0);
  });
});

describe('Teams', () => {
  it('one panel per coordinator, its workers indented under it', () => {
    const g = golden();
    view(Teams, g);
    const panels = qa('.panel');
    expect(panels.map((p) => text(p.querySelector('.ptitle')))).toEqual(['coordinator-a', 'Solo agents']);
    expect(text(panels[0].querySelector('.pmeta'))).toContain('~/projects/alpha');
    expect(text(panels[0].querySelector('.projs'))).toBe('alpha');
    const agents = [...panels[0].querySelectorAll('button.trow .agent')] as HTMLElement[];
    expect(agents.map(text)[0]).toBe('coordinator-a');
    expect(agents[0].style.getPropertyValue('--depth')).toBe('0');
    expect(agents.slice(1).every((a) => a.style.getPropertyValue('--depth') === '1')).toBe(true);
  });

  it('a worker with no known coordinator is listed last in its project, not indented', () => {
    const boss = card({ projectId: 'p_a', agent: { name: 'boss' } });
    const w = card({ projectId: 'p_a', agent: { name: 'helper', parent: 'known', parentCardId: boss.id } });
    const lost = card({ projectId: 'p_a', agent: { name: 'lost', parent: 'unknown' } });
    view(Teams, snapshot([lost, w, boss], [project('p_a', 'alpha')]));
    const agents = [...r!.target.querySelectorAll('.panel button.trow .agent')] as HTMLElement[];
    expect(agents.map(text)).toEqual(['boss', 'helper', 'lost']);
    expect(agents[2].style.getPropertyValue('--depth')).toBe('0');
  });

  it('an unknown coordinator, or one not on the board, is said in the row, not guessed', () => {
    const lost = card({ agent: { name: 'lost', parent: 'unknown' } });
    const orphan = card({ agent: { name: 'orphan', parent: 'known', parentCardId: 'claude:gone' } });
    view(Teams, snapshot([lost, orphan], [project(lost.projectId, 'one')]));
    const rows = qa('.panel button.trow').map(text);
    expect(rows.find((t) => t.includes('lost'))).toContain('coordinator: unknown');
    expect(rows.find((t) => t.includes('orphan'))).toContain('coordinator not on the board');
    expect(text(q('.panel .pmeta'))).toContain('no coordinator on the board');
  });

  it('agents without a coordinator and without workers go to "Solo agents"', () => {
    const g = golden();
    view(Teams, g);
    const solo = qa('.panel')[1];
    expect([...solo.querySelectorAll('.agent')].map(text)).toEqual(['solo-a', 'worker-e']);
  });

  it('the queue has "Needs you" and "Waiting for your message" (Idle, longest first)', () => {
    const a = card({ column: 'idle', agent: { name: 'recent', statusSince: '2026-11-17T09:00:00Z' } });
    const b = card({ column: 'idle', agent: { name: 'oldest', statusSince: '2026-11-14T09:00:00Z' } });
    view(Teams, snapshot([a, b], [project(a.projectId, 'one')]));
    const heads = qa('aside h2').map(text);
    expect(heads).toEqual(['Needs you', 'Waiting for your message']);
    const waiting = qa('aside section')[1];
    expect([...waiting.querySelectorAll('.agent')].map(text)).toEqual(['oldest', 'recent']);
  });

  it('the pull requests cell is one short line: open ones with checks, then "+N merged"', () => {
    const prs = [
      { repo: 'o/r', number: 60, title: 't', url: 'u60', state: 'open' as const, checks: 'failing' as const, review: '' as const, draft: false },
      ...Array.from({ length: 40 }, (_, i) => ({ repo: 'o/r', number: 59 - i, title: 't', url: `u${59 - i}`, state: 'merged' as const, checks: 'passing' as const, review: '' as const, draft: false })),
    ];
    const c = card({ pullRequests: prs, column: 'working' });
    const s = snapshot([c], [project(c.projectId, 'one')]);
    view(Teams, s);
    expect(text(q('button.trow .prs'))).toBe('#60 open · checks failing · +40 merged');
    r!.destroy();
    view(Lanes, s);
    expect(text(q('.note'))).toContain('#60 open · checks failing · +40 merged');
  });

  it('two coordinators with the same name give distinct section labels', () => {
    const a = card({ projectId: 'p_a', agent: { name: '' } });
    const b = card({ projectId: 'p_b', agent: { name: '' } });
    const wa = card({ projectId: 'p_a', agent: { parent: 'known', parentCardId: a.id } });
    const wb = card({ projectId: 'p_b', agent: { parent: 'known', parentCardId: b.id } });
    view(Teams, snapshot([a, b, wa, wb], [project('p_a', 'alpha'), project('p_b', 'bravo')]));
    const labels = qa('section.panel').map((p) => p.getAttribute('aria-label'));
    expect(new Set(labels).size).toBe(labels.length);
    expect(labels).toContain('unnamed session · ~/projects/alpha');
  });

  it('Lanes and Teams append the closed issues after the pull request part', () => {
    const g = golden();
    view(Teams, g);
    const cell = [...r!.target.querySelectorAll('button.trow .prs')].find((e) => text(e).includes('#101'))!;
    expect(text(cell)).toBe('#101 open · checks failing · closes #7');
    expect(cell.querySelector('.part.closes')).not.toBeNull();
    r!.destroy();
    view(Lanes, g);
    const note = qa('.note').find((e) => text(e).includes('#101'))!;
    expect(text(note)).toContain('#101 open · checks failing · closes #7');
    expect(note.classList.contains('tone-failing')).toBe(true);
    expect(text(note.querySelector('.closes')!)).toBe('closes #7');
  });

  it('rows are buttons reachable by keyboard', () => {
    view(Teams, golden());
    const rows = qa('button.trow') as HTMLButtonElement[];
    expect(rows).toHaveLength(7);
    for (const b of rows) {
      expect(b.tabIndex).toBe(0);
      expect(b.hasAttribute('aria-label')).toBe(false);
      expect(text(b.querySelector('.visually-hidden'))).toMatch(/^(Jump to agterm|Show where it runs):$/);
    }
  });

  it('unknown status shows the words "status unknown" as its state', () => {
    view(Teams, golden());
    expect(qa('button.trow .state').map(text)).toContain('Status unknown');
  });
});
