import { describe, expect, it } from 'vitest';
import { loadView, saveView, VIEW_KEY } from './view';

function memory(): Storage {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, v),
  };
}

describe('view choice', () => {
  it('is kept in localStorage under "sitboard.view"', () => {
    const s = memory();
    saveView('teams', s);
    expect(VIEW_KEY).toBe('sitboard.view');
    expect(s.getItem('sitboard.view')).toBe('teams');
    expect(loadView(s)).toBe('teams');
  });
  it('defaults to "board"', () => {
    expect(loadView(memory())).toBe('board');
  });
  it('ignores an unknown stored value', () => {
    const s = memory();
    s.setItem(VIEW_KEY, 'catchup');
    expect(loadView(s)).toBe('board');
  });
  it('defaults to "board" when storage is unavailable', () => {
    const broken = {
      getItem() {
        throw new Error('denied');
      },
      setItem() {
        throw new Error('denied');
      },
    } as unknown as Storage;
    expect(loadView(broken)).toBe('board');
    expect(() => saveView('lanes', broken)).not.toThrow();
    expect(loadView(undefined)).toBe('board');
  });
});

describe('grouping choice and open rail projects', () => {
  it('the grouping is kept under "sitboard.grouping", tiles by default, unknown values ignored', async () => {
    const { loadGrouping, saveGrouping, GROUPING_KEY } = await import('./view');
    const s = memory();
    expect(loadGrouping(s)).toBe('tiles');
    saveGrouping('rail', s);
    expect(GROUPING_KEY).toBe('sitboard.grouping');
    expect(loadGrouping(s)).toBe('rail');
    s.setItem(GROUPING_KEY, 'grid');
    expect(loadGrouping(s)).toBe('tiles');
  });
  it('open rail projects are kept as a list of ids; junk reads as none', async () => {
    const { loadOpen, saveOpen, OPEN_KEY } = await import('./view');
    const s = memory();
    expect([...loadOpen(s)]).toEqual([]);
    saveOpen(new Set(['p_a', 'p_b']), s);
    expect([...loadOpen(s)]).toEqual(['p_a', 'p_b']);
    s.setItem(OPEN_KEY, '{not json');
    expect([...loadOpen(s)]).toEqual([]);
    s.setItem(OPEN_KEY, JSON.stringify([1, 'p_c', null]));
    expect([...loadOpen(s)]).toEqual(['p_c']);
    s.setItem(OPEN_KEY, JSON.stringify({ p_a: true }));
    expect([...loadOpen(s)]).toEqual([]);
  });
  it('open rail projects are capped at 200, keeping the most recent', async () => {
    const { loadOpen, saveOpen, OPEN_KEY } = await import('./view');
    const s = memory();
    const ids = Array.from({ length: 250 }, (_, i) => `p_${i}`);
    saveOpen(new Set(ids), s);
    const stored = JSON.parse(s.getItem(OPEN_KEY)!) as string[];
    expect(stored).toHaveLength(200);
    expect(stored[0]).toBe('p_50');
    expect(stored[199]).toBe('p_249');
    s.setItem(OPEN_KEY, JSON.stringify(ids));
    expect(loadOpen(s).size).toBe(200);
  });
  it('blocked storage: defaults, and saving does not throw', async () => {
    const { loadGrouping, saveGrouping, loadOpen, saveOpen } = await import('./view');
    const broken = {
      getItem() {
        throw new Error('denied');
      },
      setItem() {
        throw new Error('denied');
      },
    } as unknown as Storage;
    expect(loadGrouping(broken)).toBe('tiles');
    expect(() => saveGrouping('rail', broken)).not.toThrow();
    expect(loadOpen(broken).size).toBe(0);
    expect(() => saveOpen(new Set(['x']), broken)).not.toThrow();
  });
});
