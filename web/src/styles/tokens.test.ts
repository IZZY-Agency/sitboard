import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

// Tests run from web/.
const SRC = join(process.cwd(), 'src');

function files(dir: string, ext: string[]): string[] {
  return readdirSync(dir).flatMap((f: string) => {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) return files(p, ext);
    return ext.some((e) => p.endsWith(e)) ? [p] : [];
  });
}

// Every stylesheet, including the <style> blocks of components.
function sheets(): Array<{ file: string; css: string }> {
  return files(SRC, ['.css', '.svelte']).map((file) => {
    const text = readFileSync(file, 'utf8');
    if (file.endsWith('.css')) return { file, css: text };
    const m = text.match(/<style[^>]*>([\s\S]*?)<\/style>/);
    return { file, css: m ? m[1] : '' };
  });
}

describe('tokens', () => {
  it('tokens define every spec value', () => {
    const css = readFileSync(join(SRC, 'styles/tokens.css'), 'utf8');
    const want: Record<string, string> = {
      '--bg': '#0a0b10',
      '--surface': '#101219',
      '--border': '#2e3148',
      '--fg': '#dddacf',
      '--fg-body': '#bcc1cb',
      '--muted': '#8b92a5',
      '--amber': '#f5c542',
      '--amber-hover': '#ffd35c',
      '--state-needs': '#f5c542',
      '--state-working': '#8fc79a',
      '--state-coordinator': '#98aabe',
      '--state-review': '#8fb8f2',
      '--state-done': '#b0a6ee',
      '--state-idle': '#8b92a5',
      '--state-failing': '#ec7c7c',
    };
    for (const [k, v] of Object.entries(want)) {
      expect(css, k).toMatch(new RegExp(`${k}:\\s*${v};`));
    }
  });

  it('no font-size below 11px in any stylesheet', () => {
    for (const { file, css } of sheets()) {
      for (const m of css.matchAll(/font-size:\s*([\d.]+)px/g)) {
        expect(Number(m[1]), file).toBeGreaterThanOrEqual(11);
      }
      expect(css, file).not.toMatch(/font-size:\s*[\d.]+(r?em|%)/);
    }
  });

  it('no border-radius except 50%', () => {
    for (const { file, css } of sheets()) {
      for (const m of css.matchAll(/border-radius:\s*([^;]+);/g)) {
        expect(['50%', '0'], file).toContain(m[1].trim());
      }
    }
  });

  it('no shadows', () => {
    for (const { file, css } of sheets()) {
      for (const m of css.matchAll(/box-shadow:\s*([^;]+);/g)) expect(m[1].trim(), file).toBe('none');
    }
  });

  it('no external URL in any stylesheet', () => {
    for (const { file, css } of sheets()) {
      expect(css, file).not.toMatch(/url\(\s*['"]?(https?:)?\/\//);
      expect(css, file).not.toMatch(/@import\s+(url\()?['"]?(https?:)?\/\//);
    }
  });

  it('the focus ring is amber and numerals are tabular', () => {
    const base = readFileSync(join(SRC, 'styles/base.css'), 'utf8');
    expect(base).toMatch(/:focus-visible\s*{[^}]*outline:\s*2px solid var\(--amber\)/);
    expect(base).toMatch(/font-variant-numeric:\s*tabular-nums/);
  });

  it('reduced motion also stops the 1px press move', () => {
    const base = readFileSync(join(SRC, 'styles/base.css'), 'utf8');
    const block = base.slice(base.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(block).toMatch(/:active,\s*:has\(> :active\)\s*{\s*transform: none !important;/);
  });

  it('visually hidden text is pinned to a corner, so it cannot stretch the page from inside a scroller', () => {
    const base = readFileSync(join(SRC, 'styles/base.css'), 'utf8');
    expect(base).toMatch(/\.visually-hidden \{[^}]*position: absolute;\s*top: 0;\s*left: 0;/);
  });

  it('every font file has its licence beside it', () => {
    const fonts = readdirSync(join(SRC, 'fonts'));
    expect(fonts).toContain('JetBrainsMono-OFL.txt');
    expect(fonts).toContain('SpaceGrotesk-OFL.txt');
  });
});
