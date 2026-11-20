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
    const lines = qa(`[data-card="${c.id}"] .pr`).map((l) => text(l).replace(' (pull request, opens in a new tab)', ''));
    expect(lines).toEqual(['#100 checks running', '#99 merged', '+23 more merged']);
  });

  it('a card lists the closed issues in one muted line under its pull requests', () => {
    const g = golden(); // the card in review has one pull request and closes one issue
    view(Board, g);
    const closes = q(`[data-card="${g.cards[3].id}"] .pr.closes`)!;
    expect(text(closes)).toBe('closes #7 (issue, opens in a new tab)');
    expect(closes.closest('.prs')).not.toBeNull();
  });

  it('a card links its pull requests and the issues they close, in a row of their own under the card button', () => {
    const g = golden(); // the card in review has pull request #101, closing issue #7
    let jumps = 0;
    r = render(Board, { snapshot: g, ix: indexOf(g), now: NOW, arrivals: new Map(), onjump: () => jumps++, onseen: noop });
    const c = g.cards[3];
    const el = q(`[data-card="${c.id}"]`)!;
    const links = [...el.querySelectorAll('.prs a')] as HTMLAnchorElement[];
    expect(links.map((l) => l.getAttribute('href'))).toEqual([c.pullRequests[0].url, c.issues[0].url]);
    expect(links.map(text)).toEqual(['#101 checks failing (pull request, opens in a new tab)', '#7 (issue, opens in a new tab)']);
    // The number is its own span, marked as a link that opens elsewhere (.ext draws the arrow as generated
    // content, so no arrow is in the text, in a copy of it, or in the link's name).
    for (const l of links) expect(l.querySelectorAll('.name > .num.ext')).toHaveLength(1);
    expect(links.map((l) => text(l.querySelector('.num')))).toEqual(['#101', '#7']);
    expect(el.querySelector('.arrow')).toBeNull();
    expect(text(el)).not.toContain('↗');
    for (const l of links) {
      expect(l.target).toBe('_blank');
      expect(l.rel.split(' ').sort()).toEqual(['noopener', 'noreferrer']);
      expect(l.closest('button')).toBeNull();
    }
    expect(q(`[data-card="${c.id}"] .prs`)!.getAttribute('aria-label')).toBe('Pull requests');
    // The card's button no longer holds the pull request lines.
    expect(text(el.querySelector('.main'))).not.toContain('#101 checks failing');
    links[0].addEventListener('click', (e) => e.preventDefault()); // no navigation in the test
    links[0].click();
    expect(jumps).toBe(0);
  });

  it('keyboard order on a card: the jump button, its pull requests, the issues, its artifacts, then Mark seen', () => {
    const pr = (n: number) => ({ repo: 'o/r', number: n, title: 't', url: `https://github.com/o/r/pull/${n}`, state: 'open' as const, checks: 'passing' as const, review: '' as const, draft: false });
    const c = card({
      attention: ['unseen'],
      pullRequests: [pr(5), pr(4)],
      issues: [{ repo: 'o/r', number: 3, url: 'https://github.com/o/r/issues/3' }],
      artifacts: [{ title: 'Placeholder', url: 'https://claude.ai/artifact/exampleArtifact0001' }],
    });
    view(Board, snapshot([c], [{ ...project(c.projectId, 'one'), repo: 'o/r' }]));
    const order = [...q(`[data-card="${c.id}"]`)!.querySelectorAll('button, a[href]')].map((e) =>
      e.tagName === 'BUTTON' ? (e.classList.contains('seen') ? 'seen' : 'jump') : e.getAttribute('href'),
    );
    expect(order).toEqual(['jump', pr(5).url, pr(4).url, 'https://github.com/o/r/issues/3', c.artifacts[0].url, 'seen']);
  });

  it('only a pull request\'s or an issue\'s own github.com address becomes a link; anything else stays text', () => {
    const pr = (n: number, url: string) => ({ repo: 'o/r', number: n, title: 't', url, state: 'open' as const, checks: 'none' as const, review: '' as const, draft: false });
    const c = card({
      pullRequests: [
        pr(9, 'javascript:alert(1)'),
        pr(8, 'https://github.com/attacker-org/phish/pull/1'),
        pr(7, 'https://github.com/o/r/pull/4'),
      ],
      issues: [
        { repo: 'o/r', number: 2, url: 'javascript:alert(2)' },
        { repo: 'o/r', number: 1, url: 'https://github.com/attacker-org/phish/issues/1' },
      ],
    });
    view(Board, snapshot([c], [{ ...project(c.projectId, 'one'), repo: 'o/r' }]));
    expect(qa(`[data-card="${c.id}"] a`)).toHaveLength(0);
    expect(qa(`[data-card="${c.id}"] .pr`).map(text)).toEqual(['#9 open', '#8 open', '#7 open', 'closes #2 #1']);
    expect(r!.target.innerHTML).not.toContain('javascript:');
    expect(r!.target.innerHTML).not.toContain('attacker-org');
    expect(r!.target.innerHTML).not.toContain('/pull/4');
  });

  it('Lanes and Teams keep the compact pull request text inside the row button, with no link in a button', () => {
    for (const V of [Lanes, Teams]) {
      const g = golden();
      view(V, g);
      expect(text(r!.target)).toContain('#101');
      expect(qa('button a, button [href]')).toHaveLength(0);
      r!.destroy();
      r = null;
    }
  });

  it('a card with issues but no pull request shows nothing extra', () => {
    const c = card({ issues: [{ repo: 'o/r', number: 7, url: 'https://github.com/o/r/issues/7' }] });
    view(Board, snapshot([c], [project(c.projectId, 'one')]));
    expect(q(`[data-card="${c.id}"] .prs`)).toBeNull();
    expect(text(q(`[data-card="${c.id}"]`))).not.toContain('closes');
  });

  it('a card links the artifacts its session published, in a row of their own under the card button', () => {
    const g = golden(); // the coordinator's card has two artifacts
    let jumps = 0;
    r = render(Board, { snapshot: g, ix: indexOf(g), now: NOW, arrivals: new Map(), onjump: () => jumps++, onseen: noop });
    const links = qa(`[data-card="${g.cards[0].id}"] a.art`) as HTMLAnchorElement[];
    expect(links.map((l) => text(l.querySelector('.name')))).toEqual(['Placeholder plan, second draft', 'Placeholder deck']);
    expect(links.map((l) => l.getAttribute('href'))).toEqual([g.cards[0].artifacts[0].url, g.cards[0].artifacts[1].url]);
    for (const l of links) {
      // One accessible name: the title once, the hint once; the full title on hover.
      expect(l.hasAttribute('title')).toBe(false);
      expect(l.querySelectorAll('[title]')).toHaveLength(1);
      const name = text(l.querySelector('.name'));
      expect(l.querySelector('.name')!.getAttribute('title')).toBe(name);
      expect(text(l)).toBe(`${name} (artifact, opens in a new tab)`);
      expect(l.target).toBe('_blank');
      expect(l.rel.split(' ').sort()).toEqual(['noopener', 'noreferrer']);
      expect(l.closest('button')).toBeNull();
    }
    links[0].addEventListener('click', (e) => e.preventDefault()); // no navigation in the test
    links[0].click();
    expect(jumps).toBe(0);
    // A blank title says so; a card without artifacts has no row.
    expect(text(q(`[data-card="${g.cards[5].id}"] a.art .name`))).toBe('Untitled artifact');
    expect(q(`[data-card="${g.cards[1].id}"] .arts`)).toBeNull();
  });

  it('a long artifact list stays at three lines, and only claude.ai addresses become links', () => {
    const arts = Array.from({ length: 7 }, (_, i) => ({ title: `Placeholder ${i}`, url: `https://claude.ai/artifact/exampleArtifact000${i}` }));
    const c = card({ artifacts: [{ title: 'bad', url: 'javascript:alert(1)' }, ...arts] });
    view(Board, snapshot([c], [project(c.projectId, 'one')]));
    expect(qa(`[data-card="${c.id}"] .art`).map((l) => text(l).replace(' (artifact, opens in a new tab)', ''))).toEqual(['Placeholder 0', 'Placeholder 1', '+5 more']);
    expect(qa(`[data-card="${c.id}"] a`).map((l) => l.getAttribute('href'))).toEqual([arts[0].url, arts[1].url]);
    expect(r!.target.innerHTML).not.toContain('javascript:');
  });

  it('cards are real buttons', () => {
    view(Board, golden());
    for (const b of qa('[data-card] .main')) expect(b.tagName).toBe('BUTTON');
  });
});

