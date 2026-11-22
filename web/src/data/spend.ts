// Spend: tokens are facts; money is Claude Code's own estimate at API list
// price, never a bill. Every money figure carries "≈" and says so on hover.
// A total whose use is only partly priced shows the priced part and says the
// rest is unpriced ("≈ $8.75 + unpriced"): it never looks complete when it is
// not, and never vanishes when most of it is known. Only when nothing is
// priced does it show tokens and "no price". Never a guessed amount.
// Codex keeps no price on the machine: its use is tokens only, shown beside
// Claude Code's money ("≈ $63 · Codex 38 k out"), never in it.
import type { Spend, SpendDay, SpendSummary, Tokens } from '../contract.gen';

/** Which token figure is shown: output tokens (the owner's choice) or all of them. */
export type TokenFigure = 'output' | 'all';
export const TOKEN_FIGURE: TokenFigure = 'output';

export const ESTIMATE = 'at API list price';
export const NOT_A_BILL = "Claude Code's own estimate, not a bill on a subscription.";
/** The one sentence a screen reader gets for every money figure. */
export const SPEND_NOTE = "Claude Code's estimate at API list price, not a bill on a subscription.";
/** The same, as the spoken tail of a figure's hidden text. */
export const SPOKEN_ESTIMATE = `${ESTIMATE}, not a bill on a subscription`;
/** Shown while sitboard is still reading older sessions: figures can still grow. */
export const PENDING_NOTE = 'Still reading older sessions: these figures can still grow.';
/** What an unpriced total is called: some of its use has no price in the table sitboard reads. */
export const UNPRICED = 'unpriced';
export const NO_PRICE_REASON = "some use has no price in the price table sitboard reads";
/** Why a Codex figure is tokens only. */
export const CODEX_NO_PRICE = 'Codex keeps no price on this machine, so its use is shown in tokens only.';
/** What a Codex card says where a Claude Code card has its estimate. */
export const TOKENS_ONLY = 'tokens only';

export function hasSpend(s: Spend): boolean {
  const t = s.tokens;
  return s.recorded || t.input + t.output + t.cacheRead + t.cacheWrite > 0;
}

export function hasTokens(t: Tokens): boolean {
  return t.input + t.output + t.cacheRead + t.cacheWrite > 0;
}

export const noTokens = (): Tokens => ({ input: 0, output: 0, cacheRead: 0, cacheWrite: 0 });

export function addTokens(a: Tokens, b: Tokens): Tokens {
  return { input: a.input + b.input, output: a.output + b.output, cacheRead: a.cacheRead + b.cacheRead, cacheWrite: a.cacheWrite + b.cacheWrite };
}

export function tokenCount(t: Tokens, figure: TokenFigure = TOKEN_FIGURE): number {
  return figure === 'output' ? t.output : t.input + t.output + t.cacheRead + t.cacheWrite;
}

const UNIT: Record<TokenFigure, string> = { output: 'out', all: 'tok' };

/** A token count in a few characters: 950, 8.2 k, 820 k, 1.2 M, 140 M, 2.3 B. */
export function fmtTokens(n: number): string {
  const step = (v: number, unit: string) => `${v < 10 ? (Math.round(v * 10) / 10).toString() : Math.round(v).toString()} ${unit}`;
  if (n < 1000) return String(Math.max(0, Math.round(n)));
  if (n < 999_500) return step(n / 1e3, 'k');
  if (n < 999_500_000) return step(n / 1e6, 'M');
  return step(n / 1e9, 'B');
}

/** US dollars: cents below $100, whole dollars from there. */
export function fmtUSD(usd: number): string {
  if (!(usd > 0)) return '$0';
  if (usd < 0.005) return '<$0.01';
  if (usd < 99.995) return `$${usd.toFixed(2)}`;
  return `$${Math.round(usd).toLocaleString('en-US')}`;
}

/** The short token figure: "1.2 M out". */
export function tokensText(s: Spend, figure: TokenFigure = TOKEN_FIGURE): string {
  return `${fmtTokens(tokenCount(s.tokens, figure))} ${UNIT[figure]}`;
}

const SPOKEN_UNIT: Record<TokenFigure, string> = { output: 'output tokens', all: 'tokens' };

/** Codex's tokens, named: "Codex 38 k out". */
export function codexText(t: Tokens, figure: TokenFigure = TOKEN_FIGURE): string {
  return `Codex ${fmtTokens(tokenCount(t, figure))} ${UNIT[figure]}`;
}

/** The same as a screen reader says it: "Codex 38 k output tokens". */
export function codexSpoken(t: Tokens, figure: TokenFigure = TOKEN_FIGURE): string {
  return `Codex ${fmtTokens(tokenCount(t, figure))} ${SPOKEN_UNIT[figure]}`;
}

