import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest';
import { flushSync } from 'svelte';
import App from './App.svelte';
import { card, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle, text } from './test/render';
import type { Card, Column, Project, Snapshot } from './contract.gen';

const gp = (id: string, name: string, parentId = '', area = ''): Project => ({ ...project(id, name), parentId, area });
const at = (projectId: string, column: Column = 'working', over: Partial<Card> = {}) => card({ projectId, column, ...over });

// Placeholder names only: two areas, one project with two parts, two idle projects.
function world(): Snapshot {
  return snapshot(
    [
      at('p_main', 'working'),
      at('p_web', 'needs_you', { attention: ['unseen'], seen: false }),
      at('p_web', 'working'),
      at('p_api', 'in_review'),
      at('p_tool', 'working'),
      at('p_quiet', 'idle'),
      at('p_still', 'idle'),
    ],
    [
      gp('p_main', 'mainapp', '', 'area-one'),
      gp('p_web', 'web', 'p_main'),
      gp('p_api', 'api', 'p_main'),
      gp('p_tool', 'tool', '', 'area-one'),
      gp('p_quiet', 'quiet', '', 'area-two'),
      gp('p_still', 'still', '', ''),
    ],
  );
}

let r: ReturnType<typeof render> | null = null;
beforeEach(() => localStorage.clear());
afterEach(() => {
  r?.destroy();
  r = null;
  vi.unstubAllGlobals();
});

function app(s: Snapshot = world()) {
  const f = feedOf({ conn: 'live', snapshot: s });
  r = render(App, { feed: f.feed, actions: recordActions().actions });
  return f;
}
const q = <T extends Element = HTMLElement>(sel: string) => r!.target.querySelector<T>(sel);
const qa = <T extends Element = HTMLElement>(sel: string) => [...r!.target.querySelectorAll<T>(sel)];
const cards = () => qa('[data-card]').length;
async function click(el: Element | null) {
  (el as HTMLElement).click();
  await settle();
}
function key(el: Element, k: string) {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
  flushSync();
}
const tile = (name: string) => qa('[role="toolbar"][aria-label="Projects"] button').find((b) => text(b.querySelector('.name')) === name)!;

describe('tiles (the default)', () => {
  it('all projects, one tile per project with work in hand, idle projects in one box', () => {
    app();
    const names = qa('[aria-label="Projects"] .tile .name').map(text);
    expect(names).toEqual(['all projects', 'mainapp', 'tool']);
    expect(text(q('[aria-label="Idle projects"]'))).toContain('Idle projects · 2');
    expect(qa('[aria-label="Idle projects"] button .name').map(text)).toEqual(['quiet', 'still']);
    expect(tile('all projects').getAttribute('aria-pressed')).toBe('true');
    expect(text(tile('mainapp'))).toContain('2 parts · 1 needs you · 2 working');
    expect(tile('mainapp').classList.contains('needs')).toBe(true);
    expect(tile('mainapp').querySelectorAll('.pip')).toHaveLength(4);
  });

  it('selecting a tile filters every view; header counts stay totals; selecting it again shows all', async () => {
    app();
    await click(tile('tool'));
    expect(cards()).toBe(1);
    expect(text(q('header .totals'))).toBe('7 agents · 4 projects');
    await click(qa('nav[aria-label="View"] button')[1]); // Lanes
    expect(qa('.lanes .row:not(.headrow) .pname').map(text)).toEqual(['tool']);
    expect(text(q('.overview .label'))).toBe('tool');
    await click(qa('nav[aria-label="View"] button')[2]); // Teams
    expect(qa('.trow[data-card]')).toHaveLength(1);
    await click(qa('nav[aria-label="View"] button')[0]);
    await click(tile('tool'));
    expect(cards()).toBe(7);
  });

  it('a project with parts opens a parts row: all parts, then each part, the parent\'s own cards under its name', async () => {
    app();
    expect(q('[aria-label="Parts of mainapp"]')).toBeNull();
    await click(tile('mainapp'));
    expect(cards()).toBe(4);
    const chips = qa('[aria-label="Parts of mainapp"] button');
    expect(chips.map((c) => text(c))).toEqual(['all parts 4', 'web 2', 'api 1', 'mainapp 1']);
    await click(chips[1]);
    expect(cards()).toBe(2);
    expect(tile('mainapp').getAttribute('aria-pressed')).toBe('true'); // the parent stays selected
    await click(qa('[aria-label="Parts of mainapp"] button')[3]);
    expect(cards()).toBe(1);
  });

  it('a card in a part reads "parent / part", the part muted', () => {
    app();
    const label = qa('[data-card] .project').find((e) => text(e).includes('/'))!;
    expect(text(label)).toBe('mainapp / web');
    expect(text(label.querySelector('.part'))).toBe('/ web');
  });

  it('arrow keys move between tiles; one tab stop', () => {
    app();
    const buttons = qa<HTMLButtonElement>('[aria-label="Projects"] button');
    expect(buttons.filter((b) => b.tabIndex === 0)).toHaveLength(1);
    buttons[0].focus();
    key(buttons[0], 'ArrowRight');
    expect(document.activeElement).toBe(buttons[1]);
    key(buttons[1], 'End');
    expect(document.activeElement).toBe(buttons[buttons.length - 1]);
  });

  it('a long name is cut with an ellipsis and keeps the full name in its title', () => {
    const name = 'a-very-long-project-name-'.repeat(6);
    const c = at('p_long');
    app(snapshot([c], [gp('p_long', name)]));
    const n = tile(name).querySelector<HTMLElement>('.name')!;
    expect(n.getAttribute('title')).toBe(name);
    const cs = getComputedStyle(n);
    expect([cs.textOverflow, cs.overflow, cs.whiteSpace]).toEqual(['ellipsis', 'hidden', 'nowrap']);
  });

  it('the parts row keeps its balanced inline chips', async () => {
    app();
    await click(tile('mainapp'));
    const row = q('[aria-label="Parts of mainapp"]')!;
    const between = [...row.childNodes].filter((n) => n.nodeType === Node.TEXT_NODE && n.textContent !== '' && n.textContent!.trim() === '');
    expect(between).toHaveLength(0);
    expect(getComputedStyle(row).getPropertyValue('text-wrap')).toBe('balance');
  });
});

