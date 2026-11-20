<script lang="ts">
  // The harness bar: it always says FAKE DATA, so a screenshot can never pass for the product.
  // It carries the harness marker, which the build check forbids in the product.
  import { marker } from './marker.json';
  let {
    scenario,
    scenarios,
    live,
    catchup,
    catchups,
  }: { scenario: string; scenarios: readonly string[]; live: boolean; catchup: string; catchups: readonly string[] } = $props();
  const q = (s: string, c: string, l: boolean) => `?s=${s}&c=${c}${l ? '&live=1' : ''}`;
</script>

<div class="bar" data-harness={marker}>
  <strong>FAKE DATA · dev harness, placeholder data</strong>
  {#each scenarios as s (s)}
    <a href={q(s, catchup, live)} aria-current={s === scenario ? 'page' : undefined}>{s}</a>
  {/each}
  <a href={q(scenario, catchup, !live)}>{live ? 'live: on' : 'live: off'}</a>
  <span>catch-up:</span>
  {#each catchups as c (c)}
    <a href={q(scenario, c, live)} aria-current={c === catchup ? 'page' : undefined}>{c}</a>
  {/each}
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
