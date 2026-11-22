<script lang="ts">
  // Board (default): one column per state. The project grouping above or beside it filters the cards.
  import { flip } from 'svelte/animate';
  import type { Card as CardT, Snapshot } from '../contract.gen';
  import { byColumn, columnMeta, COLUMNS, columnTone, type Index } from '../data/derive';
  import { flipOpts, makeMove } from '../motion';
  import Card from '../components/Card.svelte';
  import Count from '../components/Count.svelte';

  let {
    snapshot,
    ix,
    now,
    arrivals,
    onjump,
    onseen,
  }: {
    snapshot: Snapshot;
    ix: Index;
    now: number;
    arrivals: ReadonlyMap<string, number>;
    onjump: (c: CardT) => void;
    onseen: (c: CardT) => void;
  } = $props();

  const cols = $derived(byColumn(snapshot.cards));
  const [send, receive] = makeMove();
</script>

<div class="scroller">
  <div class="grid">
    {#each COLUMNS as col (col)}
      {@const meta = columnMeta(col, ix.gitlabHosts)}
      <section class="col" aria-labelledby="col-{col}" data-column={col}>
        <div class="head">
          <span class="dot tone-{columnTone(col)}"></span>
          <h2 class="label" id="col-{col}">{meta.label}</h2>
          <span class="n"><Count value={cols[col].length} /></span>
          <span class="rule" aria-hidden="true"></span>
        </div>
        <p class="hint">{meta.hint}</p>
        {#each cols[col] as card (card.id)}
          <div class="slot" animate:flip={flipOpts()} in:receive={{ key: card.id }} out:send={{ key: card.id }}>
            <Card {card} {ix} {now} agtermPresent={snapshot.agtermPresent} flashToken={arrivals.get(card.id) ?? 0} {onjump} {onseen} />
          </div>
        {/each}
        {#if cols[col].length === 0}
          <p class="empty">{meta.empty}</p>
        {/if}
      </section>
    {/each}
  </div>
</div>

<style>
  .scroller {
    overflow-x: auto;
    padding: 24px var(--gutter) 40px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
    gap: 20px;
    min-width: 1340px;
    align-items: start;
  }
  /* Beside the rail the six columns fit in less width before the board scrolls. */
  :global(.railed) .scroller {
    padding-left: 20px;
    padding-right: 20px;
  }
  :global(.railed) .grid {
    gap: 14px;
    min-width: 1120px;
  }
  .col {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: 0;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  h2 {
    margin: 0;
    white-space: nowrap;
  }
  .n {
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .rule {
    flex: 1;
    height: 1px;
    background: var(--rule-strong);
  }
  .hint {
    margin: 0 0 4px;
    min-height: 34px;
    font-size: var(--text-s);
    line-height: 1.45;
    color: var(--muted);
  }
  .slot {
    min-width: 0;
  }
  .empty {
    margin: 0;
    padding: 14px 12px;
    border: 1px dashed var(--border);
    font-size: var(--text-s);
    line-height: 1.5;
    color: var(--muted);
  }
</style>
