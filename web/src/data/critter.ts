// The agent's critter: a small creature cut from straight-edged shapes, drawn
// from the card id, with two square eyes that carry the state and a line under
// its feet in the state colour. Pure: the same id always gives the same
// critter, on every view, after a reload and after a restart.

import type { Card } from '../contract.gen';

/**
 * What the critter shows. Only states the page already receives: the six
 * columns, the four attention reasons and status unknown, resolved to one.
 */
export type CritterState =
  | 'needs_you'
  | 'working'
  | 'working_failing'
  | 'with_coordinator'
  | 'in_review'
  | 'done'
  | 'idle'
  | 'blocked'
  | 'unseen'
  | 'stale'
  | 'failing'
  | 'unknown';

export const CRITTER_STATES: readonly CritterState[] = [
  'needs_you',
  'working',
  'working_failing',
  'with_coordinator',
  'in_review',
  'done',
  'idle',
  'blocked',
  'unseen',
  'stale',
  'failing',
  'unknown',
];

/** What critterState reads from a card. */
export type CritterCard = Pick<Card, 'column' | 'attention' | 'seen' | 'pullRequests'> & { agent: Pick<Card['agent'], 'status'> };

/**
 * One state per card. Status unknown first (the card says so in words); then
 * the reasons that put a card in Needs you; stale on a busy card; failing
 * checks: crossed eyes on a card in review, and on a busy card working eyes
 * over a red ground line (it is alive, and its checks fail).
 *
 * A card marked seen on this page waits in Needs you for the server's next
 * snapshot. Where the rules send it to Idle then, it sleeps at once
 * (seenGoesIdle); anywhere else it stays as it is.
 */
export function critterState(card: CritterCard): CritterState {
  const a = card.attention;
  if (card.agent.status === 'unknown') return 'unknown';
  if (a.includes('blocked')) return 'blocked';
  if (a.includes('unseen')) return 'unseen';
  switch (card.column) {
    case 'working':
      return a.includes('stale') ? 'stale' : a.includes('failing_checks') ? 'working_failing' : 'working';
    case 'in_review':
      return a.includes('failing_checks') ? 'failing' : 'in_review';
    case 'needs_you':
      return seenGoesIdle(card) ? 'idle' : 'needs_you';
    case 'with_coordinator':
    case 'done':
    case 'idle':
      return card.column;
    default:
      return 'idle';
  }
}

/**
 * internal/core/rules.go puts a card in Needs you for one of two reasons:
 * blocked (rule 1) or a turn not seen (rule 5, which needs status finished,
 * no open pull request, none closed within the Done window, no
 * coordinator). The page drops "unseen" when the owner marks it seen; the
 * card is then in Needs you with no reason left, and marking it seen changed
 * only `seen`, so the server's next snapshot puts it in Idle (rule 7). This
 * checks what the card shows of rule 5 too: finished, nothing open.
 */
function seenGoesIdle(card: CritterCard): boolean {
  return card.seen && card.attention.length === 0 && card.agent.status === 'finished' && !card.pullRequests.some((p) => p.state === 'open');
}

// ---- Seeded generator: FNV-1a, a 32-bit finaliser, then xorshift32 ----------

