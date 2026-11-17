import { describe, expect, it } from 'vitest';
import { card, project, snapshot } from '../test/fixtures';
import type { Card, Column, Project } from '../contract.gen';
import { ALL, bandScrolls, countsOf, filterCards, grouping, partsOf, railGroups, selName, tileBand, tops, validSel, type Sel } from './groups';
import { indexOf, lanes, projectLabel, projectName } from './derive';

// A project in the grouping shape: parentId "" for a top-level project, area the folder above it.
const gp = (id: string, name: string, parentId = '', area = '', counts: Partial<Project['counts']> = {}): Project =>
  ({ ...project(id, name, counts), parentId, area });
const at = (projectId: string, column: Column = 'working', over: Partial<Card> = {}) => card({ projectId, column, ...over });

// One area with two top-level projects (one with parts), one area holding a single project, and one with no area.
const P = [
  gp('p_main', 'mainapp', '', 'area-one', { working: 1 }),
  gp('p_web', 'web', 'p_main', '', { working: 2, needsYou: 1 }),
  gp('p_api', 'api', 'p_main', '', { inReview: 1 }),
  gp('p_tool', 'tool', '', 'area-one', { working: 1 }),
  gp('p_solo', 'lonely', '', 'area-two'),
  gp('p_loose', 'loose', '', ''),
];
function world() {
  const cards = [
    at('p_main', 'working'),
    at('p_web', 'needs_you'),
    at('p_web', 'working'),
    at('p_web', 'working', { attention: ['stale'] }),
    at('p_api', 'in_review'),
    at('p_tool', 'working'),
    at('p_solo', 'idle'),
    at('p_loose', 'idle'),
    at('p_loose', 'idle'),
  ];
  return snapshot(cards, P);
}

describe('grouping: top-level projects', () => {
  it('parts fold into their parent; counts are summed; cards of parts count for the parent', () => {
    const t = tops(world());
    expect(t.map((x) => [x.id, x.cards.length])).toEqual([
      ['p_main', 5],
      ['p_loose', 2],
      ['p_solo', 1],
      ['p_tool', 1],
    ]);
    const main = t[0];
    expect(main.parts.map((p) => p.id)).toEqual(['p_web', 'p_api']);
    expect(main.counts).toEqual({ working: 3, needsYou: 1, inReview: 1, stale: 0 });
  });

  it('a parent with no cards of its own still leads its parts, and has no own entry in the parts row', () => {
    const s = snapshot([at('p_a'), at('p_b', 'needs_you')], [gp('p_top', 'umbrella'), gp('p_a', 'one', 'p_top'), gp('p_b', 'two', 'p_top')]);
    const [top] = tops(s);
    expect(top.id).toBe('p_top');
    expect(top.cards).toHaveLength(2);
    expect(partsOf(top).map((e) => e.name)).toEqual(['one', 'two']);
    expect(tileBand(s).active.map((t) => [t.name, t.note])).toEqual([['umbrella', '2 parts · 1 needs you · 1 working']]);
    expect(lanes(s).map((r) => r.project.name)).toEqual(['umbrella']);
  });

  it('a part whose parent is missing stands on its own', () => {
    const s = snapshot([at('p_orphan')], [gp('p_orphan', 'orphan', 'p_gone')]);
    expect(tops(s).map((t) => t.id)).toEqual(['p_orphan']);
  });

  it('the parts row: the parent\'s own cards under its own name, then each part, by count', () => {
    const main = tops(world())[0];
    expect(partsOf(main).map((p) => [p.id, p.name, p.count, p.own])).toEqual([
      ['p_web', 'web', 3, false],
      ['p_api', 'api', 1, false],
      ['p_main', 'mainapp', 1, true],
    ]);
    expect(partsOf(tops(world())[3])).toEqual([]); // no parts, no row
  });
});

describe('grouping: tiles', () => {
  it('one tile per top-level project with a card not in Idle; the rest go to the idle box', () => {
    const b = tileBand(world());
    expect(b.active.map((t) => t.name)).toEqual(['mainapp', 'tool']);
    expect(b.idle.map((t) => [t.name, t.count])).toEqual([
      ['loose', 2],
      ['lonely', 1],
    ]);
    expect(b.all.name).toBe('all projects');
    expect(b.all.count).toBe(9);
  });

  it('dots: one per agent, needs-you first, in column order', () => {
    const main = tileBand(world()).active[0];
    expect(main.dots.map((d) => d.column)).toEqual(['needs_you', 'working', 'working', 'working', 'in_review']);
    expect(main.more).toBe(0);
  });

  it('the note: parts, then up to two states, then stale; a single agent gets words only', () => {
    const [main, tool] = tileBand(world()).active;
    expect(main.note).toBe('2 parts · 1 needs you · 3 working · 1 stale');
    expect(tool.note).toBe('working');
    const lone = tileBand(snapshot([at('p_tool', 'working', { attention: ['stale'] })], P)).active[0];
    expect(lone.note).toBe('working · stale');
  });

  it('a tile with a needs-you agent is marked; the all-projects overview is not', () => {
    const b = tileBand(world());
    expect(b.active.map((t) => t.needs)).toEqual([true, false]);
    expect(b.all.needs).toBe(false);
  });

  it('dots beyond the cap are counted, not drawn', () => {
    const many = Array.from({ length: 30 }, () => at('p_tool'));
    const t = tileBand(snapshot(many, P)).active[0];
    expect(t.dots).toHaveLength(24);
    expect(t.more).toBe(6);
  });
});