describe('the tiles | rail switch', () => {
  const sw = () => qa<HTMLButtonElement>('[role="group"][aria-label="Group projects as"] button');

  it('is named, defaults to tiles, and is remembered per viewer', async () => {
    app();
    expect(sw().map((b) => [text(b), b.getAttribute('aria-pressed')])).toEqual([
      ['tiles', 'true'],
      ['rail', 'false'],
    ]);
    await click(sw()[1]);
    expect(q('nav[aria-label="Projects"]')).not.toBeNull();
    expect(q('[role="toolbar"][aria-label="Projects"]')).toBeNull();
    expect(localStorage.getItem('sitboard.grouping')).toBe('rail');
    r!.destroy();
    app();
    expect(q('nav[aria-label="Projects"]')).not.toBeNull();
  });

  it('the selection survives a switch', async () => {
    app();
    await click(tile('tool'));
    await click(sw()[1]);
    expect(cards()).toBe(1);
    expect(text(q('nav[aria-label="Projects"] [aria-current="true"]'))).toContain('tool');
  });

  it('the page renders when storage is unavailable', async () => {
    const broken = {
      getItem() {
        throw new Error('denied');
      },
      setItem() {
        throw new Error('denied');
      },
      removeItem() {},
      clear() {},
    };
    vi.stubGlobal('localStorage', broken);
    app();
    expect(cards()).toBe(7);
    await click(sw()[1]);
    expect(q('nav[aria-label="Projects"]')).not.toBeNull();
  });
});

