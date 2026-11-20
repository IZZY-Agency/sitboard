import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import App from './App.svelte';
import { card, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle, text } from './test/render';
import type { ActionResult } from './data/actions';
import type { Card, Column, GroupingSetting, Project, Snapshot } from './contract.gen';

const gp = (id: string, name: string, parentId = '', area = ''): Project => ({ ...project(id, name), parentId, area });
const at = (projectId: string, column: Column = 'working', over: Partial<Card> = {}) => card({ projectId, column, ...over });
const setting = (projectId: string, name: string, mode: GroupingSetting['mode'], path = `~/projects/${name}`): GroupingSetting => ({ projectId, name, path, mode });

// Placeholder names: a grouping with two parts, and a project without parts.
function world(settings: GroupingSetting[] = [], split = false): Snapshot {
  return snapshot(
    [at('p_main'), at('p_web'), at('p_api'), at('p_tool')],
    [
      gp('p_main', 'mainapp', '', 'area-one'),
      gp('p_web', 'web', split ? '' : 'p_main', split ? 'area-one' : ''),
      gp('p_api', 'api', split ? '' : 'p_main', split ? 'area-one' : ''),
      gp('p_tool', 'tool', '', 'area-one'),
    ],
    { groupingSettings: settings },
  );
}

let r: ReturnType<typeof render> | null = null;
beforeEach(() => localStorage.clear());
afterEach(() => {
  r?.destroy();
  r = null;
});

function app(settings: GroupingSetting[] = [], answer?: ActionResult) {
  const f = feedOf({ conn: 'live', snapshot: world(settings) });
  const a = recordActions(answer ? { setGrouping: answer } : {});
  r = render(App, { feed: f.feed, actions: a.actions });
  return { ...f, calls: a.calls };
}
const q = <T extends Element = HTMLElement>(sel: string) => r!.target.querySelector<T>(sel);
const qa = <T extends Element = HTMLElement>(sel: string) => [...r!.target.querySelectorAll<T>(sel)];
async function click(el: Element | null | undefined) {
  (el as HTMLElement).click();
  await settle();
}
function key(el: Element, k: string) {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
  flushSync();
}
const button = (label: string) => qa<HTMLButtonElement>('button').find((b) => text(b) === label);
const tile = (name: string) => qa('[role="toolbar"][aria-label="Projects"] button').find((b) => text(b.querySelector('.name')) === name)!;
const polite = () => text(q('[role="status"][aria-live="polite"]'));
const entry = () => q<HTMLButtonElement>('footer button.setting');
const panel = () => q('[role="dialog"][aria-labelledby="grouping-settings-title"]');

