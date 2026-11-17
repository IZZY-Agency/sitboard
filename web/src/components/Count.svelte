<script lang="ts">
  // A count that ticks when it changes: the old number slides out, the new one in.
  import { fly } from 'svelte/transition';
  import { COUNT_MS, ms } from '../motion';

  let { value }: { value: number } = $props();
</script>

<span class="count">
  {#key value}
    <span class="n" in:fly={{ y: 8, duration: ms(COUNT_MS) }} out:fly={{ y: -8, duration: ms(COUNT_MS) }}>{value}</span>
  {/key}
</span>

<style>
  .count {
    display: inline-grid;
    overflow: hidden;
    vertical-align: bottom;
  }
  .n {
    grid-area: 1 / 1;
  }
</style>