function hash(str: string): number {
  let h = 0x811c9dc5;
  for (const ch of str) {
    h ^= ch.codePointAt(0) ?? 0;
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  h ^= h >>> 16;
  h = Math.imul(h, 0x85ebca6b) >>> 0;
  h ^= h >>> 13;
  h = Math.imul(h, 0xc2b2ae35) >>> 0;
  h ^= h >>> 16;
  return h >>> 0;
}

function prng(id: string, salt: string): () => number {
  let x = hash(salt + '|' + id) || 0x9e3779b9;
  const next = () => {
    x ^= x << 13;
    x >>>= 0;
    x ^= x >>> 17;
    x ^= x << 5;
    x >>>= 0;
    return x / 4294967296;
  };
  for (let i = 0; i < 4; i++) next();
  return next;
}

const pick = (r: () => number, n: number) => Math.floor(r() * n);

/** The critter's traits, each an index into a small table. */
export interface Critter {
  body: number; // 0 bevelled, 1 house, 2 trapezoid, 3 bevelled feet, 4 slanted top
  prop: number; // 0 wide, 1 square, 2 tall
  top: number; // 0 none, 1 antenna, 2 ears
  ant: number; // antenna at left, middle or right
  mark: number; // 0 none, 1 belt, 2 corner notch, 3 spot
  es: number; // eye size
  sp: number; // eye spacing
  eh: number; // eye height
  off: number; // eyes left, centred or right
  key: string; // the traits as one string: two critters look alike when their keys match
}

type Traits = Omit<Critter, 'key'>;

/** Folds trait combinations that cannot be drawn onto ones that can. */
function norm(t: Traits): Critter {
  const m = { ...t };
  const flat = m.body === 2 || m.body === 3;
  if (m.top === 2 && !flat) m.top = 1; // ears only on a flat top
  if (m.top !== 1) m.ant = 0;
  if (m.body === 1 && m.top === 1) m.ant = 1; // the house carries its antenna on the peak
  const W = [19, 16, 13][m.prop];
  const room = () => W / 2 - (m.body === 2 ? 2.4 : 0.6) - 1;
  const ext = () => [2.6, 4, 5.6][m.sp] / 2 + [2.4, 3][m.es] + Math.abs([-1.2, 0, 1.2][m.off]);
  while (ext() > room()) {
    if (m.off !== 1) m.off = 1;
    else if (m.sp > 0) m.sp--;
    else if (m.es > 0) m.es--;
    else break;
  }
  return { ...m, key: [m.body, m.prop, m.top, m.ant, m.mark, m.es, m.sp, m.eh, m.off].join('') };
}

/** The critter for a card id. Any string works, the empty one too. */
export function critter(id: string): Critter {
  const r = prng(String(id), 'B');
  return norm({ body: pick(r, 5), prop: pick(r, 3), top: pick(r, 3), ant: pick(r, 3), mark: pick(r, 4), es: pick(r, 2), sp: pick(r, 3), eh: pick(r, 2), off: pick(r, 3) });
}

/** How many different critters the rule can make. */
export function critterCount(): number {
  const keys = new Set<string>();
  for (let body = 0; body < 5; body++)
    for (let prop = 0; prop < 3; prop++)
      for (let top = 0; top < 3; top++)
        for (let ant = 0; ant < 3; ant++)
          for (let mark = 0; mark < 4; mark++)
            for (let es = 0; es < 2; es++)
              for (let sp = 0; sp < 3; sp++)
                for (let eh = 0; eh < 2; eh++) for (let off = 0; off < 3; off++) keys.add(norm({ body, prop, top, ant, mark, es, sp, eh, off }).key);
  return keys.size;
}

/** Where the critter's loop starts, in seconds before now, so a crowd does not move in step. */
export function critterPhase(id: string): number {
  return -Math.round(prng(String(id), 'phase')() * 600) / 100;
}

// ---- Drawing ----------------------------------------------------------------

/** The eye shapes: open, wide, closed, happy, crossed. One is shown per state. */
export type EyeKind = 'eo' | 'ew' | 'ec' | 'eh' | 'ex';

/** Every shape is one SVG path in a 24 by 24 box. */
export interface Drawing {
  ground: string;
  tops: string; // antenna or ears; '' when none
  body: string;
  mark: string; // '' when none
  eyes: Record<EyeKind, string>;
  /**
   * Where the open eyes look, in box units, each as far as the body leaves room
   * for: down and left at the work, up at the review, across to the coordinator.
   */
  gaze: { work: readonly [number, number]; up: number; look: number };
  pip: string; // the small square of a turn not seen yet
  /**
   * Bounding boxes, in box units (x0, y0, x1, y1): `body`, everything that moves
   * with the body (tops, body, mark, eyes); `eyes`, every eye shape but the open
   * one; `eo`, the open eyes where they rest. The parts move about their own boxes.
   */
  box: { body: Bounds; eyes: Bounds; eo: Bounds };
}

/** A box's left, top, right and bottom edges. */
export type Bounds = readonly [number, number, number, number];

/** The box around a path drawn with M, L, h, v and Z only. */
export function pathBounds(d: string): Bounds | null {
  const xs: number[] = [];
  const ys: number[] = [];
  let x = 0;
  let y = 0;
  for (const [, cmd, args] of d.matchAll(/([MLhvZ])([^MLhvZ]*)/g)) {
    const n = args.trim() ? args.trim().split(/[\s,]+/).map(Number) : [];
    if (cmd === 'M' || cmd === 'L') [x, y] = n;
    else if (cmd === 'h') x += n[0];
    else if (cmd === 'v') y += n[0];
    else continue;
    xs.push(x);
    ys.push(y);
  }
  return xs.length ? [Math.min(...xs), Math.min(...ys), Math.max(...xs), Math.max(...ys)] : null;
}

/** The box around several boxes; boxes that are null are skipped. */
export function union(...bs: (Bounds | null)[]): Bounds {
  const all = bs.filter((b): b is Bounds => !!b);
  return [Math.min(...all.map((b) => b[0])), Math.min(...all.map((b) => b[1])), Math.max(...all.map((b) => b[2])), Math.max(...all.map((b) => b[3]))];
}

const f = (v: number) => +v.toFixed(2);
type Pt = readonly [number, number];
const rect = (x: number, y: number, w: number, h: number) => `M${f(x)} ${f(y)}h${f(w)}v${f(h)}h${f(-w)}Z`;
const poly = (pts: readonly Pt[]) => 'M' + pts.map(([x, y]) => `${f(x)} ${f(y)}`).join('L') + 'Z';

/**
 * The critter as paths. `small` is the 20 px drawing: the eyes grow and their
 * shapes are drawn bolder, so every state still reads at that size.
 */
export function drawing(m: Critter, small = false): Drawing {
  const [W, H] = ([[19, 12], [16, 14], [13, 15.5]] as const)[m.prop];
  const L = 12 - W / 2;
  const R = 12 + W / 2;
  const Bt = 20.5;
  const T = Bt - H;
  const cx = 12;
  let pts: Pt[] = [];
  let topAt = (_x: number) => T;
  let flatL = L;
  let flatR = R;
  if (m.body === 0) {
    const c = W * 0.22;
    pts = [[L + c, T], [R - c, T], [R, T + c], [R, Bt], [L, Bt], [L, T + c]];
    flatL = L + c;
    flatR = R - c;
  } else if (m.body === 1) {
    pts = [[L, T + 3.5], [cx, T], [R, T + 3.5], [R, Bt], [L, Bt]];
    topAt = (x) => T + (3.5 * Math.abs(x - cx)) / (W / 2);
  } else if (m.body === 2) {
    pts = [[L + 3, T], [R - 3, T], [R, Bt], [L, Bt]];
    flatL = L + 3;
    flatR = R - 3;
  } else if (m.body === 3) {
    const c = W * 0.2;
    pts = [[L, T], [R, T], [R, Bt - c], [R - c, Bt], [L + c, Bt], [L, Bt - c]];
  } else {
    pts = [[L, T], [R, T + 3], [R, Bt], [L, Bt]];
    topAt = (x) => T + (3 * (x - L)) / W;
  }

  let tops = '';
  if (m.top === 1) {
    const span = Math.min(3, (flatR - flatL) / 2 - 1.4);
    const ax = m.body === 1 ? cx : cx + [-span, 0, span][m.ant];
    const by = topAt(ax);
    tops = rect(ax - 0.5, by - 3, 1, 3.2) + rect(ax - 1.2, by - 4.6, 2.4, 2.4);
  } else if (m.top === 2) {
    const i = m.body === 2 ? 3.4 : 1.2;
    tops =
      poly([[L + i, T + 0.4], [L + i + 1.4, T - 2.8], [L + i + 2.8, T + 0.4]]) +
      poly([[R - i - 2.8, T + 0.4], [R - i - 1.4, T - 2.8], [R - i, T + 0.4]]);
  }

  const U = 24 / (small ? 20 : 24); // one screen pixel, in box units
  const ey = T + (m.body === 1 ? 2 : 0) + H * [0.3, 0.42][m.eh];
  // A hole (an eye, the spot) must keep about a pixel of body around it: 0.4 inside the
  // outline plus the outline's own 0.6. The corner notch is a hole too; eyes keep a pixel from it.
  const clearOf = (notch: { x0: number; y0: number; x1: number; y1: number } | null) => ([x, y, w, h]: Box): boolean => {
    const [x0, y0, x1, y1] = [x * U, y * U, (x + w) * U, (y + h) * U];
    const g = 0.4;
    const inBody = ([[x0 - g, y0 - g], [x1 + g, y0 - g], [x0 - g, y1 + g], [x1 + g, y1 + g]] as const).every(([px, py]) => insidePoly(pts, px, py));
    return inBody && !(notch && x1 > notch.x0 && x0 < notch.x1 && y1 > notch.y0 && y0 < notch.y1);
  };
  const clear = clearOf(null);
  let face = pixelEyes(m, ey, small, m.mark === 2 ? clearOf({ x0: R - 3.6 - U, y0: Bt - 3.6 - U, x1: R + 1 + U, y1: Bt + 1 + U }) : clear);
  // A few tall, tapering critters have no room for their eyes between the top and the
  // notch at 20 px: there the notch is left out, as the spot is.
  const notched = m.mark === 2 && face.fits;
  if (!face.fits && m.mark === 2) face = pixelEyes(m, ey, small, clear);
  const p = (b: Box) => rect(b[0] * U, b[1] * U, b[2] * U, b[3] * U);
  const eyes = Object.fromEntries(Object.entries(face.cells).map(([k, cells]) => [k, cells.map(p).join('')])) as Record<EyeKind, string>;

  // The spot, two pixels square, at least two pixels from the eyes wherever they look,
  // so it never reads as a third eye; lower down when it would be closer; left out
  // when it fits nowhere, and always at 20 px, where any two pixels near the eyes do.
  let spot = '';
  if (!small) {
    const z = face.zone;
    const apart = (b: Box) => b[0] >= z[2] + 2 || b[0] + b[2] <= z[0] - 2 || b[1] >= z[3] + 2 || b[1] + b[3] <= z[1] - 2;
    const at = ([[L + 2.6, Bt - 5.4], [L + 2, Bt - 3.6]] as const).map(([x, y]): Box => [Math.round(x), Math.round(y), 2, 2]).find((b) => apart(b) && clear(b));
    if (at) spot = p(at);
  }
  const mark = ['', rect(L - 1.5, Bt - 4.6, W + 3, 1.4), notched ? rect(R - 3.6, Bt - 3.6, 4.6, 4.6) : '', spot][m.mark];
  const g = face.gaze;
  const body = poly(pts);
  const { eo, ...others } = eyes;
  const eyesBox = union(...Object.values(others).map(pathBounds));
  const eoBox = union(pathBounds(eo));

  return {
    ground: rect(L, 21.6, W, 2),
    tops,
    body,
    mark,
    eyes,
    box: { body: union(pathBounds(tops), pathBounds(body), pathBounds(mark), eyesBox, eoBox), eyes: eyesBox, eo: eoBox },
    gaze: { work: [g.work[0] * U, g.work[1] * U], up: g.up * U, look: g.look * U },
    pip: rect(18.6, 0.4, 4.4, 4.4),
  };
}

/** A rectangle in screen pixels: x, y, width, height. */
type Box = readonly [number, number, number, number];

function insidePoly(pts: readonly Pt[], x: number, y: number): boolean {
  let c = false;
  for (let i = 0, j = pts.length - 1; i < pts.length; j = i++) {
    const [xi, yi] = pts[i];
    const [xj, yj] = pts[j];
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) c = !c;
  }
  return c;
}

