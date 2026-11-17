import { describe, expect, it } from 'vitest';
import { clean } from './text';

describe('clean', () => {
  it('drops control characters', () => {
    expect(clean('a\u0000b\u0007c\u009bd')).toBe('abcd');
  });
  it('drops bidi overrides and isolates so they cannot reorder neighbouring text', () => {
    expect(clean('abc\u202edef\u2066g\u2069\u200f')).toBe('abcdefg');
  });
  it('turns newlines and tabs into single spaces', () => {
    expect(clean('one\n\ttwo   three')).toBe('one two three');
  });
  it('keeps markup as plain characters (rendering escapes it)', () => {
    expect(clean('<img src=x onerror=alert(1)>')).toBe('<img src=x onerror=alert(1)>');
  });
  it('drops zero-width characters', () => {
    expect(clean('a\u200bb\u200cc\u200dd\u2060e\ufefff')).toBe('abcdef');
  });
  it('never splits a surrogate pair when it caps', () => {
    const out = clean('a' + '😀'.repeat(600));
    expect(Array.from(out)).toHaveLength(501);
    expect(out.slice(-2, -1)).not.toMatch(/[\ud800-\udbff]$/);
    expect(out.endsWith('😀…')).toBe(true);
  });
  it('caps a huge string', () => {
    const out = clean('x'.repeat(10_000));
    expect(out.length).toBeLessThanOrEqual(501);
    expect(out.endsWith('…')).toBe(true);
  });
});
