import { describe, expect, it } from 'vitest';
import { exact } from './text';

describe('exact', () => {
  it('keeps a plain name as it is, repeated spaces and all, with no cut', () => {
    expect(exact('~/projects/harbor/.worktrees/fix-x')).toBe('~/projects/harbor/.worktrees/fix-x');
    expect(exact('fix  x')).toBe('fix  x');
    const long = 'a/'.repeat(600);
    expect(exact(long)).toBe(long);
    expect(exact('naïve-émigré/日本')).toBe('naïve-émigré/日本');
    expect(exact('')).toBe('');
  });
  it('writes what would not show as an escape, so two names never look the same', () => {
    expect(exact('a\tb\nc\u0000d\u007f\u0085')).toBe('a\\x09b\\x0ac\\x00d\\x7f\\x85');
    expect(exact('fix\u200bx')).toBe('fix\\u{200b}x');
    expect(exact('\ufeffmain')).toBe('\\u{feff}main');
    expect(exact('a\u202eb\u2066c')).toBe('a\\u{202e}b\\u{2066}c');
    expect(exact('fix\u00a0x')).toBe('fix\\xa0x');
    expect(exact('fix\u3000x\u2028y')).toBe('fix\\u{3000}x\\u{2028}y');
    expect(exact('soft\u00adhyphen')).toBe('soft\\xadhyphen');
    expect(exact('x\u3164y')).toBe('x\\u{3164}y');
    expect(exact('lone\ud800')).toBe('lone\\u{d800}');
    expect(exact('fix\u200bx')).not.toBe(exact('fixx'));
  });
  it('escapes the backslash, so an escape in the name is not read as one', () => {
    expect(exact('a\\x09b')).toBe('a\\\\x09b');
    expect(exact('a\\x09b')).not.toBe(exact('a\tb'));
  });
  it('writes a space at either end as an escape, inner ones as they are', () => {
    expect(exact(' fix x ')).toBe('\\x20fix x\\x20');
    expect(exact('  ')).toBe('\\x20\\x20');
    expect(exact(' ')).not.toBe(exact(''));
  });
});
