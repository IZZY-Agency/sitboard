<script lang="ts">
  // One dot per agent in its column's colour. A dot that changes column
  // cross-fades its colour; dots arrive with a small rise and leave with a fade.
  import { fade, fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import { columnTone } from '../data/derive';
  import type { Dot } from '../data/groups';
  import { COUNT_MS, ms } from '../motion';

  let { dots, more = 0, small = false }: { dots: Dot[]; more?: number; small?: boolean } = $props();
</script>

<span class="dots" class:small aria-hidden="true"
  >{#each dots as d (d.id)}<span
      class="dot pip tone-{columnTone(d.column)}"
      in:fly={{ y: 4, duration: ms(COUNT_MS), easing: cubicOut }}
      out:fade={{ duration: ms(COUNT_MS) }}
    ></span>{/each}{#if more}<span class="more">+{more}</span>{/if}</span
>

<style>
  .dots {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    min-width: 0;
  }
  .small {
    flex-wrap: nowrap;
    gap: 3px;
  }
  .pip {
    transition: background-color 180ms ease-out;
  }
  .small .pip {
    width: 6px;
    height: 6px;
  }
  .more {
    font-size: var(--text-xs);
    line-height: 1;
    color: var(--muted);
  }
</style>
