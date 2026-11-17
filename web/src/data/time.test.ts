import { describe, expect, it } from 'vitest';
import { duration, since, clock } from './time';

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
