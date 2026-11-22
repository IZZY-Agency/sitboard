<script lang="ts">
  // The Spend panel: project × day for the 7 days sitboard reads, each
  // project's total, and all projects together. A non-modal dialog on the
  // right, like Catch-up: Escape, Close or a click outside closes it.
  import { onMount } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import type { SpendSummary } from '../contract.gen';
  import { CODEX_NO_PRICE, NOT_A_BILL, PENDING_NOTE, TOKEN_FIGURE, cellTitle, codexText, dayLabel, fmtTokens, hasSpend, hasTokens, moneyParts, spendTable, tokenCount, tokensText } from '../data/spend';
  import type { SpendCell } from '../data/spend';
  import { clean } from '../data/text';
  import { ms, VIEW_MS } from '../motion';
  import type { Refocus } from './CatchupPanel.svelte';

  let {
    id,
    spend,
    now,
    toggle,
    top,
    onclose,
  }: {
    id: string;
    spend: SpendSummary;
    now: number;
    toggle: HTMLElement | undefined;
    /** Where the panel starts, below the header (px). */
    top: number;
    onclose: (refocus: Refocus) => void;
  } = $props();

  let panel = $state<HTMLElement>();
  const table = $derived(spendTable(spend));
  const week = $derived(table.all?.total ?? table.rows[0]?.total ?? null);
  // Newest day first, right after the 7-day total: Today and the total are the
  // first things on screen, and the older days are what scrolls away.
  const order = $derived(table.days.map((_, i) => table.days.length - 1 - i));

  onMount(() => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (panel?.contains(t) || toggle?.contains(t)) return;
      onclose('stay');
    };
    document.addEventListener('pointerdown', down, true);
    return () => document.removeEventListener('pointerdown', down, true);
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    onclose('button');
  }
  function onfocusout(e: FocusEvent) {
    const to = e.relatedTarget as Node | null;
    if (!to || panel?.contains(to) || toggle?.contains(to)) return;
    onclose('stay');
  }
</script>

<div
  bind:this={panel}
  {id}
  class="panel"
  style:--top="{top}px"
  role="dialog"
  aria-labelledby="{id}-title"
  aria-describedby="{id}-note"
  tabindex="-1"
  {onkeydown}
  {onfocusout}
  in:fly={{ x: 16, duration: ms(VIEW_MS), easing: cubicOut }}
  out:fade={{ duration: ms(VIEW_MS) }}