describe('grouping: rail', () => {
  it('groups by area; an area with one project and projects with no area go to "other", last', () => {
    const g = railGroups(world());
    expect(g.map((x) => [x.label, x.count, x.projects.map((p) => p.name)])).toEqual([
      ['area-one', 6, ['mainapp', 'tool']],
      ['other', 3, ['loose', 'lonely']],
    ]);
    expect(g[0].projects[0].parts.map((p) => p.name)).toEqual(['web', 'api', 'mainapp']);
  });

  it('with no areas at all, one group named "projects"', () => {
    const s = snapshot([at('a'), at('b')], [gp('a', 'alpha'), gp('b', 'bravo')]);
    expect(railGroups(s).map((g) => g.label)).toEqual(['projects']);
  });
});

describe('grouping: filtering', () => {
  const ids = (s: ReturnType<typeof world>, sel: Sel) => filterCards(s, sel).map((c) => c.projectId);
  it('a top-level project includes its parts; a part narrows to it; the parent\'s own entry narrows to its own cards', () => {
    const s = world();
    expect(ids(s, ALL)).toHaveLength(9);
    expect(new Set(ids(s, { kind: 'top', id: 'p_main' }))).toEqual(new Set(['p_main', 'p_web', 'p_api']));
    expect(new Set(ids(s, { kind: 'part', id: 'p_web' }))).toEqual(new Set(['p_web']));
    expect(new Set(ids(s, { kind: 'part', id: 'p_main' }))).toEqual(new Set(['p_main']));
  });

  it('an area includes its projects and their parts', () => {
    const s = world();
    const [one, other] = railGroups(s);
    expect(new Set(ids(s, { kind: 'area', key: one.key }))).toEqual(new Set(['p_main', 'p_web', 'p_api', 'p_tool']));
    expect(new Set(ids(s, { kind: 'area', key: other.key }))).toEqual(new Set(['p_solo', 'p_loose']));
  });

  it('a filtered view counts its own cards the way the server does', () => {
    const s = world();
    expect(countsOf(filterCards(s, { kind: 'top', id: 'p_main' }))).toEqual({ working: 3, needsYou: 1, inReview: 1, stale: 1 });
  });

  it('a selection that no longer exists falls back to all projects', () => {
    const s = world();
    expect(validSel(s, { kind: 'top', id: 'p_gone' })).toEqual(ALL);
    expect(validSel(s, { kind: 'part', id: 'p_web' })).toEqual({ kind: 'part', id: 'p_web' });
    expect(validSel(s, { kind: 'area', key: 'area:nowhere' })).toEqual(ALL);
  });

  it('names a selection', () => {
    const s = world();
    expect(selName(s, ALL)).toBe('all projects');
    expect(selName(s, { kind: 'top', id: 'p_main' })).toBe('mainapp');
    expect(selName(s, { kind: 'part', id: 'p_web' })).toBe('mainapp / web');
    expect(selName(s, { kind: 'area', key: railGroups(s)[0].key })).toBe('area-one');
  });
});

describe('grouping: labels and lanes', () => {
  it('a card in a part reads "parent / part"; otherwise just the name', () => {
    const s = world();
    const ix = indexOf(s);
    const web = s.cards.find((c) => c.projectId === 'p_web')!;
    const tool = s.cards.find((c) => c.projectId === 'p_tool')!;
    expect(projectLabel(web, ix)).toEqual({ name: 'mainapp', part: 'web' });
    expect(projectLabel(tool, ix)).toEqual({ name: 'tool', part: '' });
    expect(projectName(web, ix)).toBe('mainapp / web');
  });

  it('Lanes has one row per top-level project, with the parts\' cards, counted from the cards it shows', () => {
    const rows = lanes(world());
    expect(rows.map((r) => r.project.id)).toEqual(['p_main', 'p_loose', 'p_solo', 'p_tool']);
    expect(Object.values(rows[0].cells).flat()).toHaveLength(5);
    expect(rows[0].overview).toEqual(['3 working', '1 needs you', '1 in review', '1 stale']);
    expect(rows[0].meta).toBe('2 parts · 5 agents');
  });
});

