// Arrow-key movement among the buttons of a toolbar or a list. Each control
// keeps one tab stop; the arrows, Home and End move between its items.

const KEYS = {
  x: { prev: ['ArrowLeft'], next: ['ArrowRight'] },
  y: { prev: ['ArrowUp'], next: ['ArrowDown'] },
  both: { prev: ['ArrowLeft', 'ArrowUp'], next: ['ArrowRight', 'ArrowDown'] },
} as const;

/** Moves focus for an arrow, Home or End key; returns whether it handled the key. */
export function moveFocus(e: KeyboardEvent, root: HTMLElement, selector: string, axis: keyof typeof KEYS): boolean {
  const k = KEYS[axis];
  const prev = (k.prev as readonly string[]).includes(e.key);
  const next = (k.next as readonly string[]).includes(e.key);
  if (!prev && !next && e.key !== 'Home' && e.key !== 'End') return false;
  const items = [...root.querySelectorAll<HTMLElement>(selector)];
  const i = items.indexOf(document.activeElement as HTMLElement);
  if (i < 0 || items.length === 0) return false;
  const to = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : prev ? Math.max(0, i - 1) : Math.min(items.length - 1, i + 1);
  e.preventDefault();
  items[to].focus();
  return true;
}

/** The one key that holds the tab stop: the focused item, else the selected one, else the first. */
export function tabStop(keys: readonly string[], focused: string | null, selected: string | null): string | null {
  if (focused && keys.includes(focused)) return focused;
  if (selected && keys.includes(selected)) return selected;
  return keys[0] ?? null;
}

/**
 * Container listeners for keyboard movement, as an attachment: the container
 * itself is not a control and never takes focus, its buttons do.
 */
export function onKeys(keydown: (e: KeyboardEvent) => void, focusin?: (e: FocusEvent) => void) {
  return (node: HTMLElement) => {
    node.addEventListener('keydown', keydown);
    if (focusin) node.addEventListener('focusin', focusin);
    return () => {
      node.removeEventListener('keydown', keydown);
      if (focusin) node.removeEventListener('focusin', focusin);
    };
  };
}

/**
 * Keeps focus in a control across an update. Call `before()` ahead of the DOM
 * update and `after()` once it is done: when the focused item left the DOM,
 * focus goes to the item with the same key if it moved, else to the item now
 * at its place, else to the last one.
 */
export function focusKeeper(root: () => HTMLElement | undefined, selector: string) {
  let el: HTMLElement | null = null;
  let key: string | undefined;
  let index = -1;
  return {
    before() {
      const r = root();
      const a = document.activeElement as HTMLElement | null;
      if (!r || !a || !r.contains(a)) {
        el = null;
        return;
      }
      el = a;
      key = a.dataset.key;
      index = [...r.querySelectorAll<HTMLElement>(selector)].indexOf(a);
    },
    after() {
      const r = root();
      if (!r || !el || el.isConnected) return;
      el = null;
      const items = [...r.querySelectorAll<HTMLElement>(selector)];
      const same = key ? items.find((x) => x.dataset.key === key) : undefined;
      (same ?? items[Math.min(Math.max(index, 0), items.length - 1)])?.focus();
    },
  };
}

/**
 * Makes everything on the page inert except `keep` and the elements given:
 * the content behind a modal cannot be reached by Tab or pointer. Returns the
 * function that restores it.
 */
export function isolate(keep: Element, also: readonly Element[] = []): () => void {
  const made: HTMLElement[] = [];
  let node: Element | null = keep;
  while (node && node.parentElement) {
    for (const sib of node.parentElement.children) {
      if (sib === node || also.includes(sib) || !(sib instanceof HTMLElement) || sib.hasAttribute('inert')) continue;
      if (also.some((a) => sib.contains(a))) continue;
      sib.setAttribute('inert', '');
      made.push(sib);
    }
    node = node.parentElement;
    if (node === document.body) break;
  }
  return () => {
    for (const el of made) el.removeAttribute('inert');
  };
}
