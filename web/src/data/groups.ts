// Project grouping: top-level projects with their parts, the tiles band, the
// rail's area groups, and the one selection all three views filter by.
//
// A part is a project whose parentId names another project on the board that
// is itself top-level (one level only). A parent that is missing, that is
// itself a part, or that forms a loop leaves the project top-level, so every
// card lands in exactly one top-level project. A card's projectId is the
// part; a project's counts are its own cards, so a top-level project sums
// its parts. The first project of a duplicated id wins.
import { ColumnValues, type Card, type Column, type Counts, type Project, type Snapshot } from '../contract.gen';
import { clean } from './text';
import { openIssuesOf } from './issues';

type S = Pick<Snapshot, 'cards' | 'projects'>;

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`;
const RANK = new Map(ColumnValues.map((c, i) => [c, i]));

/** The projects by id, the first of a duplicated id winning. */
export function projectsById(projects: readonly Project[]): Map<string, Project> {
  const byId = new Map<string, Project>();
  for (const p of projects) if (!byId.has(p.id)) byId.set(p.id, p);
  return byId;
}

const names = (p: Project) => !!p.parentId && p.parentId !== p.id;

/** The project a part belongs to, or undefined when the project is top-level. */
export function partParent(p: Project, byId: ReadonlyMap<string, Project>): Project | undefined {
  if (!names(p)) return undefined;
  const parent = byId.get(p.parentId);
  // One level only: a parent that is itself a part (a chain, or a loop) does not take parts.
  if (!parent || (names(parent) && byId.has(parent.parentId))) return undefined;
  return parent;
}

export interface Dot {
  id: string; // the card's id, so a dot keeps its identity when its colour changes
  column: Column;
}

export const DOTS_MAX = 24;

/** One dot per agent, needs-you first in column order; dots past the cap are counted in `more`. */
export function dotsOf(cards: readonly Card[]): { dots: Dot[]; more: number } {
  const all = [...cards]
    .sort((a, b) => (RANK.get(a.column) ?? 0) - (RANK.get(b.column) ?? 0))
    .map((c) => ({ id: c.id, column: c.column }));
  return { dots: all.slice(0, DOTS_MAX), more: Math.max(0, all.length - DOTS_MAX) };
}

/** Counts for a set of cards, the way the server counts them for a project. */
export function countsOf(cards: readonly Card[]): Counts {
  const c: Counts = { working: 0, needsYou: 0, inReview: 0, stale: 0 };
  for (const x of cards) {
    if (x.column === 'working') c.working++;
    else if (x.column === 'needs_you') c.needsYou++;
    else if (x.column === 'in_review') c.inReview++;
    if (x.attention.includes('stale')) c.stale++;
  }
  return c;
}

const WORD: Record<Exclude<Column, 'idle'>, (n: number) => string> = {
  needs_you: (n) => (n === 1 ? 'needs you' : 'need you'),
  working: () => 'working',
  with_coordinator: () => 'with coordinator',
  in_review: () => 'in review',
  done: () => 'done',
};

/** "2 parts · 1 needs you · 3 working · 1 stale"; a single agent reads "working · stale". */
export function tileNote(cards: readonly Card[], parts = 0): string {
  const bits: string[] = [];
  if (parts) bits.push(plural(parts, 'part'));
  const one = cards.length === 1;
  const states = (Object.keys(WORD) as (keyof typeof WORD)[])
    .map((c) => [c, cards.filter((x) => x.column === c).length] as const)
    .filter(([, n]) => n > 0)
    .slice(0, 2);
  for (const [c, n] of states) bits.push(one ? WORD[c](n) : `${n} ${WORD[c](n)}`);
  const stale = cards.filter((c) => c.attention.includes('stale')).length;
  if (stale) bits.push(one ? 'stale' : `${stale} stale`);
  if (!states.length && cards.length) bits.push('idle');
  return bits.join(' · ');
}

export interface PartEntry {
  id: string;
  name: string;
  count: number;
  own: boolean; // the parent's own cards, shown under the parent's name
  dots: Dot[];
  more: number;
  note: string;
  /** Open issues of this part's repository; null when not known. */
  issues: number | null;
}

export interface Top {
  id: string;
  name: string;
  area: string;
  project: Project;
  parts: Project[];
  cards: Card[];
  counts: Counts; // the server's counts, the parts summed in
  entries: PartEntry[]; // the parts row: empty when the project has no part with cards
}

export interface RailProject {
  id: string;
  name: string;
  count: number;
  dots: Dot[];
  more: number;
  note: string;
  parts: PartEntry[];
  /** Open issues of the project and its parts; null when not known. */
  issues: number | null;
}

export interface RailGroup {
  key: string;
  label: string;
  count: number;
  projects: RailProject[];
}

/** Everything the grouping needs, worked out once per snapshot. */
export interface Grouping {
  cards: readonly Card[];
  byId: ReadonlyMap<string, Project>;
  tops: Top[]; // with at least one card, most cards first
  topById: ReadonlyMap<string, Top>;
  groups: RailGroup[];
  groupOf: ReadonlyMap<string, string>; // top id → rail group key
  /** The top-level project a project belongs to (an unknown id is its own). */
  topOf(projectId: string): string;
}

const byCount = <T extends { name: string }>(count: (x: T) => number) => (a: T, b: T) =>
  count(b) - count(a) || a.name.localeCompare(b.name);

function entriesOf(top: Top): PartEntry[] {
  if (!top.parts.length) return [];
  const mine = new Map<string, Card[]>();
  for (const c of top.cards) {
    const list = mine.get(c.projectId) ?? [];
    list.push(c);
    mine.set(c.projectId, list);
  }
  const entry = (p: Project, own: boolean): PartEntry => {
    const cards = mine.get(p.id) ?? [];
    return { id: p.id, name: clean(p.name), count: cards.length, own, ...dotsOf(cards), note: tileNote(cards), issues: openIssuesOf([p]) };
  };
  const list = [entry(top.project, true), ...top.parts.map((p) => entry(p, false))].filter((e) => e.count > 0);
  if (!list.some((e) => !e.own)) return [];
  return list.sort(byCount((e) => e.count));
}

export function grouping(s: S): Grouping {
  const byId = projectsById(s.projects);
  const parentOf = new Map<string, string>(); // part id → parent id
  for (const p of byId.values()) {
    const parent = partParent(p, byId);
    if (parent) parentOf.set(p.id, parent.id);
  }
  const topOf = (id: string) => parentOf.get(id) ?? id;

  const all = new Map<string, Top>();
  for (const p of byId.values()) {
    if (parentOf.has(p.id)) continue;
    all.set(p.id, { id: p.id, name: clean(p.name), area: clean(p.area), project: p, parts: [], cards: [], counts: { ...p.counts }, entries: [] });
  }
  for (const p of byId.values()) {
    const t = all.get(parentOf.get(p.id) ?? '');
    if (!t) continue;
    t.parts.push(p);
    t.counts.working += p.counts.working;
    t.counts.needsYou += p.counts.needsYou;
    t.counts.inReview += p.counts.inReview;
    t.counts.stale += p.counts.stale;
  }
  for (const c of s.cards) all.get(topOf(c.projectId))?.cards.push(c);
  const tops = [...all.values()].filter((t) => t.cards.length > 0).sort(byCount((t) => t.cards.length));
  for (const t of tops) t.entries = entriesOf(t);

  // Rail groups: an area with two or more projects is a group; the rest share one fallback group.
  const perArea = new Map<string, number>();
  for (const t of tops) if (t.area) perArea.set(t.area, (perArea.get(t.area) ?? 0) + 1);
  const real = [...perArea].filter(([, n]) => n > 1).map(([a]) => a);
  let fallback = real.length ? 'other' : 'projects';
  if (real.includes(fallback)) fallback = 'no area';
  const groupOf = new Map<string, string>();
  const groups = new Map<string, RailGroup>();
  for (const t of tops) {
    const own = t.area && (perArea.get(t.area) ?? 0) > 1;
    const key = own ? `area:${t.area}` : 'other';
    groupOf.set(t.id, key);
    const g = groups.get(key) ?? { key, label: own ? t.area : fallback, count: 0, projects: [] };
    g.count += t.cards.length;
    g.projects.push({
      id: t.id,
      name: t.name,
      count: t.cards.length,
      ...dotsOf(t.cards),
      note: tileNote(t.cards, t.entries.filter((e) => !e.own).length),
      parts: t.entries,
      issues: openIssuesOf([t.project, ...t.parts]),
    });
    groups.set(key, g);
  }
  const list = [...groups.values()];
  const areas = list.filter((g) => g.key !== 'other').sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));

  return {
    cards: s.cards,
    byId,
    tops,
    topById: new Map(tops.map((t) => [t.id, t])),
    groups: [...areas, ...list.filter((g) => g.key === 'other')],
    groupOf,
    topOf,
  };
}

// ---- Shorthands over one snapshot ---------------------------------------------

const asGrouping = (s: S | Grouping): Grouping => ('topOf' in s ? s : grouping(s));

/** Top-level projects with at least one card, most cards first. */
export const tops = (s: S | Grouping): Top[] => asGrouping(s).tops;

/** The parts row of a top-level project. */
export const partsOf = (top: Top): PartEntry[] => top.entries;

/** The rail's groups. */
export const railGroups = (s: S | Grouping): RailGroup[] => asGrouping(s).groups;

// ---- Selection -------------------------------------------------------------

export type Sel =
  | { kind: 'all' }
  | { kind: 'top'; id: string } // a top-level project with its parts
  | { kind: 'part'; id: string } // exactly one project: a part, or a parent's own cards
  | { kind: 'area'; key: string }; // a rail group

export const ALL: Sel = { kind: 'all' };

export const sameSel = (a: Sel, b: Sel) =>
  a.kind === b.kind &&
  (a.kind === 'all' || (a.kind === 'area' ? a.key === (b as { key: string }).key : a.id === (b as { id: string }).id));

/** Which cards a selection keeps. */
export function filterCards(s: S | Grouping, sel: Sel): readonly Card[] {
  const g = asGrouping(s);
  switch (sel.kind) {
    case 'all':
      return g.cards;
    case 'part':
      return g.cards.filter((c) => c.projectId === sel.id);
    case 'top':
      return g.topById.get(sel.id)?.cards ?? [];
    case 'area':
      return g.cards.filter((c) => g.groupOf.get(g.topOf(c.projectId)) === sel.key);
  }
}

/**
 * The selection as it reads on this snapshot: a project that became a part
 * of another is followed as that part; one that left the board gives way to
 * all projects.
 */
export function validSel(s: S | Grouping, sel: Sel): Sel {
  const g = asGrouping(s);
  if (sel.kind === 'top' && !g.topById.has(sel.id) && g.topOf(sel.id) !== sel.id) {
    const part: Sel = { kind: 'part', id: sel.id };
    return filterCards(g, part).length ? part : ALL;
  }
  return sel.kind === 'all' || filterCards(g, sel).length > 0 ? sel : ALL;
}

/** The projects a selection covers: each top-level project with its parts, or the one part. */
export function projectsOf(s: S | Grouping, sel: Sel): Project[] {
  const g = asGrouping(s);
  const own = (t: Top) => [t.project, ...t.parts];
  switch (sel.kind) {
    case 'all':
      return g.tops.flatMap(own);
    case 'top': {
      const t = g.topById.get(sel.id);
      return t ? own(t) : [];
    }
    case 'part': {
      const p = g.byId.get(sel.id);
      return p ? [p] : [];
    }
    case 'area':
      return g.tops.filter((t) => g.groupOf.get(t.id) === sel.key).flatMap(own);
  }
}

/** The selection in words: "all projects", "mainapp", "mainapp / web", an area. */
export function selName(s: S | Grouping, sel: Sel): string {
  const g = asGrouping(s);
  if (sel.kind === 'all') return 'all projects';
  if (sel.kind === 'area') return g.groups.find((x) => x.key === sel.key)?.label ?? 'all projects';
  const p = g.byId.get(sel.id);
  if (!p) return 'unknown project';
  const parent = partParent(p, g.byId);
  return sel.kind === 'part' && parent ? `${clean(parent.name)} / ${clean(p.name)}` : clean(p.name);
}

// ---- Tiles -----------------------------------------------------------------

export const TILE_MIN = 150;
const TILE_GAP = 8;
const NARROW = 600;
const NARROW_MAX = 12;

/**
 * Whether the band stops wrapping and scrolls sideways: past two rows on a
 * wide screen; on a narrow one (under 600px) tiles wrap freely up to twelve.
 * `slots` counts tiles, the idle box as two.
 */
export function bandScrolls(width: number, slots: number): boolean {
  if (!width) return false;
  if (width < NARROW) return slots > NARROW_MAX;
  const perRow = Math.max(1, Math.floor((width + TILE_GAP) / (TILE_MIN + TILE_GAP)));
  return slots > perRow * 2;
}

export interface Tile {
  sel: Sel;
  key: string;
  name: string;
  count: number;
  dots: Dot[];
  more: number;
  note: string;
  needs: boolean;
  hasParts: boolean;
  /** Open issues of the tile's projects; null when not known. */
  issues: number | null;
}

function tileOf(sel: Sel, key: string, name: string, cards: readonly Card[], parts: number, projects: readonly Project[]): Tile {
  return {
    sel,
    key,
    name,
    count: cards.length,
    ...dotsOf(cards),
    note: tileNote(cards, parts),
    needs: cards.some((c) => c.column === 'needs_you'),
    hasParts: parts > 0,
    issues: openIssuesOf(projects),
  };
}

/** The tiles band: all projects, a tile per project with work in hand, and the idle box. */
export function tileBand(s: S | Grouping): { all: Tile; active: Tile[]; idle: Tile[] } {
  const g = asGrouping(s);
  const tile = (t: Top) => tileOf({ kind: 'top', id: t.id }, t.id, t.name, t.cards, t.entries.filter((e) => !e.own).length, [t.project, ...t.parts]);
  return {
    // The overview is not marked: someone nearly always needs you somewhere.
    all: { ...tileOf(ALL, 'all', 'all projects', g.cards, 0, projectsOf(g, ALL)), needs: false },
    active: g.tops.filter((t) => t.cards.some((c) => c.column !== 'idle')).map(tile),
    idle: g.tops.filter((t) => t.cards.every((c) => c.column === 'idle')).map(tile),
  };
}