const shift = (cells: readonly Box[], dx: number, dy: number): Box[] => cells.map(([x, y, w, h]) => [x + dx, y + dy, w, h]);

/**
 * The eyes, on whole screen pixels so their edges stay sharp at 20 and at
 * 24 px: square eyes of 2 or 3 pixels; tall ones when it needs you; a
 * one-pixel line when closed; a 3 by 3 pixel chevron when done and cross when
 * checks fail. Where they look is a whole pixel or two (see Critter.svelte).
 *
 * At 20 px they are centred, two pixels apart, and 2 pixels on a tall or
 * tapering body: spacing and offset do not show at that size. At 24 px they
 * keep the critter's own size, spacing and offset. Either way the eyes are
 * placed where every shape and every gaze stays inside the body (`clear`),
 * folding the spacing, the offset and the size, and moving the row a pixel
 * or two, until they do.
 */
function pixelEyes(
  m: Critter,
  eyTop: number,
  small: boolean,
  clear: (b: Box) => boolean,
): { cells: Record<EyeKind, Box[]>; gaze: { work: [number, number]; up: number; look: number }; zone: [number, number, number, number]; fits: boolean } {
  const U = 24 / (small ? 20 : 24);
  const c = 12 / U; // the box's centre, in screen pixels
  const y0 = Math.round(eyTop / U);

  const face = (e: number, gap: number, off: number, y: number) => {
    const x0 = Math.round(c + off - gap / 2 - e);
    const x1 = x0 + e + gap;
    // A 3 pixel cell for the drawn eyes; a 2 pixel eye grows outwards.
    const c0 = e === 3 ? x0 : x0 - 1;
    const c1 = x1;
    const two = (fn: (x: number) => Box[], a: number, b: number) => [...fn(a), ...fn(b)];
    const cells: Record<EyeKind, Box[]> = {
      eo: two((x) => [[x, y, e, e]], x0, x1),
      ew: two((x) => [[x, y - 1, e, e + 2]], x0, x1),
      ec: two((x) => [[x, y + 1, 3, 1]], c0, c1),
      eh: two((x) => [[x, y + 1, 1, 1], [x + 1, y, 1, 1], [x + 2, y + 1, 1, 1]], c0, c1),
      ex: two((x) => [[x, y, 1, 1], [x + 2, y, 1, 1], [x + 1, y + 1, 1, 1], [x, y + 2, 1, 1], [x + 2, y + 2, 1, 1]], c0, c1),
    };
    return cells;
  };
  const ok = (cells: readonly Box[]) => cells.every(clear);

  // The critter's own eyes first, then folded: offset to the centre, closer together, smaller.
  const configs: [number, number, number][] = [];
  let e = small && (m.prop === 2 || m.body === 2) ? 2 : [2, 3][m.es];
  let gap = small ? 2 : [2, 4, 6][m.sp];
  let off = small ? 0 : [-1, 0, 1][m.off];
  for (;;) {
    configs.push([e, gap, off]);
    if (off !== 0) off = 0;
    else if (gap > 2) gap -= 2;
    else if (e > 2) e--;
    else break;
  }
  for (const [e, gap, off] of configs)
    for (const y of [y0, y0 + 1, y0 - 1, y0 + 2, y0 - 2, y0 + 3, y0 + 4]) {
      const cells = face(e, gap, off, y);
      if (!Object.values(cells).every(ok)) continue;
      const work = ([[-1, 1], [-1, 0], [0, 1]] as [number, number][]).find(([dx, dy]) => ok(shift(cells.eo, dx, dy)));
      const up = [2, 1].find((d) => ok(shift(cells.eo, 0, -d)));
      if (!work || !up) continue;
      const look = [2, 1, 0].find((d) => ok(shift(cells.eo, d, 0))) ?? 0;
      const all = [...Object.values(cells).flat(), ...shift(cells.eo, work[0], work[1]), ...shift(cells.eo, 0, -up), ...shift(cells.eo, look, 0)];
      const zone: [number, number, number, number] = [
        Math.min(...all.map((b) => b[0])),
        Math.min(...all.map((b) => b[1])),
        Math.max(...all.map((b) => b[0] + b[2])),
        Math.max(...all.map((b) => b[1] + b[3])),
      ];
      return { cells, gaze: { work, up, look }, zone, fits: true };
    }
  // No placement fits: the critter's own eyes, and the caller makes room.
  const [fe, fg, fo] = configs[0];
  const cells = face(fe, fg, fo, y0);
  const all = Object.values(cells).flat();
  return { cells, gaze: { work: [-1, 1], up: 1, look: 0 }, zone: [Math.min(...all.map((b) => b[0])), y0 - 2, Math.max(...all.map((b) => b[0] + b[2])), y0 + 5], fits: false };
}

