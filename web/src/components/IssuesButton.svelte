<script lang="ts">
  // The header's Issues button: opens the panel, says how many issues are open
  // in the repositories of the projects on the board.
  let {
    count,
    open,
    controls,
    onclick,
    button = $bindable(),
  }: {
    /** Open issues, each repository once; null before any is known. */
    count: number | null;
    open: boolean;
    controls: string;
    onclick: () => void;
    button?: HTMLButtonElement;
  } = $props();

  const label = $derived(
    count === null ? 'Issues' : count === 0 ? 'Issues, none open' : `Issues, ${count} open ${count === 1 ? 'issue' : 'issues'}`,
  );
</script>

<button
  bind:this={button}
  type="button"
  class="issues"
  aria-expanded={open}
  aria-controls={open ? controls : undefined}
  aria-label={label}
  {onclick}
>
  Issues{#if count !== null && count > 0}<span class="n" aria-hidden="true">{count}</span>{/if}
</button>

<style>
  /* Quiet: open issues are not news, so the chip never lights up on its own. */
  .issues {
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
  .issues:hover {
    color: var(--fg);
    background: rgb(221 218 207 / 5%);
  }
  .issues:active {
    transform: translateY(1px);
  }
  .issues[aria-expanded='true'] {
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
