export type View = 'board' | 'lanes' | 'teams';
export const VIEWS: readonly View[] = ['board', 'lanes', 'teams'];
export const VIEW_KEY = 'sitboard.view';

function storage(): Storage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

export function loadView(s: Storage | undefined = storage()): View {
  try {
    const v = s?.getItem(VIEW_KEY);
    return VIEWS.includes(v as View) ? (v as View) : 'board';
  } catch {
    return 'board';
  }
}

export function saveView(v: View, s: Storage | undefined = storage()): void {
  try {
    s?.setItem(VIEW_KEY, v);
  } catch {
    // Storage blocked: the choice lasts for this page only.
  }
}

// How projects are grouped: a band of tiles above the view, or a rail beside it.
export type Grouping = 'tiles' | 'rail';
export const GROUPING_KEY = 'sitboard.grouping';

export function loadGrouping(s: Storage | undefined = storage()): Grouping {
  try {
    return s?.getItem(GROUPING_KEY) === 'rail' ? 'rail' : 'tiles';
  } catch {
    return 'tiles';
  }
}

export function saveGrouping(g: Grouping, s: Storage | undefined = storage()): void {
  try {
    s?.setItem(GROUPING_KEY, g);
  } catch {
    // Storage blocked: the choice lasts for this page only.
  }
}

// The rail projects the viewer opened to show their parts.
export const OPEN_KEY = 'sitboard.rail.open';
const OPEN_MAX = 200;

export function loadOpen(s: Storage | undefined = storage()): Set<string> {
  try {
    const v: unknown = JSON.parse(s?.getItem(OPEN_KEY) ?? '[]');
    return new Set(Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string').slice(0, OPEN_MAX) : []);
  } catch {
    return new Set();
  }
}

export function saveOpen(open: ReadonlySet<string>, s: Storage | undefined = storage()): void {
  try {
    s?.setItem(OPEN_KEY, JSON.stringify([...open].slice(-OPEN_MAX)));
  } catch {
    // Storage blocked: the rail forgets on reload.
  }
}
