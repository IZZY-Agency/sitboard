import { describe, expect, it } from 'vitest';
import { duration, since, clock, when } from './time';

const S = 1000;
const M = 60 * S;
const H = 60 * M;
const D = 24 * H;

describe('duration', () => {
  it('59s → "now", 13m, 11h 57m, 3d 17h', () => {
    expect(duration(59 * S)).toBe('now');
    expect(duration(13 * M + 20 * S)).toBe('13m');
    expect(duration(11 * H + 57 * M)).toBe('11h 57m');
    expect(duration(3 * D + 17 * H + 5 * M)).toBe('3d 17h');
  });
  it('is never negative', () => {
    expect(duration(-5 * M)).toBe('now');
    expect(duration(Number.NaN)).toBe('now');
  });
});

describe('since', () => {
  const now = Date.parse('2026-11-17T10:00:00Z');
  it('measures from an RFC 3339 time', () => {
    expect(since('2026-11-17T09:47:00Z', now)).toBe('13m');
  });
  it('a time in the future reads "now", never negative', () => {
    expect(since('2026-11-17T11:00:00Z', now)).toBe('now');
  });
  it('an absent or broken time is unknown: empty string', () => {
    expect(since(undefined, now)).toBe('');
    expect(since('not a time', now)).toBe('');
  });
});

describe('clock', () => {
  it('formats a time as HH:MM, 24-hour', () => {
    const d = new Date(2026, 10, 17, 9, 5);
    expect(clock(d.toISOString())).toBe('09:05');
  });
});

describe('when', () => {
  // Local times, so the test reads the same in every time zone.
  const now = new Date(2026, 10, 19, 15, 0).getTime(); // Thu 19 Nov 15:00
  const at = (y: number, mo: number, d: number, h: number, mi: number) => new Date(y, mo, d, h, mi).toISOString();
  it('today: the clock only', () => {
    expect(when(at(2026, 10, 19, 9, 5), now)).toBe('09:05');
  });
  it('this week: the day and the clock', () => {
    expect(when(at(2026, 10, 18, 23, 59), now)).toBe('Wed 23:59');
    expect(when(at(2026, 10, 13, 0, 1), now)).toBe('Fri 00:01');
  });
  it('a week or more back: the date', () => {
    expect(when(at(2026, 10, 12, 9, 0), now)).toBe('12 Nov 09:00');
  });
  it('unreadable: nothing', () => {
    expect(when('soon', now)).toBe('');
  });
});
