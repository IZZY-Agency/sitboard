import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from '../App.svelte';
import Card from './Card.svelte';
import Teams from '../views/Teams.svelte';
import { indexOf } from '../data/derive';
import { localDay } from '../data/spend';
import { card, golden, project, snapshot } from '../test/fixtures';
import { feedOf, recordActions, render, settle, text } from '../test/render';
import type { Spend } from '../contract.gen';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
  vi.useRealTimers();
});
const noop = () => {};
const NOW = Date.parse('2026-11-17T10:00:00Z');
const q = (sel: string) => r!.target.querySelector(sel);
const qa = (sel: string) => [...r!.target.querySelectorAll(sel)];
const sp = (output: number, usd: number, priced = true): Spend => ({ tokens: { input: 1, output, cacheRead: 10, cacheWrite: 1 }, usd, priced, recorded: false });

function cardOf(spend: Spend, tool: 'claude' | 'codex' = 'claude') {
  const c = card({ spend, agent: { tool } });
  const s = snapshot([c], [project(c.projectId, 'one')]);
  r = render(Card, { card: c, ix: indexOf(s), now: NOW, agtermPresent: true, onjump: noop, onseen: noop });
}

describe('spend on a card', () => {
  it('shows output tokens and the estimate, explained on hover', () => {
    cardOf(sp(84_000, 6.25));
    expect(text(q('.spend'))).toBe('Used 84 k out · ≈ $6.25 at API list price, not a bill on a subscription');
    expect(q('.spend')?.getAttribute('title')).toContain('not a bill on a subscription');
  });

  it('shows tokens only when the model has no price', () => {
    cardOf(sp(7_000, 0, false));
    expect(text(q('.spend'))).toBe('Used 7 k out · no price');
    expect(text(q('.spend'))).not.toContain('$');
  });

  it('shows the priced part of a partly priced total and says the rest is unpriced', () => {
    cardOf(sp(84_000, 6.25, false));
    expect(text(q('.spend'))).toBe('Used 84 k out · ≈ $6.25 + unpriced at API list price, not a bill on a subscription');
    expect(q('.spend .more')?.textContent).toBe('+ unpriced');
    expect(q('.spend')?.getAttribute('title')).toContain('for the use that has a price');
  });

  it('shows a Codex card in tokens only, and says Codex keeps no price', () => {
    cardOf(sp(84_000, 0, false), 'codex');
    expect(text(q('.spend'))).toBe('Used 84 k out · tokens only');
    expect(q('.spend')?.getAttribute('title')).toContain('Codex keeps no price on this machine');
    expect(q('.spend')?.getAttribute('title')).not.toContain('price table');
  });

  it('shows nothing when nothing was recorded', () => {
    cardOf({ tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, usd: 0, priced: false, recorded: false });
    expect(q('.spend')).toBeNull();
  });
});

