import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import StatusBar from './StatusBar.svelte';
import { render, settle, text } from '../test/render';
import type { Build } from '../contract.gen';
import { snapshot } from '../test/fixtures';

const BUILD: Build = { version: '0.1.0', commit: 'abc1234', os: 'darwin', arch: 'arm64' };

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
  vi.unstubAllGlobals();
});

// No build means no snapshot yet: the status bar knows the build only from the snapshot.
function bar(build: Build | null = BUILD) {
  r = render(StatusBar, { snapshot: build ? snapshot([], [], { build }) : null });
  return r;
}
const q = <T extends Element = HTMLElement>(sel: string) => r!.target.querySelector<T>(sel);
const button = () => q<HTMLButtonElement>('button.me')!;
const dialog = () => q('[role="dialog"]');
function open() {
  button().click();
  flushSync();
}
// Focus moves into the panel one tick after it opens.
async function openFocused() {
  open();
  await settle();
}
function key(el: Element, k: string, shift = false) {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: k, shiftKey: shift, bubbles: true }));
  flushSync();
}

describe('About', () => {
  it('the status bar name is a button: its label carries the version, its name starts with "About sitboard"', () => {
    bar();
    expect(text(button())).toBe('About sitboard 0.1.0');
    expect(text(button().querySelector('.name'))).toBe('sitboard 0.1.0');
    expect(button().getAttribute('aria-expanded')).toBe('false');
    expect(button().querySelector('.caret')).not.toBeNull();
    r!.destroy();
    bar({ ...BUILD, version: 'abc1234-dirty' });
    expect(text(button().querySelector('.name'))).toBe('sitboard dev');
    r!.destroy();
    bar(null);
    expect(text(button().querySelector('.name'))).toBe('sitboard');
  });

  it('opens a labelled dialog with the build facts, the privacy line, the update command and the links; focus moves in', async () => {
    bar();
    await openFocused();
    const d = dialog()!;
    expect(button().getAttribute('aria-expanded')).toBe('true');
    expect(button().getAttribute('aria-controls')).toBe(d.id);
    expect(d.getAttribute('aria-modal')).toBeNull();
    expect(text(document.getElementById(d.getAttribute('aria-labelledby')!))).toBe('About sitboard');
    expect(d.contains(document.activeElement)).toBe(true);
    expect(d.querySelector('svg.mark')).not.toBeNull();
    const rows = [...d.querySelectorAll('dl.build > div')].map((e) => text(e));
    expect(rows).toEqual(['Version 0.1.0', 'Commit abc1234', 'Platform macOS arm64']);
    expect(text(d)).toContain('Runs only on this machine. Nothing is sent anywhere; GitHub is read through your own gh.');
    expect(text(d.querySelector('code'))).toBe('brew upgrade sitboard');
    const links = [...d.querySelectorAll('a')];
    expect(links.map((a) => a.getAttribute('href'))).toEqual([
      'https://sitboard.com',
      'https://github.com/IZZY-Agency/sitboard',
      'https://github.com/IZZY-Agency/sitboard/blob/main/LICENSE',
      '/licenses.txt',
      'https://izzy.agency',
    ]);
    for (const a of links) {
      expect(a.getAttribute('target')).toBe('_blank');
      expect(a.getAttribute('rel')).toBe('noopener noreferrer');
    }
  });

  it('a development build says so; before the first snapshot the build rows are left out', () => {
    bar({ ...BUILD, version: 'abc1234' });
    open();
    expect(text(dialog()!.querySelector('dl.build'))).toContain('Version development build');
    r!.destroy();
    bar(null);
    open();
    expect(dialog()!.querySelector('dl.build')).toBeNull();
  });

  it('Escape closes it and returns focus to the button', async () => {
    bar();
    await openFocused();
    key(document.activeElement!, 'Escape');
    expect(dialog()).toBeNull();
    expect(button().getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(button());
  });

  it('a click outside on something that is not a control closes it and returns focus to the button', async () => {
    bar();
    open();
    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
    flushSync();
    expect(dialog()).toBeNull();
    await settle();
    expect(document.activeElement).toBe(button());
  });

  it('a click outside on a control closes it and leaves focus with that control', async () => {
    const other = document.createElement('button');
    document.body.appendChild(other);
    try {
      bar();
      open();
      other.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
      other.focus(); // what a browser that focuses on click does next
      await settle();
      expect(dialog()).toBeNull();
      expect(document.activeElement).toBe(other);
    } finally {
      other.remove();
    }
  });

  it('a click outside on a control that does not take focus (Safari) does not hand focus to the About button', async () => {
    const other = document.createElement('a');
    other.href = '#x';
    document.body.appendChild(other);
    try {
      bar();
      open();
      other.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
      await settle();
      expect(dialog()).toBeNull();
      expect(document.activeElement).not.toBe(button());
    } finally {
      other.remove();
    }
  });

  it('a click inside does not close it; the button toggles it', () => {
    bar();
    open();
    dialog()!.querySelector('code')!.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
    flushSync();
    expect(dialog()).not.toBeNull();
    open();
    expect(dialog()).toBeNull();
  });

  it('a non-modal disclosure: Tab is not trapped, and focus moving outside closes it without being pulled back', async () => {
    const other = document.createElement('button');
    document.body.appendChild(other);
    try {
      bar();
      open();
      const links = [...dialog()!.querySelectorAll<HTMLElement>('a')];
      const last = links[links.length - 1];
      last.focus();
      const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
      last.dispatchEvent(ev);
      expect(ev.defaultPrevented).toBe(false);
      other.focus();
      await settle();
      expect(dialog()).toBeNull();
      expect(document.activeElement).toBe(other);
    } finally {
      other.remove();
    }
  });

  it('focus moving between the panel and its button keeps it open', async () => {
    bar();
    open();
    button().focus();
    await settle();
    expect(dialog()).not.toBeNull();
  });

  it('Escape on the button closes an open panel', () => {
    bar();
    open();
    button().focus();
    key(button(), 'Escape');
    expect(dialog()).toBeNull();
    expect(document.activeElement).toBe(button());
  });

  it('opening moves focus into the panel from the status bar, so a reopened panel gets it too', async () => {
    bar();
    open();
    await settle();
    expect(dialog()!.contains(document.activeElement)).toBe(true);
    key(document.activeElement!, 'Escape');
    open();
    await settle();
    expect(dialog()!.contains(document.activeElement)).toBe(true);
  });

  it('Copy writes the command and shows "Copied"', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    bar();
    await openFocused();
    const copy = dialog()!.querySelector<HTMLButtonElement>('button.copy')!;
    expect(text(copy)).toBe('Copy');
    copy.click();
    await settle();
    expect(writeText).toHaveBeenCalledWith('brew upgrade sitboard');
    expect(text(copy)).toBe('Copied');
    expect(text(dialog()!.querySelector('[role="status"]'))).toBe('Copied to the clipboard');
  });

  it('when the clipboard is refused, the command is selected instead', async () => {
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText: () => Promise.reject(new Error('denied')) } });
    bar();
    await openFocused();
    const copy = dialog()!.querySelector<HTMLButtonElement>('button.copy')!;
    copy.click();
    await settle();
    expect(window.getSelection()!.toString()).toBe('brew upgrade sitboard');
    expect(text(copy)).toBe('Selected');
    expect(text(dialog()!.querySelector('[role="status"]'))).toBe('Selected; copy it with your keyboard');
  });

  it('build text is shown as text only', () => {
    bar({ ...BUILD, commit: '<img src=x onerror=alert(1)>' });
    open();
    expect(dialog()!.querySelector('img')).toBeNull();
    expect(text(dialog()!.querySelector('dl.build'))).toContain('<img src=x');
  });
});
