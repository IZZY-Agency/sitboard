<script lang="ts">
  // The harness bar: it always says FAKE DATA, so a screenshot can never pass for the product.
  // It carries the harness marker, which the build check forbids in the product.
  import { marker } from './marker.json';
  let { scenario, scenarios, live }: { scenario: string; scenarios: readonly string[]; live: boolean } = $props();
</script>

<div class="bar" data-harness={marker}>
  <strong>FAKE DATA · dev harness, placeholder data</strong>
  {#each scenarios as s (s)}
    <a href="?s={s}{live ? '&live=1' : ''}" aria-current={s === scenario ? 'page' : undefined}>{s}</a>
  {/each}
  <a href="?s={scenario}{live ? '' : '&live=1'}">{live ? 'live: on' : 'live: off'}</a>
</div>

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    padding: 6px 24px;
    background: #b3261e;
    color: #fff;
    font-size: 12px;
  }
  a {
    color: #fff;
  }
  a[aria-current='page'] {
    font-weight: 700;
  }
</style>