describe('rail', () => {
  beforeEach(() => localStorage.setItem('sitboard.grouping', 'rail'));
  const rail = () => q('nav[aria-label="Projects"]')!;
  const row = (name: string) => [...rail().querySelectorAll<HTMLButtonElement>('[data-key]')].find((b) => text(b.querySelector('.name, .glabel')) === name)!;

  it('all projects, then areas with their projects; a lone area and no area go under "other", last', () => {
    app();
    expect([...rail().querySelectorAll('.ghead')].map((h) => text(h))).toEqual(['area-one 5', 'other 2']);
    expect(text(row('mainapp'))).toContain('4');
  });

  it('an area selects its projects; a project with parts opens on selection and is remembered', async () => {
    app();
    await click(row('area-one'));
    expect(cards()).toBe(5);
    expect(row('area-one').getAttribute('aria-current')).toBe('true');
    expect(row('web')).toBeUndefined();
    await click(row('mainapp'));
    expect(cards()).toBe(4);
    expect(row('mainapp').getAttribute('aria-expanded')).toBe('true');
    await click(row('web'));
    expect(cards()).toBe(2);
    expect(JSON.parse(localStorage.getItem('sitboard.rail.open')!)).toEqual(['p_main']);
  });

  it('a project selected in tiles shows its parts when the rail opens', async () => {
    localStorage.setItem('sitboard.grouping', 'tiles');
    app();
    await click(tile('mainapp'));
    await click(qa('[aria-label="Group projects as"] button')[1]);
    expect(row('mainapp').getAttribute('aria-expanded')).toBe('true');
    expect(row('web')).toBeDefined();
  });

  it('the toggle opens parts without selecting; arrows move and open', async () => {
    app();
    const tog = rail().querySelector<HTMLButtonElement>('.tog')!;
    expect(tog.getAttribute('aria-expanded')).toBe('false');
    await click(tog);
    expect(row('web')).toBeDefined();
    expect(cards()).toBe(7);
    row('all projects').focus();
    key(row('all projects'), 'ArrowDown');
    expect(document.activeElement).toBe(row('area-one'));
    key(document.activeElement!, 'ArrowDown');
    expect(document.activeElement).toBe(row('mainapp'));
    key(row('mainapp'), 'ArrowLeft');
    expect(row('web')).toBeUndefined();
    key(row('mainapp'), 'ArrowRight');
    expect(row('web')).toBeDefined();
  });

  it('narrow screens: the opener opens the rail as a modal drawer; the page behind is inert until it closes', async () => {
    // jsdom has no viewport width for the media query: show the opener as a narrow screen does.
    const narrow = document.head.appendChild(document.createElement('style'));
    narrow.textContent = 'button.opener { display: flex !important; }';
    onTestFinished(() => narrow.remove());
    app();
    const opener = q<HTMLButtonElement>('button.opener')!;
    expect(text(opener)).toBe('Projects all projects');
    expect(rail().getAttribute('role')).toBeNull();
    await click(opener);
    expect(opener.getAttribute('aria-expanded')).toBe('true');
    expect(rail().getAttribute('role')).toBe('dialog');
    expect(rail().getAttribute('aria-modal')).toBe('true');
    expect(rail().getAttribute('aria-label')).toBe('Projects');
    expect(rail().contains(document.activeElement)).toBe(true);
    // Behind it: the header, the cards and the opener are inert; the drawer and its backdrop are not.
    expect(q('header')!.closest('[inert]')).not.toBeNull();
    expect(q('[data-card]')!.closest('[inert]')).not.toBeNull();
    expect(opener.closest('[inert]')).not.toBeNull();
    expect(rail().closest('[inert]')).toBeNull();
    expect(q('button.backdrop')!.closest('[inert]')).toBeNull();
    key(document.activeElement!, 'Escape');
    await settle();
    expect(rail().getAttribute('role')).toBeNull();
    expect(r!.target.querySelector('[inert]')).toBeNull();
    expect(document.activeElement).toBe(opener);
    await click(opener);
    await click(row('tool'));
    expect(rail().getAttribute('aria-modal')).toBeNull(); // choosing closes it
    expect(r!.target.querySelector('[inert]')).toBeNull();
    expect(text(opener)).toBe('Projects tool');
  });

  it('the selected project can be closed, and stays closed until another selection', async () => {
    app();
    await click(row('mainapp'));
    expect(row('web')).toBeDefined();
    await click(rail().querySelector('.tog'));
    expect(row('web')).toBeUndefined();
    expect(row('mainapp').getAttribute('aria-expanded')).toBe('false');
    await click(row('tool'));
    await click(row('mainapp'));
    expect(row('web')).toBeDefined();
  });

  it('each row says its state in words, not only in dot colours', () => {
    app();
    expect(text(row('mainapp').querySelector('.visually-hidden'))).toBe(', 2 parts · 1 needs you · 2 working');
    expect(text(row('all projects').querySelector('.visually-hidden'))).toContain('needs you');
  });

  it('Right on an open project moves into its first part; Left goes back up', async () => {
    app();
    await click(rail().querySelector('.tog'));
    row('mainapp').focus();
    key(row('mainapp'), 'ArrowRight');
    expect(document.activeElement).toBe(row('web'));
    key(row('web'), 'ArrowLeft');
    expect(document.activeElement).toBe(row('mainapp'));
  });

  it('focus stays in the rail when its row closes under it', async () => {
    app();
    await click(rail().querySelector('.tog'));
    row('web').focus();
    await click(rail().querySelector('.tog'));
    expect(rail().contains(document.activeElement)).toBe(true);
  });
});

describe('selection across modes and updates', () => {
  it('an area chosen in the rail shows as a pressed "area" tile; pressing it clears the selection', async () => {
    localStorage.setItem('sitboard.grouping', 'rail');
    app();
    await click([...q('nav[aria-label="Projects"]')!.querySelectorAll('.ghead')][0]);
    expect(cards()).toBe(5);
    await click(qa('[aria-label="Group projects as"] button')[0]);
    const chip = q('[aria-label="Projects"] .tile.area')!;
    expect(text(chip.querySelector('.name'))).toBe('area: area-one');
    expect(chip.getAttribute('aria-pressed')).toBe('true');
    await click(chip);
    expect(cards()).toBe(7);
    expect(q('[aria-label="Projects"] .tile.area')).toBeNull();
  });

  it('a focused tile whose project goes idle keeps focus in the idle box; one that leaves hands it on', async () => {
    const f = app();
    tile('tool').focus();
    const idle = world();
    idle.cards.find((c) => c.projectId === 'p_tool')!.column = 'idle';
    f.push({ conn: 'live', snapshot: idle });
    await settle();
    expect((document.activeElement as HTMLElement).dataset.key).toBe('p_tool');
    expect(document.activeElement!.classList.contains('ibtn')).toBe(true);
    const gone = world();
    gone.cards = gone.cards.filter((c) => c.projectId !== 'p_tool');
    f.push({ conn: 'live', snapshot: gone });
    await settle();
    expect(q('[role="toolbar"][aria-label="Projects"]')!.contains(document.activeElement)).toBe(true);
  });

});