function breakdown(t: Tokens): string {
  return `${fmtTokens(t.output)} output · ${fmtTokens(t.input)} input · ${fmtTokens(t.cacheRead)} cache read · ${fmtTokens(t.cacheWrite)} cache write`;
}

/** The hover text for Codex's tokens in a total. */
export function codexTitle(t: Tokens): string {
  return `Codex: ${breakdown(t)}. ${CODEX_NO_PRICE}`;
}

/** True when only part of the use has a price: USD is that part, not the whole. */
export function isPartial(s: Spend): boolean {
  return !s.priced && s.usd > 0;
}

/**
 * The money figure in two parts: "≈ $3.10" and, when some use has no price,
 * " + unpriced". Both empty when nothing is priced (show tokens instead).
 */
export function moneyParts(s: Spend): { main: string; more: string } {
  if (s.priced) return { main: `≈ ${fmtUSD(s.usd)}`, more: '' };
  if (isPartial(s)) return { main: `≈ ${fmtUSD(s.usd)}`, more: ` + ${UNPRICED}` };
  return { main: '', more: '' };
}

/** The money figure, "≈ $3.10" or "≈ $3.10 + unpriced", or "" when nothing has a price. */
export function moneyText(s: Spend): string {
  const m = moneyParts(s);
  return m.main + m.more;
}

/** The figure as a screen reader says it: "about $3.10, at API list price"; a Codex card's "84 k output tokens, tokens only". */
export function spokenText(s: Spend, figure: TokenFigure = TOKEN_FIGURE, codex = false): string {
  if (codex) return `${fmtTokens(tokenCount(s.tokens, figure))} ${SPOKEN_UNIT[figure]}, ${TOKENS_ONLY}`;
  if (s.priced) return `about ${fmtUSD(s.usd)}, ${ESTIMATE}`;
  if (isPartial(s)) return `about ${fmtUSD(s.usd)} plus unpriced use, ${ESTIMATE}`;
  return `${fmtTokens(tokenCount(s.tokens, figure))} ${figure === 'output' ? 'output tokens' : 'tokens'}, no price`;
}

/** The hover text for any spend figure; codex for a Codex card's. */
export function spendTitle(s: Spend, codex = false): string {
  const t = s.tokens;
  if (codex) return `${TOKENS_ONLY[0].toUpperCase()}${TOKENS_ONLY.slice(1)}: ${CODEX_NO_PRICE}\n${breakdown(t)}`;
  const parts = [
    s.priced
      ? `≈ ${fmtUSD(s.usd)} ${ESTIMATE}. ${NOT_A_BILL}`
      : isPartial(s)
        ? `≈ ${fmtUSD(s.usd)} ${ESTIMATE} for the use that has a price, plus use that has none: ${NO_PRICE_REASON}. ${NOT_A_BILL}`
        : `No price: ${NO_PRICE_REASON}, so only tokens are shown.`,
    breakdown(t),
  ];
  if (s.recorded) parts.push('The total Claude Code recorded when the session ended.');
  return parts.join('\n');
}

