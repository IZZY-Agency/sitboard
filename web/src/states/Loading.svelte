<script lang="ts">
  let { label = 'Connecting', text = 'Connecting to sitboard' }: { label?: string; text?: string } = $props();
</script>

<section class="state" aria-busy="true">
  <p class="label">{label}</p>
  <p class="muted">{text}</p>
</section>

<style>
  .state {
    padding: 64px var(--gutter);
    max-width: 640px;
  }
  .label {
    margin: 0 0 8px;
  }
  .muted {
    margin: 0;
    color: var(--muted);
  }
</style>
