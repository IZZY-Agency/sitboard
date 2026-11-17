// No invisible or direction-changing character may sit in the source: they can
// make code read differently from how it runs. Escapes (\u202e) are fine.
import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

const ROOT = process.cwd(); // web/
const INVISIBLE = /[\p{Cf}\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029]/u;

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((f: string) => {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) return files(p);
    return /\.(ts|svelte|mjs|css|html|json)$/.test(p) ? [p] : [];
  });
}

describe('source text', () => {
  it('has no invisible or bidi characters', () => {
    for (const f of [...files(join(ROOT, 'src')), ...files(join(ROOT, 'dev')), ...files(join(ROOT, 'scripts'))]) {
      expect(INVISIBLE.test(readFileSync(f, 'utf8')), f).toBe(false);
    }
  });
});
