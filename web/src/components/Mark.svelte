<script lang="ts">
  // No props: the mark is fixed artwork, 18px, sized so every edge falls on a whole pixel.
</script>

<!-- The Cards mark: a small board, one card amber. Decorative; the wordmark beside it names the app. -->
<!-- 18px at a 0.8 scale: 4px squares, 2px gaps, every edge on a whole pixel. -->
<svg class="mark" aria-hidden="true" width="18" height="18" viewBox="4.75 4.75 22.5 22.5"
  ><rect class="a" x="6" y="6" width="5" height="5" /><rect class="c" x="6" y="13.5" width="5" height="5" /><rect
    class="c"
    x="6"
    y="21"
    width="5"
    height="5"
  /><rect class="c" x="13.5" y="6" width="5" height="5" /><rect class="c" x="13.5" y="13.5" width="5" height="5" /><rect
    class="c"
    x="21"
    y="6"
    width="5"
    height="5"
  /></svg
>

<style>
  .mark {
    display: block;
    flex: none;
  }
  .a {
    fill: var(--amber);
  }
  .c {
    fill: var(--fg);
  }
</style>
