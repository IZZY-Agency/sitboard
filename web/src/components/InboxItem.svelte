<script lang="ts">
  // One entry of the needs-you inbox or the idle queue: who, where, what, and why.
  import type { Card } from '../contract.gen';
  import { agentName, attentionLines, canJump, cardTitle, columnTone, projectLabel, timeInState, type Index, type Line } from '../data/derive';
  import { flash } from '../motion';

  let {
    card,
    ix,
    now,
    agtermPresent,
    reason,
    showProject = true,
    flashToken = 0,
    onjump,
    onseen,
  }: {
    card: Card;
    ix: Index;
    now: number;
    agtermPresent: boolean;
    /** Overrides the attention words, for the idle queue. */
    reason?: Line;
    showProject?: boolean;
    flashToken?: number;
    onjump: (c: Card) => void;
    onseen: (c: Card) => void;
  } = $props();
  const place = $derived(projectLabel(card, ix));

  let el = $state<HTMLElement>();
  const name = $derived(agentName(card));
  const lines = $derived(reason ? [reason] : attentionLines(card, now));
  const tone = $derived(card.attention.includes('blocked') || card.attention.includes('unseen') ? 'needs' : card.attention.includes('failing_checks') ? 'failing' : columnTone(card.column));

  $effect(() => {
    if (flashToken && el) flash(el);
  });
</script>

<article class="item" class:needs={tone === 'needs'} class:failing={tone === 'failing'} bind:this={el}>
  <button
    class="main"
    type="button"
    onclick={() => onjump(card)}
  >
    <span class="visually-hidden">{canJump(card, agtermPresent) ? 'Jump to agterm:' : 'Show where it runs:'}</span>
    <span class="top">
      <span class="dot tone-{tone}" class:pulse={card.column === 'working'}></span>
      <span class="agent u">{name}</span>
      {#if showProject}<span class="project u">{place.name}{#if place.part}<span class="part">{' / '}{place.part}</span>{/if}</span>{/if}
      <span class="since">{timeInState(card, now)}</span>
    </span>
    <span class="title u">{cardTitle(card)}</span>
    <span class="reason">
      {#each lines as l, i (i)}<span class="tone-{l.tone}">{l.text}</span>{/each}
    </span>
  </button>
  {#if card.attention.includes('unseen')}
    <button class="seen" type="button" onclick={() => onseen(card)}>Mark seen</button>
  {/if}
</article>

<style>
  .project .part {
    color: var(--muted);
  }
  .item {
    display: flex;
    flex-direction: column;
    min-width: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    transition:
      border-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .item:hover {
    border-color: var(--border-hover);
  }
  .item.needs {
    border-color: var(--amber-line);
  }
  .item.needs:hover {
    border-color: var(--amber);
  }
  .item.failing {
    border-color: var(--failing-line);
  }
  .item.failing:hover {
    border-color: var(--state-failing);
  }
  .item:has(.main:active),
  .item:has(.seen:active) {
    transform: translateY(1px);
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: 100%;
    min-height: 44px;
    padding: 12px 14px;
  }
  .main:focus-visible,
  .seen:focus-visible {
    outline-offset: -2px;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    font-size: var(--text-m);
  }
  .agent {
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .project {
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .since {
    margin-left: auto;
    flex: none;
    font-size: var(--text-s);
    color: var(--muted);
  }
  .title {
    font-family: var(--font-title);
    font-size: var(--text-title);
    font-weight: 500;
    line-height: 1.35;
    color: var(--fg);
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    overflow-wrap: anywhere;
  }
  .reason {
    display: flex;
    flex-wrap: wrap;
    gap: 2px 10px;
    font-size: var(--text-s);
  }
  .reason > span {
    color: var(--tone);
  }
  .seen {
    min-height: 36px;
    padding: 0 14px;
    border-top: 1px solid var(--rule);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--amber);
  }
  .seen:hover {
    color: var(--amber-hover);
    background: var(--amber-soft);
  }
</style>
