<script lang="ts">
  // Every state, for a few sample ids, at 20 px on the page ground (Teams,
  // Lanes) and 24 px on a card; each also at twice the size, which is what a
  // 2x screen draws.
  import Critter from '../src/components/Critter.svelte';
  import { CRITTER_STATES } from '../src/data/critter';

  const params = new URLSearchParams(location.search);
  const ids = (params.get('ids') ?? 'claude:site-lead,claude:site-w1,claude:site-w2,claude:site-w3,claude:site-d1,claude:site-k1').split(',');
</script>

<div class="sheet">
  {#each [20, 24] as size (size)}
    {#each [1, 2] as zoom (zoom)}
      <section class:card={size === 24}>
        <h2 class="label">{size} px{zoom === 2 ? ' · as a 2x screen draws it' : ''}</h2>
        <div class="grid" style:--n={CRITTER_STATES.length}>
          <span></span>
          {#each CRITTER_STATES as s (s)}<span class="st">{s.replace('_', ' ')}</span>{/each}
          {#each ids as id (id)}
            <span class="id">{id.replace('claude:', '')}</span>
            {#each CRITTER_STATES as s (s)}<span class="cell" style:zoom><Critter {id} mood={s} {size} /></span>{/each}
          {/each}
        </div>
      </section>
    {/each}
  {/each}
</div>

<style>
  .sheet {
    display: flex;
    flex-direction: column;
    gap: 20px;
    padding: 20px 24px;
  }
  section {
    --hole: var(--bg);
    padding: 12px;
    border: 1px solid var(--border);
  }
  section.card {
    --hole: var(--surface);
    background: var(--surface);
  }
  h2 {
    margin: 0 0 10px;
  }
  .grid {
    display: grid;
    grid-template-columns: 110px repeat(var(--n), 1fr);
    gap: 8px 4px;
    align-items: center;
  }
  .st,
  .id {
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .cell {
    justify-self: start;
  }
</style>
