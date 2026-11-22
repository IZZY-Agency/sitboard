<script lang="ts">
  // The header's Spend button: today's estimate, and it opens the Spend panel.
  // Its description (aria-describedby) says what the figure is: an estimate at
  // API list price, not a bill.

  const NOTE_ID = 'spend-button-note';
  let {
    figure,
    spoken,
    pending = false,
    note,
    title,
    open,
    controls,
    onclick,
    button = $bindable(),
  }: {
    /** Today's figure, "≈ $63" or "1.2 M out"; "" when nothing was used today. */
    figure: string;
    /** The same figure as a screen reader says it ("about $63.00, at API list price", "1.2 M output tokens, no price"); "" when nothing was used today. */
    spoken: string;
    /** Older sessions are still being read: the figure can still grow. */
    pending?: boolean;
    /** What the figure is, for its description: Claude Code's estimate, Codex's tokens, or both. */
    note: string;
    /** The hover text: what the figure is (the estimate, at API list price). */
    title: string;
    open: boolean;
    controls: string;
    onclick: () => void;
    button?: HTMLButtonElement;
  } = $props();
  const label = $derived(
    (spoken ? `Spend today ${spoken}` : figure ? 'Spend today' : 'Spend, nothing used today') + (pending ? ', still reading older sessions' : ''),
  );
</script>

<span id={NOTE_ID} class="visually-hidden">{note}</span>
<button
  bind:this={button}
  type="button"
  class="spend"
  aria-expanded={open}
  aria-controls={open ? controls : undefined}
  aria-label={label}
  aria-describedby={NOTE_ID}
  {title}
  {onclick}
>
  Spend{#if figure}<span class="t" aria-hidden="true">today</span><span class="n" aria-hidden="true">{figure}</span>{/if}
</button>

<style>
  .spend {
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
  .spend:hover {
    color: var(--fg);
    background: rgb(221 218 207 / 5%);
  }
  .spend:active {
    transform: translateY(1px);
  }
  .spend[aria-expanded='true'] {
    background: var(--amber);
    border-color: var(--amber);
    color: var(--bg);
  }
  .t {
    font-weight: 400;
  }
  .n {
    font-weight: 400;
    letter-spacing: 0.02em;
    text-transform: none;
    font-variant-numeric: tabular-nums;
    text-decoration: underline dotted;
    text-underline-offset: 3px;
  }
</style>
