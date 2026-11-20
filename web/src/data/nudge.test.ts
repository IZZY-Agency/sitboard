import { beforeEach, describe, expect, it } from 'vitest';
import { card } from '../test/fixtures';
import { applySeen } from './derive';
import { iconVersion, needsYouCount, showSignal, tabIcon, tabSignal, tabTitle, warmLostIcon } from './nudge';

const blocked = () => card({ column: 'needs_you', attention: ['blocked'], seen: true, agent: { status: 'blocked' } });
const unseen = () => card({ column: 'needs_you', attention: ['unseen'], seen: false, agent: { status: 'finished' } });

describe('needsYouCount', () => {
  it('counts the cards in Needs you, whatever their project', () => {
    expect(needsYouCount([blocked(), unseen(), card({ projectId: 'p_000000000002', column: 'needs_you', attention: ['blocked'] })])).toBe(3);
  });

  it('does not count failing checks or stale cards outside Needs you, or other columns', () => {
    const cards = [
      card({ column: 'in_review', attention: ['failing_checks'] }),
      card({ column: 'working', attention: ['stale'] }),
      card({ column: 'idle', agent: { status: 'unknown' } }),
      card({ column: 'with_coordinator' }),
    ];
    expect(needsYouCount(cards)).toBe(0);
  });

  it('drops a card the owner marked seen here, until its next turn', () => {
    const u = unseen();
    const cards = [u, blocked()];
    expect(needsYouCount(applySeen(cards, new Map([[u.id, u.agent.statusSince ?? '']])))).toBe(1);
    expect(needsYouCount(applySeen(cards, new Map([[u.id, 'an older turn']])))).toBe(2);
  });
});

describe('tabSignal', () => {
  it('is plain before the first snapshot and with nobody waiting', () => {
    expect(tabSignal('connecting', null)).toEqual({ kind: 'plain' });
    expect(tabSignal('live', null)).toEqual({ kind: 'plain' });
    expect(tabSignal('live', [card()])).toEqual({ kind: 'plain' });
  });

  it('counts while live', () => {
    expect(tabSignal('live', [blocked(), unseen(), card()])).toEqual({ kind: 'count', n: 2 });
  });

  it('gives no number while not live, in the header’s words: the board cannot say', () => {
    const cards = [blocked()];
    expect(tabSignal('reconnecting', cards)).toEqual({ kind: 'lost', word: 'reconnecting' });
    expect(tabSignal('unreachable', null)).toEqual({ kind: 'lost', word: 'not connected' });
    expect(tabSignal('unauthorized', cards)).toEqual({ kind: 'lost', word: 'stopped' });
    expect(tabSignal('outdated', cards)).toEqual({ kind: 'lost', word: 'stopped' });
  });
});

describe('tabTitle and tabIcon', () => {
  it('puts the number first', () => {
    expect(tabTitle({ kind: 'count', n: 3 })).toBe('(3) sitboard');
    expect(tabTitle({ kind: 'count', n: 12 })).toBe('(12) sitboard');
    expect(tabTitle({ kind: 'plain' })).toBe('sitboard');
    expect(tabTitle({ kind: 'lost', word: 'reconnecting' })).toBe('sitboard · reconnecting');
  });

  it('picks a badge per count, 9+ above nine, "?" when not live, the plain mark otherwise', () => {
    expect([1, 5, 9].map((n) => tabIcon({ kind: 'count', n }))).toEqual(['/badge/1.svg', '/badge/5.svg', '/badge/9.svg']);
    expect(tabIcon({ kind: 'count', n: 10 })).toBe('/badge/9plus.svg');
    expect(tabIcon({ kind: 'count', n: 140 })).toBe('/badge/9plus.svg');
    expect(tabIcon({ kind: 'lost', word: 'stopped' })).toBe('/badge/lost.svg');
    expect(tabIcon({ kind: 'plain' })).toBe('/favicon.svg');
  });
});

describe('the badges’ build', () => {
  it('is the version and commit, empty before any snapshot', () => {
    expect(iconVersion({ version: '0.3.0', commit: 'abc1234', os: 'linux', arch: 'amd64' })).toBe('0.3.0+abc1234');
    expect(iconVersion(null)).toBe('');
  });

  it('goes on every badge address, encoded, never on the plain mark', () => {
    expect(tabIcon({ kind: 'count', n: 3 }, '0.3.0+a b')).toBe('/badge/3.svg?v=0.3.0%2Ba%20b');
    expect(tabIcon({ kind: 'count', n: 30 }, 'x')).toBe('/badge/9plus.svg?v=x');
    expect(tabIcon({ kind: 'lost', word: '' }, 'x')).toBe('/badge/lost.svg?v=x');
    expect(tabIcon({ kind: 'plain' }, 'x')).toBe('/favicon.svg');
    expect(tabIcon({ kind: 'count', n: 3 })).toBe('/badge/3.svg');
  });
});

describe('showSignal', () => {
  let doc: Document;
  beforeEach(() => {
    doc = document.implementation.createHTMLDocument('sitboard');
    doc.head.innerHTML = '<link rel="icon" type="image/svg+xml" href="/favicon.svg" />';
  });
  const icons = () => [...doc.head.querySelectorAll('link[rel="icon"]')];

  it('sets the title and replaces the one icon link', () => {
    const first = icons()[0];
    showSignal(doc, { kind: 'count', n: 2 });
    expect(doc.title).toBe('(2) sitboard');
    expect(icons()).toHaveLength(1);
    expect(icons()[0]).not.toBe(first);
    expect(icons()[0].getAttribute('href')).toBe('/badge/2.svg');
    expect(icons()[0].getAttribute('type')).toBe('image/svg+xml');
  });

  it('leaves the link alone when the icon does not change', () => {
    showSignal(doc, { kind: 'count', n: 12 });
    const link = icons()[0];
    showSignal(doc, { kind: 'count', n: 15 });
    expect(doc.title).toBe('(15) sitboard');
    expect(icons()[0]).toBe(link);
  });

  it('adds a link when the page has none, and goes back to the plain mark', () => {
    doc.head.innerHTML = '';
    showSignal(doc, { kind: 'lost', word: 'reconnecting' });
    expect(icons().map((l) => l.getAttribute('href'))).toEqual(['/badge/lost.svg']);
    showSignal(doc, { kind: 'plain' });
    expect(doc.title).toBe('sitboard');
    expect(icons().map((l) => l.getAttribute('href'))).toEqual(['/favicon.svg']);
  });
});

describe('warmLostIcon', () => {
  it('asks for the "not live" icon, without putting anything on the page', () => {
    const doc = document.implementation.createHTMLDocument('sitboard');
    const img = warmLostIcon(doc, '0.3.0+abc1234');
    expect(img.getAttribute('src')).toBe('/badge/lost.svg?v=0.3.0%2Babc1234');
    expect(img.isConnected).toBe(false);
  });
});
