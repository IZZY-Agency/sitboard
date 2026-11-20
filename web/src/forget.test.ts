import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import App from './App.svelte';
import { card, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle, text } from './test/render';
import type { ActionResult } from './data/actions';
import type { Card, Column, Project, Snapshot } from './contract.gen';

const gp = (id: string, name: string, parentId = '', area = ''): Project => ({ ...project(id, name), parentId, area });
const at = (projectId: string, column: Column = 'working', over: Partial<Card> = {}) => card({ projectId, column, ...over });

// Placeholder names: a grouping with two parts and a card of its own, and a
// project without parts.
function world(split = false): Snapshot {
  return snapshot(
    [at('p_main'), at('p_web'), at('p_web', 'in_review'), at('p_api'), at('p_tool')],
    [
      gp('p_main', 'mainapp', '', 'area-one'),
      gp('p_web', 'web', split ? '' : 'p_main', split ? 'area-one' : ''),
      gp('p_api', 'api', split ? '' : 'p_main', split ? 'area-one' : ''),
      gp('p_tool', 'tool', '', 'area-one'),
    ],
  );
}

let r: ReturnType<typeof render> | null = null;
beforeEach(() => localStorage.clear());
afterEach(() => {
  r?.destroy();
  r = null;
});

function app(answer?: ActionResult, jump?: ActionResult) {
  const f = feedOf({ conn: 'live', snapshot: world() });
  const a = recordActions({ ...(answer ? { forgetGrouping: answer } : {}), ...(jump ? { jump } : {}) });
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
const confirm = () => q('[role="group"][aria-label^="Forget the grouping"]');
const polite = () => text(q('[role="status"][aria-live="polite"]'));

describe('forget grouping, tiles', () => {
  it('shows only in the parts row of a project with parts, outside the chips', async () => {
    app();
    expect(button('Forget grouping')).toBeUndefined();
    await click(tile('tool'));
    expect(button('Forget grouping')).toBeUndefined();
    await click(tile('mainapp'));
    const b = button('Forget grouping')!;
    expect(b).toBeDefined();
    expect(b.tabIndex).toBe(0); // its own tab stop
    expect(b.closest('[role="toolbar"]')).toBeNull(); // not one of the chips the arrows move through
  });

  it('asks first: the confirm names the project as the board does, no path; Keep takes the focus and sits last', async () => {
    const a = app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    expect(a.calls).toEqual([]);
    expect(text(confirm())).toBe(
      'Forget the grouping of mainapp? Its parts are no longer grouped under it. Forget once lets it group again later; Never group keeps them apart. Never group Forget once Keep',
    );
    expect(text(confirm())).not.toContain('~/');
    const acts = [...confirm()!.querySelectorAll('button')].map(text);
    expect(acts).toEqual(['Never group', 'Forget once', 'Keep']); // Keep is where the button was
    expect(document.activeElement).toBe(button('Keep'));
    expect(button('Keep')!.classList.contains('keep')).toBe(true); // the primary look goes with the focus
    expect(button('Forget once')!.classList.contains('keep')).toBe(false);
  });

  it('Keep is at least as wide as the button was, so a double click cannot land on Forget', async () => {
    app();
    await click(tile('mainapp'));
    const b = button('Forget grouping')!;
    b.getBoundingClientRect = () => ({ width: 137.5, height: 32 }) as DOMRect;
    await click(b);
    expect(button('Keep')!.style.minWidth).toBe('138px');
  });

  it('the question is the description of the confirm, so it is read out with the focus', async () => {
    app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    const id = confirm()!.getAttribute('aria-describedby')!;
    expect(text(r!.target.querySelector(`[id="${id}"]`))).toBe(
      'Forget the grouping of mainapp? Its parts are no longer grouped under it. Forget once lets it group again later; Never group keeps them apart.',
    );
  });

  it('a second click or Enter lands on Keep: nothing is forgotten', async () => {
    const a = app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(document.activeElement); // the second click of a double click, or Enter
    expect(a.calls).toEqual([]);
    expect(confirm()).toBeNull();
    expect(document.activeElement).toBe(button('Forget grouping'));
  });

  it('Escape keeps the grouping and returns the focus', async () => {
    const a = app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    key(document.activeElement!, 'Escape');
    await settle();
    expect(confirm()).toBeNull();
    expect(a.calls).toEqual([]);
    expect(document.activeElement).toBe(button('Forget grouping'));
  });

  it('Forget sends the project id, says so, and the parts are ungrouped with the next snapshot', async () => {
    const a = app();
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Forget once'));
    expect(a.calls).toEqual([['forgetGrouping', 'p_main']]);
    expect(polite()).toBe('Forgot the grouping of mainapp. Its parts are no longer grouped under it.');
    expect(document.activeElement).toBe(tile('mainapp'));
    a.push({ conn: 'live', snapshot: { ...world(true), seq: 2 } });
    await settle();
    expect(q('[aria-label="Parts of mainapp"]')).toBeNull();
    expect(button('Forget grouping')).toBeUndefined();
    expect(qa('[aria-label="Projects"] .tile .name').map(text).sort()).toEqual(['all projects', 'api', 'mainapp', 'tool', 'web']);
    expect(document.activeElement).toBe(tile('mainapp'));
  });

  it('the success is said once: a later notice and its dismissal do not bring it back', async () => {
    const a = app(undefined, { ok: false, kind: 'failed', message: 'sitboard answered 500' });
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Forget once'));
    expect(polite()).toContain('Forgot the grouping of mainapp');
    a.push({ conn: 'live', snapshot: { ...world(true), seq: 2 } });
    await settle();
    await click(q('[data-card] button.main')); // the jump fails: a notice
    expect(polite()).not.toContain('Forgot');
    key(document.body, 'Escape');
    await settle();
    expect(polite()).toBe('');
  });

  it('a state file that could not be written shows what failed with a retry, and the grouping stays', async () => {
    const a = app({ ok: false, kind: 'failed', message: 'sitboard could not save it' });
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Forget once'));
    expect(text(q('section[aria-label="Action failed"]'))).toContain('Forget the grouping of mainapp failed: sitboard could not save it.');
    expect(confirm()).toBeNull();
    expect(button('Forget grouping')).toBeDefined();
    await click(button('Retry'));
    expect(a.calls).toEqual([
      ['forgetGrouping', 'p_main'],
      ['forgetGrouping', 'p_main'],
    ]);
  });

  it('a refusal that would fail again (gone, or no longer grouped) offers no retry', async () => {
    for (const message of ['this project is no longer on the board', 'it is no longer grouped']) {
      app({ ok: false, kind: 'failed', message, final: true });
      await click(tile('mainapp'));
      await click(button('Forget grouping'));
      await click(button('Forget once'));
      expect(text(q('section[aria-label="Action failed"]'))).toContain(`Forget the grouping of mainapp failed: ${message}.`);
      expect(button('Retry')).toBeUndefined();
      r!.destroy();
      r = null;
    }
  });

  it('while the request is out the focus stays on Forget, and neither button acts', async () => {
    let answer: (v: ActionResult) => void = () => {};
    const calls: string[] = [];
    const actions = {
      jump: async () => ({ ok: true }) as ActionResult,
      seen: async () => ({ ok: true }) as ActionResult,
      setGrouping: async () => ({ ok: true }) as ActionResult,
      forgetGrouping: (id: string) => {
        calls.push(id);
        return new Promise<ActionResult>((res) => (answer = res));
      },
    };
    const f = feedOf({ conn: 'live', snapshot: world() });
    r = render(App, { feed: f.feed, actions });
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    const forget = button('Forget once')!;
    forget.focus();
    await click(forget);
    expect(document.activeElement).toBe(forget);
    expect(forget.getAttribute('aria-disabled')).toBe('true');
    expect(button('Keep')!.getAttribute('aria-disabled')).toBe('true');
    expect(forget.hasAttribute('disabled')).toBe(false);
    await click(forget);
    await click(button('Keep'));
    expect(calls).toEqual(['p_main']);
    expect(confirm()).not.toBeNull();
    answer({ ok: true });
    await settle();
    expect(polite()).toContain('Forgot the grouping of mainapp');
  });

  it('a double click on Retry sends one forget, and a forget that worked shows no failure', async () => {
    const calls: string[] = [];
    let answer: (v: ActionResult) => void = () => {};
    const actions = {
      ...recordActions().actions,
      forgetGrouping: (id: string) => {
        calls.push(id);
        if (calls.length === 1) return Promise.resolve<ActionResult>({ ok: false, kind: 'failed', message: 'sitboard could not save it' });
        return new Promise<ActionResult>((res) => (answer = res));
      },
    };
    const f = feedOf({ conn: 'live', snapshot: world() });
    r = render(App, { feed: f.feed, actions });
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Forget once'));
    const retry = button('Retry')!;
    retry.click();
    retry.click();
    await settle();
    expect(calls).toEqual(['p_main', 'p_main']);
    answer({ ok: true });
    await settle();
    expect(q('section[aria-label="Action failed"]')).toBeNull();
    expect(polite()).toContain('Forgot the grouping of mainapp');
  });

  it('a lost key shows the open-the-link state', async () => {
    app({ ok: false, kind: 'unauthorized' });
    await click(tile('mainapp'));
    await click(button('Forget grouping'));
    await click(button('Forget once'));
    expect(text(r!.target)).toMatch(/open the link/i);
  });
});

describe('forget grouping, rail', () => {
  beforeEach(() => localStorage.setItem('sitboard.grouping', 'rail'));
  const rail = () => q('nav[aria-label="Projects"]')!;
  const row = (name: string) => [...rail().querySelectorAll<HTMLButtonElement>('[data-key]')].find((b) => text(b.querySelector('.name, .glabel')) === name)!;

  it('the last row under the selected project with parts; the arrows reach it', async () => {
    app();
    expect(button('Forget grouping')).toBeUndefined();
    await click(row('tool'));
    expect(button('Forget grouping')).toBeUndefined();
    await click(row('mainapp'));
    const parts = [...rail().querySelectorAll<HTMLElement>('.parts [data-key]')];
    expect(parts.map(text).slice(-2)).toEqual(['Always group', 'Forget grouping']);
    row('web').focus();
    key(row('web'), 'ArrowDown');
    key(document.activeElement!, 'ArrowDown');
    key(document.activeElement!, 'ArrowDown');
    expect(document.activeElement).toBe(button('Always group'));
    key(document.activeElement!, 'ArrowDown');
    expect(document.activeElement).toBe(button('Forget grouping'));
  });

  it('asks first with Keep first and focused; Forget sends the id and focus goes back to the project', async () => {
    const a = app();
    await click(row('mainapp'));
    await click(button('Forget grouping'));
    expect([...confirm()!.querySelectorAll('button')].map(text)).toEqual(['Keep', 'Forget once', 'Never group']);
    expect(document.activeElement).toBe(button('Keep'));
    key(document.activeElement!, 'Escape');
    await settle();
    expect(a.calls).toEqual([]);
    expect(rail().isConnected).toBe(true);
    await click(button('Forget grouping'));
    await click(button('Forget once'));
    expect(a.calls).toEqual([['forgetGrouping', 'p_main']]);
    expect(document.activeElement).toBe(row('mainapp'));
  });
});
