<script lang="ts">
  // Lanes: a needs-you strip on top, then one row per project and one column per
  // state. It carries the all-projects overview.
  import { flip } from 'svelte/animate';
  import type { Card as CardT, Snapshot } from '../contract.gen';
  import {
    agentName,
    jumpLabel,
    COLUMN_META,
    columnTone,
    inbox,
    laneColumns,
    laneNote,
    lanes,
    timeInState,
    type Index,
  } from '../data/derive';
  import { clean } from '../data/text';
  import { flipOpts, makeMove } from '../motion';
  import Count from '../components/Count.svelte';
  import Critter from '../components/Critter.svelte';
  import { critterState } from '../data/critter';
  import InboxItem from '../components/InboxItem.svelte';
  import RowLinks from '../components/RowLinks.svelte';

  let {
    snapshot,
    ix,
    scope = 'All projects',
    now,
    arrivals,
    onjump,
    onseen,
  }: {
    snapshot: Snapshot;
    ix: Index;
    scope?: string; // what the project grouping selected
    now: number;
    arrivals: ReadonlyMap<string, number>;
    onjump: (c: CardT) => void;
    onseen: (c: CardT) => void;
  } = $props();

  const strip = $derived(inbox(snapshot.cards));
  const cols = $derived(laneColumns(snapshot.cards));
  const rows = $derived(lanes(snapshot));
  const heads = $derived(cols.map((c) => ({ col: c, n: snapshot.cards.filter((x) => x.column === c).length })));
  const totals = $derived([
    { n: snapshot.counts.working, word: 'working' },
    { n: snapshot.counts.needsYou, word: 'needs you' },
    { n: snapshot.counts.inReview, word: 'in review' },
    { n: snapshot.counts.stale, word: 'stale' },
  ]);
  const [send, receive] = makeMove();

  // A click that reached the item itself, not the jump button or a link: a
  // raised part (.tip). The button and the links handle their own clicks.
  function rowClick(e: MouseEvent, card: CardT) {
    if (!(e.target instanceof Element) || !e.target.closest('a, button')) onjump(card);
  }
</script>