describe('spend in Teams', () => {
  it('has Out and ≈ $ columns per agent', () => {
    const s = golden();
    r = render(Teams, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
    expect(qa('.thead')[0].textContent).toContain('Out≈ $For');
    const row = qa('div.trow').find((b) => b.getAttribute('data-card') === s.cards[0].id)!;
    const nums = [...row.querySelectorAll('.num')].map(text);
    expect(nums).toEqual([', output tokens 84 k', '≈ 6.25, about $6.25, at API list price, not a bill on a subscription']);
    // Raised above the row's jump area, so their tooltips are reachable.
    expect(row.querySelectorAll('.num.tip')).toHaveLength(2);
    // A keyboard on the jump button hears the spend too.
    const ids = row.querySelector('button.jump')!.getAttribute('aria-describedby')!.split(' ');
    expect(ids.map((id) => text(r!.target.ownerDocument.getElementById(id)!))).toEqual(nums);
  });

  it('puts ≈ in the money cell, and "+" with the reason when only part is priced', () => {
    const s = golden();
    s.cards[0].spend = sp(84_000, 6.25, false);
    s.cards[1].spend = sp(9_000, 0, false);
    r = render(Teams, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
    const money = (id: string) => [...qa('div.trow').find((b) => b.getAttribute('data-card') === id)!.querySelectorAll('.num')].map(text)[1];
    const cell = (id: string) => qa('div.trow').find((b) => b.getAttribute('data-card') === id)!.querySelectorAll('.num')[1];
    // Seen: "≈ 6.25" and "+ unpriced" below it; heard once, in words.
    expect(text(cell(s.cards[0].id).querySelector('[aria-hidden="true"]')!)).toBe('≈ 6.25+ unpriced');
    expect(text(cell(s.cards[0].id).querySelector('.more')!)).toBe('+ unpriced');
    expect(text(cell(s.cards[0].id).querySelector('.visually-hidden')!)).toBe(', about $6.25 plus unpriced use, at API list price, not a bill on a subscription');
    expect(money(s.cards[1].id)).toBe('no price, no price');
    expect(text(cell(s.cards[1].id).querySelector('.visually-hidden')!)).toBe(', no price');
  });
});

describe('Codex in Teams', () => {
  it('shows a Codex agent\'s tokens and "tokens only" in the money column', () => {
    const s = golden();
    const cx = s.cards.find((c) => c.agent.tool === 'codex')!;
    r = render(Teams, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
    const row = qa('div.trow').find((b) => b.getAttribute('data-card') === cx.id)!;
    const nums = [...row.querySelectorAll('.num')].map(text);
    expect(nums).toEqual([', output tokens 38 k', 'tokens only, tokens only']);
    expect(row.querySelectorAll('.num')[1].getAttribute('title')).toContain('Codex keeps no price');
  });
});

describe('the Spend panel', () => {
  // Only Date is faked, at midday local time on a fixed day: the days below
  // are calendar days from it, never "now minus 24 hours" (which is two days
  // back after a clock change).
  const TODAY = new Date(2026, 10, 17, 12);
  const dayOf = (back: number) => localDay(new Date(TODAY.getFullYear(), TODAY.getMonth(), TODAY.getDate() - back, 12).getTime());
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'], now: TODAY });
  });
  // Codex's tokens in the golden are cleared unless a test keeps them: most
  // tests here are about Claude Code's money alone.
  function app(edit: (s: ReturnType<typeof golden>) => void = () => {}, codex = false) {
    const s = golden();
    if (!codex) {
      for (const d of s.spend.days) {
        d.codex = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
        for (const p of d.projects) p.codex = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
      }
    }
    s.spend.days[1].day = dayOf(0);
    s.spend.days[0].day = dayOf(1);
    for (const p of s.spend.days[1].projects) p.spend.priced = true;
    s.spend.days[1].spend = { ...s.spend.days[1].spend, priced: true, usd: 63 };
    edit(s);
    const f = feedOf({ conn: 'live', snapshot: s });
    r = render(App, { feed: f.feed, actions: recordActions({}).actions });
  }
  const btn = () => qa('header button').find((b) => b.textContent?.startsWith('Spend')) as HTMLButtonElement;

  it("shows today's estimate in the header and opens project × day", async () => {
    app();
    expect(btn().getAttribute('aria-label')).toBe('Spend today about $63.00, at API list price');
    expect(text(document.getElementById(btn().getAttribute('aria-describedby')!))).toBe("Claude Code's estimate at API list price, not a bill on a subscription.");
    expect(text(btn())).toBe('Spendtoday≈ $63.00');
    expect(btn().title).toContain('at API list price');
    expect(btn().title).toContain('not a bill');
    btn().click();
    await settle();
    const dlg = q('#spend-panel')!;
    expect(dlg.getAttribute('role')).toBe('dialog');
    expect(text(dlg.querySelector('#spend-panel-note'))).toContain('≈ at API list price');
    const head = [...dlg.querySelectorAll('thead th')].map(text);
    expect(head).toEqual(['Project', '7 d', 'Today', 'Yesterday']);
    const rows = [...dlg.querySelectorAll('tbody tr')].map((tr) => [...tr.children].map(text));
    expect(rows[0]).toEqual(['alpha', '≈ $20.25', '≈ $8.75', '≈ $11.50']);
    expect(rows.at(-1)?.[0]).toBe('All');
    dlg.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(q('#spend-panel')).toBeNull();
  });

  it('has a focusable, named scroller so a keyboard can reach every day', async () => {
    app();
    btn().click();
    await settle();
    const sc = q('#spend-panel .scroll')!;
    expect(sc.getAttribute('tabindex')).toBe('0');
    expect(sc.getAttribute('role')).toBe('region');
    expect(sc.getAttribute('aria-label')).toContain('Spend by project and day');
  });

  it('says what the table is: whole sessions on cards, the last 7 days here', async () => {
    app();
    btn().click();
    await settle();
    const notes = qa('#spend-panel .note').map(text).join(' ');
    expect(notes).toContain("Cards show each agent's whole session; this table shows the last 7 days, by project.");
    expect(notes).not.toContain('leave no line in the transcript');
  });

  it('keeps the priced part when some use has no price, in the rows, the All row, the button and its label', async () => {
    app((s) => {
      s.spend.days[1].projects[0].spend = { ...s.spend.days[1].projects[0].spend, priced: false, usd: 8.75 };
      s.spend.days[1].spend = { ...s.spend.days[1].spend, priced: false, usd: 63 };
    });
    expect(text(btn())).toBe('Spendtoday≈ $63.00 + unpriced');
    expect(btn().getAttribute('aria-label')).toBe('Spend today about $63.00 plus unpriced use, at API list price');
    expect(btn().title).toContain('for the use that has a price');
    btn().click();
    await settle();
    const rows = [...qa('#spend-panel tbody tr')].map((tr) => [...tr.children].map(text));
    expect(rows[0][2]).toBe('≈ $8.75 + unpriced');
    expect(rows.at(-1)?.[0]).toBe('All');
    expect(rows.at(-1)?.[2]).toBe('≈ $63.00 + unpriced');
    expect(rows.at(-1)?.[1]).toContain('+ unpriced');
  });

  it('shows tokens and "no price" when nothing is priced', async () => {
    app((s) => {
      s.spend.days[1].spend = { ...s.spend.days[1].spend, priced: false, usd: 0 };
    });
    expect(text(btn())).toBe('Spendtoday122 k out · no price');
    expect(btn().getAttribute('aria-label')).toBe('Spend today 122 k output tokens, no price');
    expect(text(btn())).not.toContain('$');
  });

  it('marks the header figure and the panel while older sessions are still being read', async () => {
    app((s) => {
      s.spend.pending = true;
    });
    expect(text(btn())).toBe('Spendtoday≈ $63.00 …');
    expect(btn().getAttribute('aria-label')).toBe('Spend today about $63.00, at API list price, still reading older sessions');
    btn().click();
    await settle();
    expect(text(q('#spend-panel .pending'))).toBe('Still reading older sessions: these figures can still grow.');
  });

  it('says only that it is still reading when nothing is in yet', async () => {
    app((s) => {
      s.spend.pending = true;
      s.spend.days = [];
    });
    btn().click();
    await settle();
    expect(q('#spend-panel .pending')).not.toBeNull();
    expect(text(q('#spend-panel')!)).not.toContain('No tokens used');
  });

  it('says "no price" in each cell that has none', async () => {
    app((s) => {
      s.spend.days[1].projects[0].spend = { ...s.spend.days[1].projects[0].spend, priced: false, usd: 0 };
    });
    btn().click();
    await settle();
    const rows = [...qa('#spend-panel tbody tr')].map((tr) => [...tr.children].map(text));
    expect(rows[0][2]).toBe("115 k out · no price");
  });

  it('puts Codex tokens beside the money in the header, never in it', async () => {
    app(undefined, true);
    expect(text(btn())).toBe('Spendtoday≈ $63.00 · Codex 38 k out');
    expect(btn().getAttribute('aria-label')).toBe('Spend today about $63.00, at API list price, and Codex 38 k output tokens');
    expect(text(document.getElementById(btn().getAttribute('aria-describedby')!))).toBe(
      "Claude Code's estimate at API list price, not a bill on a subscription. Codex keeps no price on this machine, so its use is shown in tokens only.",
    );
    expect(btn().title).toContain('Codex keeps no price');
  });

  it('shows Codex alone in the header on a day with Codex use only', async () => {
    app((s) => {
      s.spend.days[1].spend = { tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, usd: 0, priced: true, recorded: false };
    }, true);
    expect(text(btn())).toBe('SpendtodayCodex 38 k out');
    expect(btn().getAttribute('aria-label')).toBe('Spend today Codex 38 k output tokens');
    expect(text(btn())).not.toContain('$');
    // Its description is Codex's, never Claude Code's price estimate.
    const desc = text(document.getElementById(btn().getAttribute('aria-describedby')!));
    expect(desc).toBe('Codex keeps no price on this machine, so its use is shown in tokens only.');
    expect(desc).not.toContain('API list price');
  });

  it('shows Codex tokens beside the money in the rows and the All row', async () => {
    app(undefined, true);
    btn().click();
    await settle();
    const rows = [...qa('#spend-panel tbody tr')].map((tr) => [...tr.children].map(text));
    expect(rows[0]).toEqual(['alpha', '≈ $20.25 · Codex 38 k out', '≈ $8.75 · Codex 38 k out', '≈ $11.50']);
    expect(rows.at(-1)?.[2]).toBe('≈ $63.00 · Codex 38 k out');
    expect(rows.find((x) => x[0] === 'beta')?.[2]).not.toContain('Codex');
    const notes = qa('#spend-panel .note').map(text).join(' ');
    expect(notes).toContain('Codex keeps no price');
    expect(notes).toContain('those that ended before sitboard started were read once, at start');
  });

  it('lists a project with Codex use alone', async () => {
    app((s) => {
      s.spend.days[1].projects.push({
        projectId: 'p_00000000000c',
        name: 'gamma',
        path: '~/projects/gamma',
        spend: { tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, usd: 0, priced: true, recorded: false },
        codex: { input: 10, output: 6_000, cacheRead: 100, cacheWrite: 0 },
      });
    }, true);
    btn().click();
    await settle();
    const rows = [...qa('#spend-panel tbody tr')].map((tr) => [...tr.children].map(text));
    expect(rows.find((x) => x[0] === 'gamma')).toEqual(['gamma', 'Codex 6 k out', 'Codex 6 k out', '—']);
  });

  it('says nothing about reading when it is not', async () => {
    app();
    btn().click();
    await settle();
    expect(q('#spend-panel .pending')).toBeNull();
    expect(text(btn())).not.toContain('…');
  });

  it('opening Catch-up closes it', async () => {
    app();
    btn().click();
    await settle();
    (qa('header button').find((b) => b.textContent?.startsWith('Catch-up')) as HTMLButtonElement).click();
    await settle();
    expect(q('#spend-panel')).toBeNull();
  });
});
