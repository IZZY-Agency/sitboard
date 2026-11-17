import { describe, expect, it } from 'vitest';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
// @ts-expect-error plain JavaScript module without types
import { checkDist } from './check-dist.mjs';

function dist(files: Record<string, string>): string {
  const dir = mkdtempSync(join(tmpdir(), 'sitboard-dist-'));
  for (const [name, body] of Object.entries(files)) {
    const p = join(dir, name);
    mkdirSync(join(p, '..'), { recursive: true });
    writeFileSync(p, body);
  }
  return dir;
}

describe('check-dist', () => {
  it('passes on a clean build', () => {
    expect(checkDist(dist({ 'index.html': '<div id="app"></div>', 'assets/index.js': 'console.log(1)' }))).toEqual([]);
  });
  it('fails on a planted placeholder', () => {
    const problems = checkDist(dist({ 'assets/index.js': 'const n = "worker-a";' }));
    expect(problems).toEqual(['assets/index.js: contains "worker-a"']);
  });
  it('fails on the dev harness marker', () => {
    const marker = JSON.parse(readFileSync(join(process.cwd(), 'dev/marker.json'), 'utf8')).marker as string;
    expect(marker.length).toBeGreaterThan(10);
    expect(checkDist(dist({ 'assets/x.js': `const m = ${JSON.stringify(marker)};` }))).toEqual([
      `assets/x.js: contains "${marker}"`,
    ]);
  });
  it('fails on a dev-harness file name', () => {
    expect(checkDist(dist({ 'dev/index.html': '' })).length).toBeGreaterThan(0);
  });
});
