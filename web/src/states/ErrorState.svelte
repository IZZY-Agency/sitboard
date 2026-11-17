<script lang="ts">
  let { kind, retryAt, now }: { kind: 'unauthorized' | 'outdated' | 'unreachable'; retryAt?: number; now: number } = $props();
</script>

<section class="state" class:warn={kind !== 'outdated'}>
  {#if kind === 'unauthorized'}
    <h1>This tab lost its key</h1>
    <p>
      sitboard opens with a private link that works while this run is alive, and forgets it when it stops. Open the link sitboard printed in the terminal when it
      started. If sitboard is not running, start it again with <code>sitboard</code>.
    </p>
  {:else if kind === 'outdated'}
    <h1>sitboard was updated</h1>
    <p>This page was built for another version of sitboard. Reload the page to get the new one.</p>
    <button type="button" class="primary" onclick={() => location.reload()}>Reload the page</button>
  {:else}
    <h1>Cannot reach sitboard</h1>
    <p>
      Is it still running? This page keeps trying{#if retryAt}; next attempt {retryAt - now > 1500
          ? `in ${Math.ceil((retryAt - now) / 1000)} s`
          : 'now'}{/if}.
    </p>
    <p class="muted">If you stopped it, start it again with <code>sitboard</code> and open the link it prints.</p>
  {/if}
</section>

<style>
  .state {
    padding: 64px var(--gutter);
    max-width: 640px;
  }
  h1 {
    margin: 0 0 10px;
    font-family: var(--font-title);
    font-size: 22px;
    font-weight: 500;
    color: var(--fg);
  }
  .warn h1 {
    color: var(--amber);
  }
  p {
    margin: 0 0 16px;
  }
  .muted {
    color: var(--muted);
  }
  code {
    color: var(--fg);
  }
  .primary {
    min-height: 44px;
    padding: 0 16px;
    background: var(--amber);
    color: var(--bg);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    transition:
      background-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .primary:hover {
    background: var(--amber-hover);
  }
  .primary:active {
    transform: translateY(1px);
  }
</style>