/** Which eyes each state shows; status unknown has none, only a dashed outline. */
export const EYES: Record<CritterState, EyeKind | null> = {
  working: 'eo',
  working_failing: 'eo',
  with_coordinator: 'eo',
  in_review: 'eo',
  needs_you: 'ew',
  blocked: 'ew',
  unseen: 'ew',
  idle: 'ec',
  stale: 'ec',
  done: 'eh',
  failing: 'ex',
  unknown: null,
};

// ---- The move on a change of state -----------------------------------------

/** The context key under which the page hands its move tokens to every critter. */
export const MOVES = Symbol('critter-moves');

/** How long a state change can still play its move: a critter that mounts later does not. */
export const MOVE_TTL = 1000;

/** How long the move itself lasts; every move is shorter. */
export const MOVE_MS = 800;

/**
 * The critter states of a list of cards, and the ids whose state differs from
 * `prev`. A card new to the list has no change: it just arrived. With no
 * `prev` (the first snapshot) nothing changed.
 */
export function stateChanges(
  prev: ReadonlyMap<string, CritterState> | null,
  cards: readonly (CritterCard & Pick<Card, 'id'>)[],
): { states: Map<string, CritterState>; changed: string[] } {
  const states = new Map<string, CritterState>();
  const changed: string[] = [];
  for (const c of cards) {
    const s = critterState(c);
    states.set(c.id, s);
    const before = prev?.get(c.id);
    if (before !== undefined && before !== s) changed.push(c.id);
  }
  return { states, changed };
}