describe('grouping setting, tiles', () => {
  it('Always group sits before Forget grouping in the parts row, outside the chips, and sends always', async () => {
    const a = app();
    await click(tile('mainapp'));
    const b = button('Always group')!;
    expect(b).toBeDefined();
    expect(b.closest('[role="toolbar"]')).toBeNull();
    const ends = qa('.parts button').map(text);
    expect(ends.slice(-2)).toEqual(['Always group', 'Forget grouping']);
    b.focus();
    await click(b);
    expect(a.calls).toEqual([['setGrouping', 'p_main', 'always']]);
    expect(polite()).toBe(''); // said once a snapshot shows it
    // The next snapshot lists it: it is said, the same button now says Back
    // to auto, keeps the focus, and Forget grouping is gone.
    a.push({ conn: 'live', snapshot: { ...world([setting('p_main', 'mainapp', 'always')]), seq: 2 } });
    await settle();
    expect(polite()).toBe('mainapp always groups now.');
    expect(text(b)).toBe('Back to auto');
    expect(document.activeElement).toBe(b);
    expect(button('Forget grouping')).toBeUndefined();
    const label = r!.target.querySelector(`[id="${b.getAttribute('aria-describedby')}"]`);
    expect(text(label)).toBe('Grouping always');
    await click(b);
    expect(a.calls.at(-1)).toEqual(['setGrouping', 'p_main', 'auto']);
    a.push({ conn: 'live', snapshot: { ...world([]), seq: 3 } });
    await settle();
    expect(polite()).toBe('mainapp is back to auto grouping.');
  });

  it('two tabs at once: the announcement says what the server ended up with, not what this tab asked', async () => {
    const a = app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Never group'));
    // The other tab's always won: the next snapshot shows always.
    a.push({ conn: 'live', snapshot: { ...world([setting('p_main', 'mainapp', 'always')]), seq: 2 } });
    await settle();
    expect(polite()).toBe('mainapp always groups now.');
  });

  it('Never group, from the forget confirm, sends never (not a forget) and focus goes back to the project', async () => {
    const a = app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    expect(button('Always group')).toBeUndefined(); // the confirm takes the row
    await click(button('Never group'));
    expect(a.calls).toEqual([['setGrouping', 'p_main', 'never']]);
    expect(document.activeElement).toBe(tile('mainapp'));
    a.push({ conn: 'live', snapshot: { ...world([setting('p_main', 'mainapp', 'never')], true), seq: 2 } });
    await settle();
    expect(polite()).toBe('mainapp never groups now. Its parts are no longer grouped under it.');
    expect(q('[aria-label="Parts of mainapp"]')).toBeNull();
    expect(text(entry())).toBe('1 folder never groups');
  });

  it('set to always elsewhere while the forget confirm is open: the confirm goes and Back to auto shows', async () => {
    const a = app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    a.push({ conn: 'live', snapshot: { ...world([setting('p_main', 'mainapp', 'always')]), seq: 2 } });
    await settle();
    expect(q('[role="group"][aria-label^="Forget the grouping"]')).toBeNull();
    expect(button('Back to auto')).toBeDefined();
  });

  it('a setting that could not be saved shows what failed with a retry, and the confirm closes', async () => {
    const a = app([], { ok: false, kind: 'failed', message: 'sitboard could not save it' });
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Never group'));
    expect(text(q('section[aria-label="Action failed"]'))).toContain('Set mainapp to never group failed: sitboard could not save it.');
    expect(button('Forget grouping')).toBeDefined();
    await click(button('Retry'));
    expect(a.calls).toEqual([
      ['setGrouping', 'p_main', 'never'],
      ['setGrouping', 'p_main', 'never'],
    ]);
  });

  it('while Always group is out it says so and a second press sends nothing', async () => {
    let answer: (v: ActionResult) => void = () => {};
    const calls: string[] = [];
    const actions = {
      ...recordActions().actions,
      setGrouping: (id: string, mode: string) => {
        calls.push(`${id} ${mode}`);
        return new Promise<ActionResult>((res) => (answer = res));
      },
    };
    r = render(App, { feed: feedOf({ conn: 'live', snapshot: world() }).feed, actions });
    await click(tile('mainapp'));
    const b = button('Always group')!;
    await click(b);
    expect(b.getAttribute('aria-disabled')).toBe('true');
    await click(b);
    expect(calls).toEqual(['p_main always']);
    answer({ ok: true });
    await settle();
    expect(b.getAttribute('aria-disabled')).toBe('false');
  });
});

describe('grouping setting, rail', () => {
  beforeEach(() => localStorage.setItem('sitboard.grouping', 'rail'));
  const rail = () => q('nav[aria-label="Projects"]')!;
  const row = (name: string) => [...rail().querySelectorAll<HTMLButtonElement>('[data-key]')].find((b) => text(b.querySelector('.name, .glabel')) === name)!;

  it('Always group is a row under the parts; set to always, the row says Back to auto and Forget grouping goes', async () => {
    const a = app();
    await click(row('mainapp'));
    await click(button('Always group'));
    expect(a.calls).toEqual([['setGrouping', 'p_main', 'always']]);
    a.push({ conn: 'live', snapshot: { ...world([setting('p_main', 'mainapp', 'always')]), seq: 2 } });
    await settle();
    const parts = [...rail().querySelectorAll<HTMLElement>('.parts [data-key]')].map(text);
    expect(parts.at(-1)).toBe('Back to auto');
    expect(button('Forget grouping')).toBeUndefined();
    expect(text(rail())).toContain('Grouping always');
  });

  it('Never group is the last answer of the rail confirm', async () => {
    const a = app();
    await click(row('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Never group'));
    expect(a.calls).toEqual([['setGrouping', 'p_main', 'never']]);
    expect(document.activeElement).toBe(row('mainapp'));
  });
});

