import { describe, expect, it } from 'vitest';
import type { Spend, SpendSummary, Tokens } from '../contract.gen';
import { addSpend, cellText, codexText, todaySpoken, dayLabel, fmtTokens, fmtUSD, hasSpend, localDay, moneyText, spendTable, spendTitle, spokenText, todayText, tokenCount, tokensText } from './spend';

const sp = (output: number, usd: number, priced = true, recorded = false): Spend => ({
  tokens: { input: 10, output, cacheRead: 1000, cacheWrite: 100 },
  usd,
  priced,
  recorded,
});
const NOW = new Date(2026, 10, 20, 15, 0).getTime(); // local time
const none: Tokens = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
const cx = (output: number): Tokens => ({ input: 5, output, cacheRead: 500, cacheWrite: 0 });
const day = (d: number) => localDay(new Date(2026, 10, 20 + d, 12).getTime());

describe('spend figures', () => {
  it('formats tokens in a few characters', () => {
    expect([0, 950, 8_240, 820_000, 999_499, 1_234_567, 140_000_000, 2_300_000_000].map(fmtTokens)).toEqual([
      '0',
      '950',
      '8.2 k',
      '820 k',
      '999 k',
      '1.2 M',
      '140 M',
      '2.3 B',
    ]);
  });

  it('formats dollars with cents below $100', () => {
    expect([0, 0.001, 3.1, 41.2, 99.99, 342.4, 1234].map(fmtUSD)).toEqual(['$0', '<$0.01', '$3.10', '$41.20', '$99.99', '$342', '$1,234']);
  });

  it('shows output tokens by default, all tokens on the switch', () => {
    expect(tokensText(sp(1_200_000, 3))).toBe('1.2 M out');
    expect(tokenCount(sp(100, 0).tokens, 'all')).toBe(1210);
    expect(tokensText(sp(100, 0), 'all')).toBe('1.2 k tok');
  });

  it('marks money as an estimate and never shows an amount without a price', () => {
    expect(moneyText(sp(1, 3.1))).toBe('≈ $3.10');
    expect(moneyText(sp(1, 3.1, false))).toBe('≈ $3.10 + unpriced');
    expect(moneyText(sp(1, 0, false))).toBe('');
    expect(spendTitle(sp(1, 3.1))).toContain('at API list price');
    expect(spendTitle(sp(1, 3.1))).toContain('not a bill');
    expect(spendTitle(sp(1, 0, false))).toContain('No price');
    expect(spendTitle(sp(1, 0, false))).not.toContain('$');
    expect(spendTitle(sp(1, 0, false))).toContain('price table sitboard reads');
    expect(spendTitle(sp(1, 3.1, true, true))).toContain('recorded when the session ended');
    expect(hasSpend({ ...sp(0, 0), tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } })).toBe(false);
  });

  it('shows the priced part of a total that is only partly priced, and says so', () => {
    const part = sp(1, 8.75, false);
    expect(moneyText(part)).toBe('≈ $8.75 + unpriced');
    expect(cellText({ spend: part, codex: none })).toBe('≈ $8.75 + unpriced');
    const t = spendTitle(part);
    expect(t).toContain('≈ $8.75 at API list price for the use that has a price');
    expect(t).toContain('has none');
    expect(t).toContain('not a bill');
    expect(t).not.toContain('No price');
  });

  it('says a figure the way a screen reader should', () => {
    expect(spokenText(sp(1, 63))).toBe('about $63.00, at API list price');
    expect(spokenText(sp(1, 8.75, false))).toBe('about $8.75 plus unpriced use, at API list price');
    expect(spokenText(sp(1_200_000, 0, false))).toBe('1.2 M output tokens, no price');
  });

  it('adds the priced parts and is priced only when everything is (like the core)', () => {
    const a = addSpend(sp(1, 2), sp(1, 3, false));
    expect(a.usd).toBe(5);
    expect(a.priced).toBe(false);
    expect(addSpend(sp(1, 2), sp(1, 3)).priced).toBe(true);
    expect(addSpend(sp(1, 0, false), sp(1, 0, false)).usd).toBe(0);
  });

  it('adds "…" to the header figure while older sessions are still being read', () => {
    const sum: SpendSummary = { days: [{ day: day(0), spend: sp(500, 63), codex: none, projects: [] }], pending: true };
    expect(todayText(sum, NOW)).toBe('≈ $63.00 …');
    expect(todayText({ days: [], pending: true }, NOW)).toBe('…');
    expect(todayText({ days: [], pending: false }, NOW)).toBe('');
  });

  it("names today's figure for the header, money first", () => {
    const sum: SpendSummary = { days: [{ day: day(0), spend: sp(500, 63), codex: none, projects: [] }], pending: false };
    expect(todayText(sum, NOW)).toBe('≈ $63.00');
    sum.days[0].spend.priced = false;
    expect(todayText(sum, NOW)).toBe('≈ $63.00 + unpriced');
    sum.days[0].spend.usd = 0;
    expect(todayText(sum, NOW)).toBe('500 out · no price');
    expect(todayText({ days: [{ day: day(-1), spend: sp(1, 1), codex: none, projects: [] }], pending: false }, NOW)).toBe('');
  });

  it('labels days', () => {
    expect(dayLabel(day(0), NOW)).toBe('Today');
    expect(dayLabel(day(-1), NOW)).toBe('Yesterday');
    expect(dayLabel(day(-3), NOW)).toBe('Tue');
  });
});

