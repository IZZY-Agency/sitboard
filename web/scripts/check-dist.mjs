#!/usr/bin/env node
// Runs after `npm run build`: fails when the embedded build contains anything
// from the dev harness or the contract's placeholder data. The product only
// ever shows real data.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

// The dev harness's own marker: anything from the harness that leaks carries it.
const WEB = dirname(dirname(fileURLToPath(import.meta.url)));
const MARKER = JSON.parse(readFileSync(join(WEB, 'dev/marker.json'), 'utf8')).marker;

export const FORBIDDEN = [
  MARKER,
  'coordinator-a',
  'worker-a',
  'solo-a',
  'example-org',
  '/home/dev/',
  'FAKE DATA',
  'dev/harness',
  'dev/main',
];

function walk(dir) {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

/** Returns one problem per offending file name or file content, empty when clean. */
export function checkDist(dir) {
  const problems = [];
  for (const file of walk(dir)) {
    const rel = relative(dir, file);
    if (/(^|\/)dev(\/|$)|harness/i.test(rel)) problems.push(`${rel}: dev-harness file in the build`);
    const text = readFileSync(file).toString('latin1');
    for (const word of FORBIDDEN) {
      if (text.includes(word)) problems.push(`${rel}: contains "${word}"`);
    }
  }
  return problems;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const dir = process.argv[2] ?? join(WEB, '../internal/server/dist');
  const problems = checkDist(dir);
  if (problems.length) {
    console.error('check-dist: the build contains dev or placeholder material:');
    for (const p of problems) console.error('  ' + p);
    process.exit(1);
  }
  console.log('check-dist: clean');
}