<div class="lanes">
  <section class="strip" aria-labelledby="strip-head">
    <div class="head">
      <span class="dot tone-needs"></span>
      <h2 class="label" id="strip-head">Needs you</h2>
      <span class="n"><Count value={strip.length} /></span>
      <span class="rule" aria-hidden="true"></span>
    </div>
    {#if strip.length}
      <div class="items">
        {#each strip as card (card.id)}
          <div class="strip-slot" animate:flip={flipOpts()} in:receive={{ key: 's' + card.id }} out:send={{ key: 's' + card.id }}>
            <InboxItem {card} {ix} {now} flashToken={arrivals.get(card.id) ?? 0} {onjump} {onseen} />
          </div>
        {/each}
      </div>
    {:else}
      <p class="empty">Nobody needs you right now. Blocked agents, turns you have not seen, stale agents and failing checks land here.</p>
    {/if}
  </section>

  <section class="overview" aria-label={scope}>
    <span class="label u">{scope}</span>
    <ul class="totals">
      {#each totals as t (t.word)}
        <li><span class="num"><Count value={t.n} /></span> {t.word}</li>
      {/each}
    </ul>
  </section>

  <div class="scroller">
    <div class="table" style:--cols={cols.length}>
      <div class="row headrow">
        <div class="cell pcell label muted">Project</div>
        {#each heads as h (h.col)}
          <div class="cell colhead">
            <span class="dot tone-{columnTone(h.col)}"></span>
            <span class="label">{COLUMN_META[h.col].label}</span>
            <span class="hn"><Count value={h.n} /></span>
          </div>
        {/each}
      </div>
      {#each rows as row (row.project.id)}
        <div class="row" animate:flip={flipOpts()}>
          <div class="cell pcell">
            <span class="pname u" title={row.name}>{row.name}</span>
            <span class="path u">{clean(row.project.path)}</span>
            <span class="meta">{row.meta}</span>
            {#if row.overview.length}
              <span class="meta ov">{row.overview.join(' · ')}</span>
            {/if}
          </div>
          {#each cols as col (col)}
            <div class="cell">
              {#each row.cells[col] as card (card.id)}
                {@const note = laneNote(card, ix)}
                {@const name = agentName(card)}
                <!-- The name is the jump button; its hit area covers the whole
                     item, and the links in the note sit above it (RowLinks). -->
                <!-- A pointer-only click for the parts raised above the jump area
                     (.tip, for their tooltips): they still jump. Keyboards and
                     screen readers use the button. -->
                <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
                <div
                  class="it"
                  data-card={card.id}
                  onclick={(e) => rowClick(e, card)}
                  animate:flip={flipOpts()}
                  in:receive={{ key: card.id }}
                  out:send={{ key: card.id }}
                >
                  <span class="it-top">
                    <button type="button" class="jump" onclick={() => onjump(card)}
                      ><span class="visually-hidden">{jumpLabel(card)}</span
                      ><Critter id={card.id} mood={critterState(card)} size={20} /><span class="agent u">{name}</span></button
                    >
                    <span class="since">{timeInState(card, now)}</span>
                  </span>
                  <span class="note tone-{note.tone} u"
                    >{#if note.head}{note.head}{' · '}{/if}<span class="model {note.hue ? `model-${note.hue}` : ''}">{note.model}</span
                    >{#if note.effort}<span class="visually-hidden">, effort</span>{' '}<span
                        class="eff tip effort-{note.effort.level}"
                        class:launch={note.effort.launch}
                        title={note.effort.title}>{note.effort.text}{#if note.effort.launch}<span class="at">{' at launch'}</span>{/if}</span
                      >{/if}{#if note.tail}{' · '}{note.tail}{/if}{#if note.prs.length}{' · '}<RowLinks parts={note.prs} />{/if}</span>
                </div>
              {/each}
            </div>
          {/each}
        </div>
      {/each}
    </div>
  </div>
</div>

<style>
  .lanes {
    display: flex;
    flex-direction: column;
    gap: 28px;
    padding: 24px var(--gutter) 40px;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 10px;
  }
  h2 {
    margin: 0;
  }
  .n,
  .hn {
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .hn {
    margin-left: auto;
    font-weight: 400;
  }
  .rule {
    flex: 1;
    height: 1px;
    background: var(--rule-strong);
  }
  .items {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
  }
  .strip-slot {
    flex: 1 1 360px;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .strip-slot > :global(*) {
    flex: 1;
  }
  .empty {
    margin: 0;
    padding: 14px 12px;
    border: 1px dashed var(--border);
    font-size: var(--text-s);
    color: var(--muted);
  }
  .overview {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px 20px;
    margin-bottom: -16px;
  }
  .totals {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 18px;
    margin: 0;
    padding: 0;
    list-style: none;
    font-size: var(--text-m);
    color: var(--muted);
  }
  .num {
    color: var(--fg);
  }
  .scroller {
    overflow-x: auto;
  }
  .table {
    min-width: 1180px;
    border: 1px solid var(--border);
  }
  .row {
    display: grid;
    grid-template-columns: 220px repeat(var(--cols), minmax(0, 1fr));
    border-bottom: 1px solid var(--rule);
  }
  .row:last-child {
    border-bottom: 0;
  }
  .headrow {
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }
  .cell {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    padding: 6px 14px;
  }
  .cell + .cell {
    border-left: 1px solid var(--rule);
  }
  .headrow .cell {
    padding: 10px 14px;
  }
  .colhead {
    flex-direction: row;
    align-items: center;
    gap: 8px;
  }
  .muted {
    color: var(--muted);
  }
  .pcell {
    padding: 12px 14px;
  }
  .pname {
    font-family: var(--font-title);
    font-size: var(--text-lead);
    font-weight: 600;
    color: var(--fg);
    overflow-wrap: anywhere;
  }
  .path {
    font-size: var(--text-xs);
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta {
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .ov {
    color: var(--fg-body);
  }
  .it {
    /* The critter's eyes are holes in the page ground. */
    --hole: var(--bg);
    position: relative;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 1px;
    width: 100%;
    min-height: 44px;
    padding: 4px 0;
    transition: transform var(--press) ease-out;
  }
  .it:has(.jump:active, .tip:active) {
    transform: translateY(1px);
  }
  .it:has(.jump:hover, .tip:hover) .agent {
    color: var(--amber-hover);
  }
  .it-top {
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
    font-size: var(--text-m);
  }
  .jump {
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
  }
  /* The button's hit area, stretched over the whole item; its focus ring too. */
  .jump::after {
    content: '';
    position: absolute;
    inset: 0;
  }
  .jump:focus-visible {
    outline: none;
  }
  .jump:focus-visible::after {
    outline: 2px solid var(--amber);
    outline-offset: 0;
  }
  /* A part with a tooltip sits above the jump area, so the pointer reaches its
     title; a click on it still jumps (rowClick). */
  .tip {
    position: relative;
    z-index: 1;
    cursor: pointer;
  }
  .agent {
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    transition: color var(--press) ease-out;
  }
  .since {
    margin-left: auto;
    flex: none;
    font-size: var(--text-s);
    color: var(--muted);
  }
  .note {
    padding-left: 27px;
    font-size: var(--text-xs);
    line-height: 1.45;
    color: var(--tone);
    overflow-wrap: anywhere;
  }
  .model {
    color: var(--model, inherit);
  }
  /* The level keeps the note's tone, apart from a muted note, where it is on
     the ramp. From high up it is heavier and sits on a small filled patch
     (base.css), on the ramp in every note: the ramp is what reads on the
     fill. Set at launch, it has no patch. */
  .eff {
    color: inherit;
    font-weight: var(--effort-weight, 400);
  }
  .tone-muted .eff,
  .eff:is(.effort-high, .effort-xhigh, .effort-max):not(.launch) {
    color: var(--effort);
  }
  .eff:is(.effort-high, .effort-xhigh, .effort-max):not(.launch) {
    padding: 0 4px;
    background: var(--effort-fill);
  }
  /* "at launch" no brighter than the faintest level; in a needs-you or failing note it keeps the tone. */
  .tone-muted .at {
    color: var(--effort-low);
  }
  .at {
    font-weight: 400;
  }
</style>