/** The machine's local date, as the server names days: "2026-11-20". */
export function localDay(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export function today(sum: SpendSummary, now: number): SpendDay | null {
  return sum.days.find((d) => d.day === localDay(now)) ?? null;
}

/** The header button's figure: today's money, or its tokens and "no price" when nothing is priced, then Codex's tokens beside it; "…" is added while older sessions are still being read. */
export function todayText(sum: SpendSummary, now: number): string {
  const d = today(sum, now);
  const parts = d
    ? [hasSpend(d.spend) ? moneyText(d.spend) || `${tokensText(d.spend)} · no price` : '', hasTokens(d.codex) ? codexText(d.codex) : '']
    : [];
  const fig = parts.filter(Boolean).join(' · ');
  if (!sum.pending) return fig;
  return fig ? `${fig} …` : '…';
}

/** Today's figure as a screen reader says it; "" when nothing was used today. */
export function todaySpoken(sum: SpendSummary, now: number): string {
  const d = today(sum, now);
  if (!d) return '';
  const claude = hasSpend(d.spend) ? spokenText(d.spend) : '';
  const codex = hasTokens(d.codex) ? codexSpoken(d.codex) : '';
  return claude && codex ? `${claude}, and ${codex}` : claude || codex;
}

/** What today's figure is, for the header button's description: Claude Code's estimate, Codex's tokens, or both. */
export function todayNote(sum: SpendSummary, now: number): string {
  const d = today(sum, now);
  const claude = !d || hasSpend(d.spend) || !hasTokens(d.codex);
  const codex = !!d && hasTokens(d.codex);
  return [claude ? SPEND_NOTE : '', codex ? CODEX_NO_PRICE : ''].filter(Boolean).join(' ');
}

/** Today's hover text, Codex's part included; "" when nothing was used today. */
export function todayTitle(sum: SpendSummary, now: number): string {
  const d = today(sum, now);
  if (!d) return '';
  return [hasSpend(d.spend) ? spendTitle(d.spend) : '', hasTokens(d.codex) ? codexTitle(d.codex) : ''].filter(Boolean).join('\n');
}

const WEEKDAY = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

/** A day's column label: "Today", "Yesterday" or the weekday. */
export function dayLabel(day: string, now: number): string {
  if (day === localDay(now)) return 'Today';
  const d = new Date(`${day}T12:00:00`);
  if (localDay(d.getTime() + 24 * 3600 * 1000) === localDay(now)) return 'Yesterday';
  return WEEKDAY[d.getDay()] ?? day;
}

/** One cell: Claude Code's spend, and Codex's tokens beside it. */
export interface SpendCell {
  spend: Spend;
  codex: Tokens;
}

export interface SpendRow {
  key: string;
  name: string;
  path: string;
  cells: (SpendCell | null)[]; // one per column day; null when no use that day
  total: SpendCell;
}

export interface SpendTable {
  days: string[];
  rows: SpendRow[];
  all: SpendRow | null; // the sum over every project, when there is more than one
}

const zero = (): Spend => ({ tokens: noTokens(), usd: 0, priced: true, recorded: false });
const zeroCell = (): SpendCell => ({ spend: zero(), codex: noTokens() });
const addCell = (a: SpendCell, b: SpendCell): SpendCell => ({ spend: addSpend(a.spend, b.spend), codex: addTokens(a.codex, b.codex) });

/** a + b: USD adds the priced parts; priced only when both are (the core's addSpend). */
export function addSpend(a: Spend, b: Spend): Spend {
  const priced = a.priced && b.priced;
  return {
    tokens: {
      input: a.tokens.input + b.tokens.input,
      output: a.tokens.output + b.tokens.output,
      cacheRead: a.tokens.cacheRead + b.tokens.cacheRead,
      cacheWrite: a.tokens.cacheWrite + b.tokens.cacheWrite,
    },
    usd: a.usd + b.usd,
    priced,
    recorded: false,
  };
}

/** Project × day, with each project's 7-day total, largest first. */
export function spendTable(sum: SpendSummary): SpendTable {
  const days = sum.days.map((d) => d.day);
  const rows = new Map<string, SpendRow>();
  sum.days.forEach((d, i) => {
    for (const p of d.projects) {
      const key = p.projectId || '-';
      let r = rows.get(key);
      if (!r) {
        r = { key, name: p.name || 'no folder', path: p.path, cells: days.map(() => null), total: zeroCell() };
        rows.set(key, r);
      }
      const c = { spend: p.spend, codex: p.codex };
      r.cells[i] = c;
      r.total = addCell(r.total, c);
    }
  });
  // Biggest priced part first; with equal money, the one with more tokens.
  const size = (r: SpendRow) => r.total.spend.usd;
  const toks = (r: SpendRow) => tokenCount(r.total.spend.tokens) + tokenCount(r.total.codex);
  const list = [...rows.values()].sort((a, b) => size(b) - size(a) || toks(b) - toks(a) || a.name.localeCompare(b.name));
  let all: SpendRow | null = null;
  if (list.length > 1) {
    const cells = sum.days.map((d) => ({ spend: d.spend, codex: d.codex }));
    all = { key: 'all', name: 'All', path: '', cells, total: cells.reduce(addCell, zeroCell()) };
  }
  return { days, rows: list, all };
}

/** The Claude Code part of a cell: the estimate ("≈ $1.00", "≈ $1.00 + unpriced"), else the tokens; "" with no use. */
export function claudeCellText(s: Spend): string {
  return hasSpend(s) ? moneyText(s) || tokensText(s) : '';
}

/** A table cell: Claude Code's part, then Codex's tokens ("≈ $1.00 · Codex 4 k out"); "—" with no use. */
export function cellText(c: SpendCell | null): string {
  if (!c) return '—';
  return [claudeCellText(c.spend), hasTokens(c.codex) ? codexText(c.codex) : ''].filter(Boolean).join(' · ') || '—';
}

/** A cell's hover text. */
export function cellTitle(c: SpendCell): string {
  return [hasSpend(c.spend) ? spendTitle(c.spend) : '', hasTokens(c.codex) ? codexTitle(c.codex) : ''].filter(Boolean).join('\n');
}