describe('spendTable', () => {
  const p = (id: string, name: string, s: Spend, c: Tokens = none) => ({ projectId: id, name, path: `~/projects/${name}`, spend: s, codex: c });
  const sum: SpendSummary = {
    days: [
      { day: day(-1), spend: sp(10, 1), codex: none, projects: [p('a', 'alpha', sp(10, 1))] },
      { day: day(0), spend: sp(27, 2, false), codex: none, projects: [p('a', 'alpha', sp(20, 2)), p('b', 'beta', sp(7, 0, false))] },
    ],
    pending: false,
  };

  it('puts projects in rows, days in columns, priced totals first', () => {
    const t = spendTable(sum);
    expect(t.days).toEqual([day(-1), day(0)]);
    expect(t.rows.map((r) => r.name)).toEqual(['alpha', 'beta']);
    expect(t.rows[0].cells.map(cellText)).toEqual(['≈ $1.00', '≈ $2.00']);
    expect(t.rows[0].total.spend.usd).toBe(3);
    expect(t.rows[1].cells.map(cellText)).toEqual(['—', '7 out']);
    expect(t.rows[1].total.spend.priced).toBe(false);
    expect(t.all?.cells.map(cellText)).toEqual(['≈ $1.00', '≈ $2.00 + unpriced']);
    expect(t.all?.total.spend.priced).toBe(false);
    expect(t.all?.total.spend.usd).toBe(3);
    expect(cellText(t.all?.total ?? null)).toBe('≈ $3.00 + unpriced');
  });

  it('carries Codex tokens beside the money: in cells, totals and the All row', () => {
    const zero: Spend = { tokens: none, usd: 0, priced: true, recorded: false };
    const t = spendTable({
      days: [
        { day: day(-1), spend: sp(10, 1), codex: cx(4_000), projects: [p('a', 'alpha', sp(10, 1), cx(4_000))] },
        { day: day(0), spend: sp(27, 2, false), codex: cx(6_000), projects: [p('a', 'alpha', sp(20, 2)), p('b', 'beta', sp(7, 0, false)), p('c', 'gamma', zero, cx(6_000))] },
      ],
      pending: false,
    });
    // With no money, Codex's tokens count in the order too: gamma's 6 k before beta's 7.
    expect(t.rows.map((r) => r.name)).toEqual(['alpha', 'gamma', 'beta']);
    expect(t.rows[0].cells.map(cellText)).toEqual(['≈ $1.00 · Codex 4 k out', '≈ $2.00']);
    expect(cellText(t.rows[0].total)).toBe('≈ $3.00 · Codex 4 k out');
    expect(t.rows[1].cells.map(cellText)).toEqual(['—', 'Codex 6 k out']);
    expect(t.rows[1].total.spend.priced).toBe(true);
    expect(cellText(t.all?.total ?? null)).toBe('≈ $3.00 + unpriced · Codex 10 k out');
    expect(codexText(cx(1_200_000))).toBe('Codex 1.2 M out');
  });

  it("puts Codex beside today's money in the header, in words too", () => {
    const sum: SpendSummary = { days: [{ day: day(0), spend: sp(500, 63), codex: cx(38_000), projects: [] }], pending: false };
    expect(todayText(sum, NOW)).toBe('≈ $63.00 · Codex 38 k out');
    expect(todaySpoken(sum, NOW)).toBe('about $63.00, at API list price, and Codex 38 k output tokens');
    sum.days[0].spend = { tokens: none, usd: 0, priced: true, recorded: false };
    expect(todayText(sum, NOW)).toBe('Codex 38 k out');
    expect(todaySpoken(sum, NOW)).toBe('Codex 38 k output tokens');
  });

  it('has no All row for one project, and nothing for no use', () => {
    expect(spendTable({ days: [sum.days[0]], pending: false }).all).toBeNull();
    expect(spendTable({ days: [], pending: false }).rows).toEqual([]);
  });
});
