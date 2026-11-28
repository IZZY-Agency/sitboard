// The tab's own signal: its title says how many agents need you, and its icon
// carries the same number. Seen from another tab, or with the board behind
// other windows, the browser's tab strip is where "needs you" shows.

import type { Build, Card } from '../contract.gen';
import type { Conn } from './client';

/** What the tab says: a count while live, nothing before the first snapshot, "?" when not live. */
export type TabSignal = { kind: 'plain' } | { kind: 'count'; n: number } | { kind: 'lost'; word: string };

/** Agents in Needs you, all projects. A card the owner marked seen here no longer counts. */
export function needsYouCount(cards: readonly Card[]): number {
  return cards.filter((c) => c.column === 'needs_you' && (c.attention.includes('blocked') || c.attention.includes('unseen'))).length;
}

// The same words as the header's live mark.
const LOST_WORD: Record<Exclude<Conn, 'live' | 'connecting'>, string> = {
  reconnecting: 'reconnecting',
  stopped: 'stopped',
  unreachable: 'not connected',
  unauthorized: 'stopped',
  outdated: 'stopped',
};

export function tabSignal(conn: Conn, cards: readonly Card[] | null): TabSignal {
  if (conn === 'connecting') return { kind: 'plain' };
  if (conn !== 'live') return { kind: 'lost', word: LOST_WORD[conn] };
  if (!cards) return { kind: 'plain' };
  const n = needsYouCount(cards);
  return n ? { kind: 'count', n } : { kind: 'plain' };
}

export const TITLE = 'sitboard';

/** "(3) sitboard": the number first, so a narrow tab still shows it. */
export function tabTitle(s: TabSignal): string {
  switch (s.kind) {
    case 'count':
      return `(${s.n}) ${TITLE}`;
    case 'lost':
      return `${TITLE} · ${s.word}`;
    default:
      return TITLE;
  }
}

/**
 * The build the badges belong to. sitboard lets the browser keep a badge for
 * a week, so each address carries the build: a new release's artwork is a
 * new address, never an old file from the cache. Empty before any snapshot.
 */
export function iconVersion(build: Build | null | undefined): string {
  return build ? `${build.version}+${build.commit}` : '';
}

/** The icon file for a signal; the files are written by scripts/badges.mjs. */
export function tabIcon(s: TabSignal, version = ''): string {
  const v = version ? `?v=${encodeURIComponent(version)}` : '';
  switch (s.kind) {
    case 'count':
      return `${s.n > 9 ? '/badge/9plus.svg' : `/badge/${s.n}.svg`}${v}`;
    case 'lost':
      return `/badge/lost.svg${v}`;
    default:
      return '/favicon.svg';
  }
}

/**
 * Puts a signal on the document. The icon link is replaced, not edited:
 * some browsers keep showing the first icon when only its address changes.
 * Nothing is touched when the signal is the one already shown.
 */
export function showSignal(doc: Document, s: TabSignal, version = ''): void {
  const title = tabTitle(s);
  if (doc.title !== title) doc.title = title;
  const href = tabIcon(s, version);
  const old = doc.head.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (old?.getAttribute('href') === href) return;
  const link = doc.createElement('link');
  link.rel = 'icon';
  link.type = 'image/svg+xml';
  link.setAttribute('href', href);
  if (old) old.replaceWith(link);
  else doc.head.append(link);
}

/**
 * Loads the "not live" icon once while sitboard answers, so the browser has
 * it (sitboard lets it keep the tab icons) when the server is gone or this
 * tab's key no longer works, which is exactly when it is shown.
 */
export function warmLostIcon(doc: Document, version: string): HTMLImageElement {
  const img = doc.createElement('img');
  img.decoding = 'async';
  img.src = tabIcon({ kind: 'lost', word: '' }, version);
  return img;
}