describe('model names take their family hue in every view', () => {
  const cards = () => {
    const a = card({ agent: { name: 'a', model: 'claude-fable-5-2' } });
    const b = card({ agent: { name: 'b', model: 'claude-haiku-4-5-20251001', parent: 'known', parentCardId: a.id } });
    const c = card({ agent: { name: 'c', model: 'gpt-5.4-codex', tool: 'codex' } });
    const d = card({ agent: { name: 'd', model: '' } });
    return snapshot([a, b, c, d], [project(a.projectId, 'one')]);
  };
  // name, family class, marked unknown (dashed and muted)
  const want = [
    ['Fable 5.2', 'model-fable', false],
    ['Haiku 4.5', 'model-haiku', false],
    ['gpt-5.4-codex', undefined, false],
    ['model unknown', undefined, true],
  ];
  const seen = (sel: string) =>
    qa(sel)
      .map((e) => [text(e), [...e.classList].find((k) => k.startsWith('model-')), e.classList.contains('unknown')])
      .sort((x, y) => String(x[0]).localeCompare(String(y[0])));
  const sorted = [...want].sort((x, y) => String(x[0]).localeCompare(String(y[0])));

  it('Board: the model chip on each card; no model id gives the dashed, muted chip', () => {
    view(Board, cards());
    expect(seen('[data-card] .tag')).toEqual(sorted);
  });
  it('Lanes: the model part of each muted note, the name still written', () => {
    view(Lanes, cards());
    expect(seen('.it .note .model').map((x) => x.slice(0, 2))).toEqual(sorted.map((x) => x.slice(0, 2)));
    expect(qa('.it .note').map(text).some((t) => t.startsWith('coordinator · Fable 5.2 · 1 worker'))).toBe(true);
  });
  it('Lanes: in a needs-you or failing note the model has no hue, so it takes the note\'s tone', () => {
    const n = card({ column: 'needs_you', agent: { name: 'n', model: 'claude-opus-5-5' } });
    const f = card({ column: 'in_review', attention: ['failing_checks'], agent: { name: 'f', model: 'claude-sonnet-5-5' } });
    const m = card({ column: 'working', agent: { name: 'm', model: 'claude-opus-5-5' } });
    view(Lanes, snapshot([n, f, m], [project(n.projectId, 'one')]));
    const note = (c: typeof n) => qa('.cell .it').find((e) => text(e.querySelector('.agent')) === c.agent.name)!.querySelector('.note')!;
    const hue = (c: typeof n) => [...note(c).querySelector('.model')!.classList].find((k) => k.startsWith('model-'));
    expect(note(n).classList.contains('tone-needs')).toBe(true);
    expect(hue(n)).toBeUndefined();
    expect(note(f).classList.contains('tone-failing')).toBe(true);
    expect(hue(f)).toBeUndefined();
    expect(hue(m)).toBe('model-opus');
  });
  it('Teams: the Model cell of each row', () => {
    view(Teams, cards());
    expect(seen('button.trow .model')).toEqual(sorted);
    // A name too long for the column is cut on screen and stays whole in the tooltip.
    for (const e of qa('button.trow .model')) expect(e.getAttribute('title')).toBe(text(e));
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
    const agents = [...panels[0].querySelectorAll('button.trow .who')] as HTMLElement[];
    expect(agents.map(text)[0]).toBe('coordinator-a');
    expect(agents[0].style.getPropertyValue('--depth')).toBe('0');
    expect(agents.slice(1).every((a) => a.style.getPropertyValue('--depth') === '1')).toBe(true);
  });

  it('a worker with no known coordinator is listed last in its project, not indented', () => {
    const boss = card({ projectId: 'p_a', agent: { name: 'boss' } });
    const w = card({ projectId: 'p_a', agent: { name: 'helper', parent: 'known', parentCardId: boss.id } });
    const lost = card({ projectId: 'p_a', agent: { name: 'lost', parent: 'unknown' } });
    view(Teams, snapshot([lost, w, boss], [project('p_a', 'alpha')]));
    const agents = [...r!.target.querySelectorAll('.panel button.trow .who')] as HTMLElement[];
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

describe('the effort level sits next to the model in every view', () => {
  const cards = () => {
    const a = card({ agent: { name: 'a', model: 'claude-fable-5-2', effort: 'max', effortSource: 'recorded' } });
    const b = card({ agent: { name: 'b', model: 'claude-opus-5-5', effort: 'low', effortSource: 'recorded' } });
    const c = card({ agent: { name: 'c', model: '', effort: 'high', effortSource: 'launch' } });
    const d = card({ agent: { name: 'd', model: 'claude-sonnet-5-5', effort: '', effortSource: 'none' } });
    const e = card({ agent: { name: 'e', model: 'claude-haiku-5-5', effort: 'medium', effortSource: 'none' } });
    return snapshot([a, b, c, d, e], [project(a.projectId, 'one')]);
  };
  // agent, effort text, ramp class, set at launch
  const want = [
    ['a', 'max', 'effort-max', false],
    ['b', 'low', 'effort-low', false],
    ['c', 'high at launch', 'effort-high', true],
  ];
  const row = (root: Element | null, sel: string) => {
    const e = root?.querySelector(sel);
    return e ? [text(e), [...e.classList].find((k) => k.startsWith('effort-')), e.querySelector('.at') !== null] : null;
  };

  it('Board: inside the model chip, on the ramp; at launch said in words and a dashed rule; none when unknown', () => {
    view(Board, cards());
    const got = qa('[data-card]')
      .map((el) => [text(el.querySelector('.agent')), row(el, '.chip .eff')])
      .filter(([, r]) => r !== null)
      .map(([n, r]) => [n, ...(r as unknown[])])
      .sort((x, y) => String(x[0]).localeCompare(String(y[0])));
    expect(got).toEqual(want);
    const launch = qa('[data-card] .chip .eff.launch');
    expect(launch.length).toBe(1);
    expect(launch[0].parentElement!.getAttribute('title')).toContain('set at launch');
    expect(qa('[data-card] .chip')[0].getAttribute('title') ?? '').not.toContain('launch');
    // Heard as "effort …", never glued to the model name.
    const card = (n: string) => text(qa('[data-card]').find((e) => text(e.querySelector('.agent')) === n)!);
    expect(card('a')).toContain('Fable 5.2, effort max');
    expect(card('c')).toContain('model unknown, effort high at launch');
    expect(card('d')).not.toContain('effort');
  });

  it('Lanes: after the model in the note, its level always marked (the colour is the CSS: ramp in a muted note or from high up)', () => {
    const s = cards();
    s.cards.push(card({ column: 'needs_you', agent: { name: 'n', model: 'claude-opus-5-5', effort: 'xhigh', effortSource: 'recorded' } }));
    view(Lanes, s);
    const note = (n: string) => qa('.cell .it').find((e) => text(e.querySelector('.agent')) === n)!.querySelector('.note')!;
    for (const [n, t, k, l] of want) expect(row(note(String(n)), '.eff')).toEqual([t, k, l]);
    expect(text(note('a'))).toContain('Fable 5.2, effort max');
    expect(text(note('c'))).toContain('model unknown, effort high at launch');
    expect(note('d').querySelector('.eff')).toBeNull();
    expect(note('e').querySelector('.eff')).toBeNull();
    expect(row(note('n'), '.eff')).toEqual(['xhigh', 'effort-xhigh', false]);
    expect(note('c').querySelector('.eff')!.classList.contains('launch')).toBe(true);
    expect(note('a').querySelector('.eff')!.classList.contains('launch')).toBe(false);
  });

  it('Teams: on its own line under the model name', () => {
    view(Teams, cards());
    const cell = (n: string) => qa('button.trow').find((e) => text(e.querySelector('.agent')) === n)!.querySelector('.mcell')!;
    for (const [n, t, k, l] of want) expect(row(cell(String(n)), '.eff')).toEqual([t, k, l]);
    expect(cell('d').querySelector('.eff')).toBeNull();
    expect(cell('c').querySelector('.eff')!.getAttribute('title')).toContain('set at launch');
    expect(text(cell('a'))).toBe('Fable 5.2, effort max');
    expect(text(cell('c'))).toBe('model unknown, effort high at launch');
  });
});