>
  <div class="head">
    <h2 id="{id}-title" class="title">Spend · 7 days</h2>
    <button type="button" class="close" onclick={() => onclose('button')}>Close<span class="key" aria-hidden="true">Esc</span></button>
  </div>
  <p id="{id}-note" class="note">Amounts are ≈ at API list price. {NOT_A_BILL}</p>
  {#if spend.pending}<p class="note pending">{PENDING_NOTE}</p>{/if}
  <div class="body">
    {#if table.rows.length === 0}
      {#if !spend.pending}<p class="note">No tokens used in the last 7 days.</p>{/if}
    {:else}
      {#snippet figure(c: SpendCell | null)}
        {#if !c || (!hasSpend(c.spend) && !hasTokens(c.codex))}—{:else}
          {#if hasSpend(c.spend)}
            {@const m = moneyParts(c.spend)}
            {#if m.main}{m.main}{#if m.more}<span class="un">{m.more}</span>{/if}{:else}{tokensText(c.spend)}<span class="un">{' · no price'}</span>{/if}
          {/if}{#if hasTokens(c.codex)}<span class="cx"
              >{#if hasSpend(c.spend)}<span class="sep">{' · '}</span>{/if}{codexText(c.codex)}</span
            >{/if}
        {/if}
      {/snippet}
      <!-- A scrolling region a keyboard can focus, so every day can be reached (the usual pattern for a wide table). -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="scroll" role="region" aria-label="Spend by project and day, scrolls sideways" tabindex="0">
        <table>
          <thead>
            <tr>
              <th scope="col" class="pj">Project</th>
              <th scope="col" class="r tot">7 d</th>
              {#each order as i (i)}<th scope="col" class="r">{dayLabel(table.days[i], now)}</th>{/each}
            </tr>
          </thead>
          <tbody>
            {#each table.all ? [...table.rows, table.all] : table.rows as r (r.key)}
              <tr class:all={r.key === 'all'}>
                <th scope="row" class="pj u" title={clean(r.path)}>{clean(r.name)}</th>
                <td class="r tot" title={cellTitle(r.total)}>{@render figure(r.total)}</td>
                {#each order as i (i)}
                  {@const c = r.cells[i]}
                  <td class="r" class:none={!c} title={c ? cellTitle(c) : undefined}>{@render figure(c)}</td>
                {/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if week}
        <p class="note">
          {TOKEN_FIGURE === 'output' ? 'Output tokens' : 'Tokens'}: {fmtTokens(tokenCount(week.spend.tokens))}{#if hasTokens(week.codex)}, and {codexText(week.codex)}{/if}
          in 7 days. Days with no use are left out. "+ unpriced"
          means some use has no price, so the amount is only the part that has one; a cell with no price at all shows tokens. Cards show each agent's whole
          session; this table shows the last 7 days, by project.
        </p>
        {#if hasTokens(week.codex)}
          <p class="note">
            {CODEX_NO_PRICE} Its tokens sit beside the amounts, never in them. The days count the Codex sessions of the last 7 days; those that ended before sitboard started were read once, at start.
          </p>
        {/if}
      {/if}
    {/if}
  </div>
</div>

<style>
  .panel {
    position: fixed;
    top: var(--top);
    right: 0;
    bottom: 0;
    z-index: 4;
    width: min(560px, 100vw);
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border-left: 1px solid var(--border);
    border-top: 1px solid var(--border);
    color: var(--fg-body);
    font-size: var(--text-m);
  }
  .panel:focus {
    outline: none;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 6px 12px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .title {
    margin: 0;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--fg);
  }
  .close {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-height: 44px;
    padding: 0 14px;
    border: 1px solid var(--border);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--fg);
  }
  .close:hover {
    background: rgb(221 218 207 / 5%);
  }
  .key {
    padding: 0 4px;
    border: 1px solid var(--border);
    color: var(--muted);
    font-weight: 400;
    letter-spacing: 0.04em;
  }
  .body {
    flex: 1;
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 4px 16px 16px;
  }
  .note {
    margin: 0;
    padding: 10px 16px 0;
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .body .note {
    padding: 10px 0 0;
  }
  .scroll {
    overflow-x: auto;
    overscroll-behavior: contain;
  }
  .scroll:focus-visible {
    outline: 2px solid var(--amber);
    outline-offset: -2px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--text-s);
    font-variant-numeric: tabular-nums;
  }
  th,
  td {
    padding: 6px 8px;
    border-bottom: 1px solid var(--rule);
    white-space: nowrap;
  }
  thead th {
    font-size: var(--text-xs);
    font-weight: 400;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--muted);
    text-align: left;
  }
  .r {
    text-align: right;
  }
  .pj {
    max-width: 160px;
    overflow: hidden;
    text-overflow: ellipsis;
    text-align: left;
    font-weight: 400;
    color: var(--fg-body);
    /* The project stays in view while the older days scroll. */
    position: sticky;
    left: 0;
    background: var(--surface);
  }
  .tot {
    border-left: 1px solid var(--rule);
    border-right: 1px solid var(--rule);
    color: var(--fg);
  }
  /* Codex's tokens on a line of their own, below Claude Code's figure; the
     dot that separates them in words is not drawn. */
  .cx {
    display: block;
    color: var(--muted);
  }
  .sep {
    display: none;
  }
  .un {
    display: block;
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .pending {
    color: var(--amber);
  }
  td.none {
    color: var(--muted);
  }
  tr.all th,
  tr.all td {
    color: var(--fg);
    border-bottom: 0;
  }
  @media (max-width: 559px) {
    .panel {
      top: 0;
      width: 100vw;
      border-left: 0;
      border-top: 0;
    }
  }
</style>
