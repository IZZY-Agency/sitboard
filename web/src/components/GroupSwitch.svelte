<script lang="ts">
  import type { Grouping } from '../data/view';

  let { mode, onmode }: { mode: Grouping; onmode: (g: Grouping) => void } = $props();
</script>

<div class="switch" role="group" aria-label="Group projects as">
  <button type="button" aria-pressed={mode === 'tiles'} onclick={() => onmode('tiles')}>tiles</button><button
    type="button"
    aria-pressed={mode === 'rail'}
    onclick={() => onmode('rail')}>rail</button
  >
</div>

<style>
  .switch {
    display: inline-flex;
    flex: none;
    border: 1px solid var(--border);
  }
  button {
    min-height: 32px;
    padding: 0 12px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--muted);
    transition:
      background-color var(--press) ease-out,
      color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  button + button {
    border-left: 1px solid var(--border);
  }
  button:hover {
    color: var(--fg);
    background: rgb(221 218 207 / 5%);
  }
  button:active {
    transform: translateY(1px);
  }
  button[aria-pressed='true'] {
    color: var(--amber);
    background: var(--amber-soft);
  }
  button:focus-visible {
    outline-offset: -2px;
  }
</style>