describe('grouping settings, status bar', () => {
  it('no entry without settings; it counts the folders set to never, else those set to always', async () => {
    app();
    expect(entry()).toBeNull();
    r!.destroy();
    for (const [settings, want] of [
      [[setting('p_a', 'alpha', 'never'), setting('p_b', 'beta', 'always')], '1 folder never groups'],
      [[setting('p_a', 'alpha', 'never'), setting('p_c', 'gamma', 'never')], '2 folders never group'],
      [[setting('p_b', 'beta', 'always')], '1 folder always groups'],
      [[setting('p_b', 'beta', 'always'), setting('p_d', 'delta', 'always')], '2 folders always group'],
    ] as const) {
      app([...settings]);
      expect(text(entry())).toBe(want);
      r!.destroy();
    }
    r = null;
  });

  it('opens a panel that lists every folder with its setting and path, and Escape closes it back to the entry', async () => {
    app([setting('p_a', 'alpha', 'never', '~/projects/alpha'), setting('p_b', 'beta', 'always', '~/projects/beta')]);
    expect(panel()).toBeNull();
    await click(entry());
    expect(entry()!.getAttribute('aria-expanded')).toBe('true');
    expect(entry()!.getAttribute('aria-controls')).toBe('grouping-settings');
    expect(document.activeElement).toBe(panel());
    const rows = [...panel()!.querySelectorAll('li')].map((li) => [text(li.querySelector('.name')), text(li.querySelector('.mode')), text(li.querySelector('.path'))]);
    expect(rows).toEqual([
      ['alpha', 'never', '~/projects/alpha'],
      ['beta', 'always', '~/projects/beta'],
    ]);
    expect(text(panel()!.querySelector('li button'))).toBe('Back to auto for alpha, set to never');
    key(panel()!, 'Escape');
    await settle();
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(entry());
  });

  it('Back to auto sends the listed id; the focus moves to the row that took its place, then the panel and entry go', async () => {
    const settings = [setting('p_a', 'alpha', 'never'), setting('p_b', 'beta', 'never')];
    const a = app(settings);
    await click(entry());
    const first = panel()!.querySelector<HTMLButtonElement>('li button')!;
    first.focus();
    await click(first);
    expect(a.calls).toEqual([['setGrouping', 'p_a', 'auto']]);
    a.push({ conn: 'live', snapshot: { ...world([settings[1]]), seq: 2 } });
    await settle();
    expect(polite()).toBe('alpha is back to auto grouping.');
    expect(document.activeElement).toBe(panel()!.querySelector('li button'));
    expect(text(document.activeElement)).toBe('Back to auto for beta, set to never');
    await click(document.activeElement);
    a.push({ conn: 'live', snapshot: { ...world([]), seq: 3 } });
    await settle();
    expect(panel()).toBeNull();
    expect(entry()).toBeNull();
    expect(document.activeElement).toBe(q('footer button.me'));
  });

  it('only one panel at a time: About closes the settings, and the other way round', async () => {
    app([setting('p_a', 'alpha', 'never')]);
    await click(entry());
    expect(panel()).not.toBeNull();
    await click(q('footer button.me'));
    await settle();
    expect(panel()).toBeNull();
    expect(q('#about-sitboard')).not.toBeNull();
    await click(entry());
    expect(q('#about-sitboard')).toBeNull();
    expect(panel()).not.toBeNull();
  });

  it('opened from the keyboard while the other panel is open (focus on its button), one closes the other', async () => {
    app([setting('p_a', 'alpha', 'never')]);
    const me = q<HTMLButtonElement>('footer button.me')!;
    await click(entry());
    entry()!.focus(); // back on the entry: the panel stays open
    me.focus();
    await settle();
    expect(panel()).not.toBeNull();
    await click(me); // Enter on the About button: no pointer down
    expect(q('#about-sitboard')).not.toBeNull();
    expect(panel()).toBeNull();
    me.focus();
    entry()!.focus();
    await settle();
    expect(q('#about-sitboard')).not.toBeNull();
    await click(entry());
    expect(panel()).not.toBeNull();
    expect(q('#about-sitboard')).toBeNull();
  });

  it('names and paths are text, never markup', async () => {
    app([setting('p_x', '<img src=x onerror="alert(1)">', 'never', '~/<b>bold</b>')]);
    await click(entry());
    expect(panel()!.querySelector('img, b')).toBeNull();
    expect(text(panel()!.querySelector('.name'))).toBe('<img src=x onerror="alert(1)">');
  });
});
