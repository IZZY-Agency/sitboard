<script lang="ts">
  // The header's Catch-up button: opens the panel, says how many items are new.
  import type { CatchupStatus } from '../data/catchup.svelte';

  let {
    count,
    listed,
    status,
    open,
    controls,
    onclick,
    button = $bindable(),
  }: {
    /** Unread items, counting those past the server's cap. */
    count: number;
    /** Items the list holds: fewer than count at the cap. */
    listed: number;
    status: CatchupStatus;
    open: boolean;
    controls: string;
    onclick: () => void;
    button?: HTMLButtonElement;
  } = $props();

  // Before the first list there is no number to show, not a zero.
  const known = $derived(status === 'ready' || (status === 'failed' && count > 0));
  const label = $derived(
    !known
      ? 'Catch-up'
      : count === 0
        ? 'Catch-up, nothing new'
        : `Catch-up, ${count} new ${count === 1 ? 'item' : 'items'}${listed < count ? `, the newest ${listed} listed` : ''}`,
  );
</script>

<button
  bind:this={button}
  type="button"
  class="catchup"
  class:new={known && count > 0}
  aria-expanded={open}
  aria-controls={open ? controls : undefined}
  aria-label={label}
  {onclick}
>
  Catch-up{#if known && count > 0}<span class="n" aria-hidden="true">{listed < count ? `${listed}+` : count}</span>{/if}
</button>

<style>
  .catchup {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-height: 44px;
    padding: 0 12px;
    border: 1px solid var(--border);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--fg-body);
    white-space: nowrap;
    transition:
      background-color var(--press) ease-out,
      color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .catchup:hover {
    color: var(--fg);
    background: rgb(221 218 207 / 5%);
  }
  .catchup:active {
    transform: translateY(1px);
  }
  .catchup.new {
    border-color: var(--amber-edge);
    color: var(--amber);
  }
  .catchup[aria-expanded='true'] {
    background: var(--amber);
    border-color: var(--amber);
    color: var(--bg);
  }
  .n {
    padding: 0 5px;
    border: 1px solid currentColor;
    font-variant-numeric: tabular-nums;
  }
</style>
