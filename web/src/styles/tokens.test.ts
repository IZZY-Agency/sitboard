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

// WCAG contrast and CIEDE2000, for the model family hues below.
const rgb = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const lin = (c: number) => ((c /= 255) <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const lum = (h: string) => {
  const [r, g, b] = rgb(h).map(lin);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const contrast = (a: string, b: string) => {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
};
function lab(h: string): [number, number, number] {
  const [r, g, b] = rgb(h).map(lin);
  const f = (t: number) => (t > 0.008856 ? Math.cbrt(t) : 7.787 * t + 16 / 116);
  const x = f((r * 0.4124 + g * 0.3576 + b * 0.1805) / 0.95047);
  const y = f(r * 0.2126 + g * 0.7152 + b * 0.0722);
  const z = f((r * 0.0193 + g * 0.1192 + b * 0.9505) / 1.08883);
  return [116 * y - 16, 500 * (x - y), 200 * (y - z)];
}
// Red-green colour blindness, full strength (Machado, Oliveira and Fernandes 2009), on linear RGB.
const CVD: Record<string, number[][]> = {
  deutan: [
    [0.367322, 0.860646, -0.227968],
    [0.280085, 0.672501, 0.047413],
    [-0.01182, 0.04294, 0.968881],
  ],
  protan: [
    [0.152286, 1.052583, -0.204868],
    [0.114503, 0.786281, 0.099216],
    [-0.003882, -0.048116, 1.051998],
  ],
};
const unlin = (v: number) => {
  const c = Math.min(1, Math.max(0, v));
  return Math.round(255 * (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055));
};
function seenAs(h: string, m: number[][]): string {
  const l = rgb(h).map(lin);
  return '#' + m.map((r) => unlin(r[0] * l[0] + r[1] * l[1] + r[2] * l[2]).toString(16).padStart(2, '0')).join('');
}
function deltaE(p: string, q: string): number {
  const d = Math.PI / 180;
  const [l1, a1, b1] = lab(p);
  const [l2, a2, b2] = lab(q);
  const cBar = (Math.hypot(a1, b1) + Math.hypot(a2, b2)) / 2;
  const g = 0.5 * (1 - Math.sqrt(cBar ** 7 / (cBar ** 7 + 25 ** 7)));
  const [a1p, a2p] = [a1 * (1 + g), a2 * (1 + g)];
  const [c1, c2] = [Math.hypot(a1p, b1), Math.hypot(a2p, b2)];
  const hue = (x: number, y: number) => ((Math.atan2(y, x) / d) + 360) % 360;
  const [h1, h2] = [hue(a1p, b1), hue(a2p, b2)];
  let dh = h2 - h1;
  if (Math.abs(dh) > 180) dh -= 360 * Math.sign(dh);
  const dH = 2 * Math.sqrt(c1 * c2) * Math.sin((dh / 2) * d);
  const lBar = (l1 + l2) / 2;
  const cpBar = (c1 + c2) / 2;
  let hBar = (h1 + h2) / 2;
  if (Math.abs(h1 - h2) > 180) hBar += 180;
  const t = 1 - 0.17 * Math.cos((hBar - 30) * d) + 0.24 * Math.cos(2 * hBar * d) + 0.32 * Math.cos((3 * hBar + 6) * d) - 0.2 * Math.cos((4 * hBar - 63) * d);
  const sl = 1 + (0.015 * (lBar - 50) ** 2) / Math.sqrt(20 + (lBar - 50) ** 2);
  const sc = 1 + 0.045 * cpBar;
  const sh = 1 + 0.015 * cpBar * t;
  const rt = -2 * Math.sqrt(cpBar ** 7 / (cpBar ** 7 + 25 ** 7)) * Math.sin(60 * Math.exp(-(((hBar - 275) / 25) ** 2)) * d);
  const [x, y, z] = [(l2 - l1) / sl, (c2 - c1) / sc, dH / sh];
  return Math.sqrt(x * x + y * y + z * z + rt * y * z);
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

  it('each model family has a hue and a half-strength line, apart from every text and state colour and readable on the page and a card', () => {
    const css = readFileSync(join(SRC, 'styles/tokens.css'), 'utf8');
    const token = (k: string) => css.match(new RegExp(`${k}:\\s*(#[0-9a-f]{6});`))?.[1] ?? '';
    // Every solid colour token but the grounds and the families themselves: text, state, accent and lines.
    const taken = [...css.matchAll(/(--[a-z-]+):\s*#[0-9a-f]{6};/g)]
      .map((m) => m[1])
      .filter((k) => !k.startsWith('--model-') && k !== '--bg' && k !== '--surface');
    expect(taken).toEqual(expect.arrayContaining(['--fg', '--fg-body', '--muted', '--amber', '--amber-hover', '--state-working', '--state-failing']));
    const base = readFileSync(join(SRC, 'styles/base.css'), 'utf8');
    const hues: string[] = [];
    for (const f of ['fable', 'opus', 'sonnet', 'haiku']) {
      const hue = token(`--model-${f}`);
      expect(hue, f).toMatch(/^#[0-9a-f]{6}$/);
      expect(css, f).toMatch(new RegExp(`--model-${f}-line:\\s*rgb\\(${rgb(hue).join(' ')} / 50%\\);`));
      expect(base, f).toMatch(new RegExp(`\\.model-${f} \\{\\s*--model: var\\(--model-${f}\\);\\s*--model-line: var\\(--model-${f}-line\\);`));
      for (const k of taken) expect(deltaE(hue, token(k)), `${f} vs ${k}`).toBeGreaterThanOrEqual(17);
      // The name is always written; this only keeps a family from reading as needs-you to red-green colour-blind eyes.
      for (const [kind, m] of Object.entries(CVD)) {
        for (const k of ['--amber', '--amber-hover']) {
          expect(deltaE(seenAs(hue, m), seenAs(token(k), m)), `${f} vs ${k}, ${kind}`).toBeGreaterThanOrEqual(9);
        }
      }
      for (const k of ['--bg', '--surface']) expect(contrast(hue, token(k)), `${f} on ${k}`).toBeGreaterThanOrEqual(8);
      for (const h of hues) expect(deltaE(hue, h), `${f} vs ${h}`).toBeGreaterThanOrEqual(25);
      hues.push(hue);
    }
  });

  it('a chip with no model is dashed and muted, and muted text reads on a card and the page', () => {
    const css = readFileSync(join(SRC, 'styles/tokens.css'), 'utf8');
    const token = (k: string) => css.match(new RegExp(`${k}:\\s*(#[0-9a-f]{6});`))?.[1] ?? '';
    const card = readFileSync(join(SRC, 'components/Card.svelte'), 'utf8');
    expect(card).toMatch(/\.tag\.unknown \{\s*border-style: dashed;\s*color: var\(--muted\);/);
    for (const k of ['--bg', '--surface']) expect(contrast(token('--muted'), token(k)), k).toBeGreaterThanOrEqual(6);
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
