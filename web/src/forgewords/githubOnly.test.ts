// With GitHub only, the GitLab words change nothing a reader sees or hears:
// the text of every view, the hidden link words and the labels are the ones
// recorded before GitLab (the snapshot file next to this test).
import { afterEach, describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import Board from '../views/Board.svelte';
import Lanes from '../views/Lanes.svelte';
import Teams from '../views/Teams.svelte';
import App from '../App.svelte';
import StatusBar from '../components/StatusBar.svelte';
import { indexOf } from '../data/derive';
import { golden } from '../test/fixtures';
import { feedOf, recordActions, render, settle, text } from '../test/render';
import type { CatchupItem, Snapshot } from '../contract.gen';
import type { CatchupClient } from '../data/catchup';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});
const NOW = Date.parse('2026-11-17T10:00:00Z');
const noop = () => {};

/** The golden snapshot with everything GitLab taken out. */
export function githubOnly(): Snapshot {
  const g = golden();
  return {
    ...g,
    sources: g.sources.filter((s) => s.kind !== 'gitlab'),
    projects: g.projects.map((p) => (p.repoForge === 'gitlab' ? { ...p, repoForge: 'github' as const, repoHost: 'github.com' } : p)),
    cards: g.cards.map((c) => ({
      ...c,
      pullRequests: c.pullRequests.filter((p) => p.forge !== 'gitlab'),
      issues: c.issues.filter((i) => i.forge !== 'gitlab'),
    })),
    gitlabHosts: [],
  };
}

/** Everything read out: the text, hidden words included, and every label. */
function heard(el: Element): string {
  const labels = [...el.querySelectorAll('[aria-label]')].map((e) => `${e.tagName.toLowerCase()}: ${e.getAttribute('aria-label')}`);
  return [text(el), ...labels].join('\n');
}

describe('GitHub only: nothing visible changes', () => {
  for (const [name, C] of [['Board', Board], ['Lanes', Lanes], ['Teams', Teams]] as const) {
    it(`${name} reads as before`, () => {
      const s = githubOnly();
      r = render(C as typeof Board, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
      expect(heard(r.target)).toMatchSnapshot();
    });
  }

  it('the status bar and About read as before', () => {
    r = render(StatusBar, { snapshot: githubOnly() });
    r.target.querySelector<HTMLButtonElement>('button.me')!.click();
    flushSync();
    expect(heard(r.target)).toMatchSnapshot();
  });
});

/**
 * Every word kept for screen readers and every label, as the page writes it,
 * whitespace and all (a space added to a hint fails), with the column hints.
 */
function raw(el: Element | Document): string[] {
  return [
    ...[...el.querySelectorAll('.visually-hidden')].map((e) => `hidden: ${JSON.stringify(e.textContent)}`),
    ...[...el.querySelectorAll('[aria-label]')].map((e) => `label: ${JSON.stringify(e.getAttribute('aria-label'))}`),
    ...[...el.querySelectorAll('.hint, .empty, .thead span, .privacy, .k')].map((e) => `text: ${JSON.stringify(e.textContent)}`),
  ];
}

describe('GitHub only: the hidden words and labels, byte for byte', () => {
  for (const [name, C] of [['Board', Board], ['Lanes', Lanes], ['Teams', Teams]] as const) {
    it(`${name}`, () => {
      const s = githubOnly();
      r = render(C as typeof Board, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
      expect(raw(r.target)).toMatchSnapshot();
    });
  }
  it('an empty Board', () => {
    const s = { ...githubOnly(), cards: [] };
    r = render(Board, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
    expect(raw(r.target)).toMatchSnapshot();
  });
  it('About', () => {
    r = render(StatusBar, { snapshot: githubOnly() });
    r.target.querySelector<HTMLButtonElement>('button.me')!.click();
    flushSync();
    expect(raw(r.target)).toMatchSnapshot();
  });
  it('Catch-up', async () => {
    const s = githubOnly();
    const pr = s.cards.flatMap((c) => c.pullRequests)[0];
    const item: CatchupItem = { at: '2026-11-17T09:40:00Z', kind: 'pr_opened', cardId: '', projectId: s.projects[0].id, projectName: 'sample', agentName: 'sample-worker', title: 'Sample', pullRequest: pr };
    const client: CatchupClient = {
      load: async () => ({ ok: true, catchup: { asOf: '2026-11-17T10:00:00Z', total: 2, items: [item, { ...item, at: '2026-11-17T09:30:00Z', kind: 'pr_merged', pullRequest: { ...pr, state: 'merged' } }] } }),
      markRead: async () => ({ ok: true }),
    };
    r = render(App, { feed: feedOf({ conn: 'live', snapshot: s }).feed, actions: recordActions().actions, catchup: client });
    await settle();
    document.querySelector<HTMLButtonElement>('header button.catchup')!.click();
    await settle();
    const panel = document.querySelector('#catchup-panel')!;
    expect(panel.querySelector('li a')).not.toBeNull();
    expect(raw(panel)).toMatchSnapshot();
  });
});
