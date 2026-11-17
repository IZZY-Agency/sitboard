<script lang="ts">
  // The parts of the selected project: "all parts", then each part with its
  // count, the parent's own cards under the parent's name.
  import { moveFocus, onKeys, tabStop } from '../keys';
  import Count from './Count.svelte';

  export interface PartChip {
    key: string;
    name: string;
    count: number;
  }

  let {
    label,
    chips,
    active,
    onselect,
  }: { label: string; chips: PartChip[]; active: string; onselect: (key: string) => void } = $props();

  let row = $state<HTMLElement>();
  let focused = $state<string | null>(null);
  const stop = $derived(tabStop(chips.map((c) => c.key), focused, active));
</script>

<div
  class="filter"
  role="toolbar"
  aria-label={label}
  bind:this={row}
  {@attach onKeys(
    (e) => row && moveFocus(e, row, 'button', 'x'),
    (e) => (focused = (e.target as HTMLElement).dataset.key ?? focused),
  )}
>
  <!-- no whitespace between chips: inline chips would get an extra gap
  -->{#each chips as c (c.key)}<button
      type="button"
      data-key={c.key}
      tabindex={c.key === stop ? 0 : -1}
      aria-pressed={active === c.key}
      onclick={() => onselect(c.key)}
    >
      <span class="name u" title={c.name}>{c.name}</span>{' '}<span class="n"><Count value={c.count} /></span>
    </button>{/each}
</div>

<style>
  /* Chips are inline so the browser balances the rows when they wrap: two
     even rows, never one row and a lone chip. */
  .filter {
    padding: 2px calc(var(--gutter) - 4px) 0;
    border-bottom: 1px solid var(--rule);
    text-wrap: balance;
    line-height: 0;
  }
  button {
    margin: 0 4px;
    max-width: calc(100% - 8px);
    vertical-align: top;
    line-height: 1.5;
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: 7px;
    min-height: 36px;
    padding: 0 10px;
    font-size: var(--text-s);
    color: var(--fg-body);
    border-bottom: 2px solid transparent;
    transition:
      color var(--press) ease-out,
      border-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  button:hover {
    color: var(--fg);
    border-bottom-color: var(--border-hover);
  }
  button:active {
    transform: translateY(1px);
  }
  button[aria-pressed='true'] {
    color: var(--fg);
    border-bottom-color: var(--amber);
  }
  /* A long name is cut inside its chip; the full name stays in the text (for
     assistive tech) and in the title (on hover). */
  .name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .n {
    flex: none;
    color: var(--muted);
  }
</style>
