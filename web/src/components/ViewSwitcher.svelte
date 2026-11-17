<script lang="ts">
  import type { View } from '../data/view';

  let { view, onview }: { view: View; onview: (v: View) => void } = $props();
  const items: Array<{ id: View; label: string }> = [
    { id: 'board', label: 'Board' },
    { id: 'lanes', label: 'Lanes' },
    { id: 'teams', label: 'Teams' },
  ];
</script>

<nav aria-label="View">
  {#each items as it (it.id)}
    <button type="button" aria-current={view === it.id ? 'page' : undefined} onclick={() => onview(it.id)}>{it.label}</button>
  {/each}
</nav>

<style>
  nav {
    display: flex;
    border: 1px solid var(--border);
  }
  button {
    display: inline-flex;
    align-items: center;
    min-height: 44px;
    padding: 0 16px;
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--fg-body);
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
  button[aria-current='page'] {
    background: var(--amber);
    color: var(--bg);
    font-weight: 700;
  }
  button[aria-current='page']:hover {
    background: var(--amber-hover);
  }
</style>