describe('grouping: odd parent graphs', () => {
  const ids = (cards: readonly Card[]) => cards.map((c) => c.id).sort();

  it('two projects naming each other are both top-level; every card is still shown', () => {
    const s = snapshot([at('p_a'), at('p_b'), at('p_b')], [gp('p_a', 'aa', 'p_b'), gp('p_b', 'bb', 'p_a')]);
    expect(tops(s).map((t) => [t.id, t.cards.length])).toEqual([
      ['p_b', 2],
      ['p_a', 1],
    ]);
    expect(lanes(s).flatMap((r) => Object.values(r.cells).flat())).toHaveLength(3);
    expect(projectLabel(s.cards[0], indexOf(s))).toEqual({ name: 'aa', part: '' });
  });

  it('a part of a part is top-level: one level only', () => {
    const s = snapshot([at('p_top'), at('p_mid'), at('p_low'), at('p_low')], [gp('p_top', 'top'), gp('p_mid', 'mid', 'p_top'), gp('p_low', 'low', 'p_mid')]);
    expect(tops(s).map((t) => [t.id, t.cards.length])).toEqual([
      ['p_low', 2],
      ['p_top', 2],
    ]);
    expect(projectLabel(s.cards[2], indexOf(s))).toEqual({ name: 'low', part: '' });
    expect(projectLabel(s.cards[1], indexOf(s))).toEqual({ name: 'top', part: 'mid' });
  });

  it('a duplicated project id counts once, the first one winning', () => {
    const s = snapshot([at('p_a')], [gp('p_a', 'first'), gp('p_a', 'second')]);
    expect(tops(s).map((t) => t.name)).toEqual(['first']);
    expect(tileBand(s).active.map((t) => t.key)).toEqual(['p_a']);
    expect(projectLabel(s.cards[0], indexOf(s)).name).toBe('first');
  });

  it('any parent graph: every card appears in exactly one top-level project, tile or idle entry, and rail group', () => {
    let seed = 7;
    const rnd = (n: number) => {
      seed = (seed * 1103515245 + 12345) % 2 ** 31;
      return seed % n;
    };
    for (let round = 0; round < 200; round++) {
      const n = 1 + rnd(8);
      const pids = Array.from({ length: n }, (_, i) => `p_${i}`);
      const projects = pids.map((id) => gp(id, id, rnd(3) ? pids[rnd(n + 2)] ?? 'p_gone' : '', rnd(2) ? `area-${rnd(3)}` : ''));
      if (rnd(4) === 0) projects.push(gp(pids[0], 'dup'));
      const cards = Array.from({ length: rnd(12) }, () => at(pids[rnd(n)], (['working', 'idle', 'needs_you'] as const)[rnd(3)]));
      const s = snapshot(cards, projects);
      const g = grouping(s);
      const all = ids(s.cards);
      expect(ids(g.tops.flatMap((t) => t.cards))).toEqual(all);
      const band = tileBand(g);
      expect([...band.active, ...band.idle].reduce((a, t) => a + t.count, 0)).toBe(cards.length);
      expect(g.groups.reduce((a, x) => a + x.count, 0)).toBe(cards.length);
      expect(ids(g.groups.flatMap((x) => filterCards(g, { kind: 'area', key: x.key })))).toEqual(all);
      expect(lanes(s).flatMap((r) => Object.values(r.cells).flat()).length).toBe(cards.length);
    }
  });
});

describe('grouping: selection follows the board', () => {
  it('a selected project that becomes a part is followed as that part', () => {
    const before = snapshot([at('p_a'), at('p_b')], [gp('p_a', 'aa'), gp('p_b', 'bb')]);
    expect(validSel(before, { kind: 'top', id: 'p_b' })).toEqual({ kind: 'top', id: 'p_b' });
    const after = snapshot([at('p_a'), at('p_b')], [gp('p_a', 'aa'), gp('p_b', 'bb', 'p_a')]);
    expect(validSel(after, { kind: 'top', id: 'p_b' })).toEqual({ kind: 'part', id: 'p_b' });
  });

  it('an area named like the fallback group does not share its label', () => {
    const s = snapshot(
      [at('a'), at('b'), at('c')],
      [gp('a', 'aa', '', 'other'), gp('b', 'bb', '', 'other'), gp('c', 'cc', '', '')],
    );
    expect(railGroups(s).map((g) => g.label)).toEqual(['other', 'no area']);
  });
});

describe('grouping: Lanes with one part selected', () => {
  it('the lane is named "parent / part", counts only its shown cards, and drops the parts text', () => {
    const s = world();
    const shown = { ...s, cards: filterCards(s, { kind: 'part', id: 'p_web' }) as Card[] };
    const [row] = lanes(shown);
    expect(row.name).toBe('mainapp / web');
    expect(row.overview).toEqual(['2 working', '1 needs you', '1 stale']);
    expect(row.meta).toBe('3 agents');
  });
});

describe('grouping: when the tiles band scrolls', () => {
  it('wide: past two rows; narrow (under 600px): only past twelve; unknown width: never', () => {
    expect(bandScrolls(0, 99)).toBe(false);
    expect(bandScrolls(1260, 16)).toBe(false); // 8 per row
    expect(bandScrolls(1260, 17)).toBe(true);
    expect(bandScrolls(327, 6)).toBe(false); // three projects and the idle box
    expect(bandScrolls(327, 12)).toBe(false);
    expect(bandScrolls(327, 13)).toBe(true);
  });
});

