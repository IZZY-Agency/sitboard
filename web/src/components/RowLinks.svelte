<script lang="ts">
  // A row line ("#219 open · checks passing · closes #88") with each number a
  // link to its own pull or merge request or issue. Rows in Lanes and Teams put it
  // beside their jump button, never inside it: a link cannot sit in a button.
  // The row's button stretches its hit area over the whole row; the links are
  // raised above that area, each with a larger hit area than its text, so a
  // click on a number opens it and a click anywhere else on the row jumps.
  import type { RowPart } from '../data/derive';
  import { requestLinkHint } from '../data/words';

  let { parts, keep = false }: { parts: readonly RowPart[]; keep?: boolean } = $props();

  const hint = { pr: requestLinkHint('github'), mr: requestLinkHint('gitlab'), issue: ' (issue, opens in a new tab)' } as const;
</script>

{#each parts as part, i (i)}{#if i}{' · '}{/if}<span class="part" class:keep class:closes={part.closes}
    >{#each part.bits as b, j (j)}{#if b.href && b.kind}<a class="lk" href={b.href} target="_blank" rel="noopener noreferrer"
          ><span class="num ext u">{b.text}</span><span class="visually-hidden">{hint[b.kind]}</span></a
        >{:else}<span class="u">{b.text}</span>{/if}{/each}</span
  >{/each}

<style>
  /* In a table cell each part stays whole and the line wraps between parts. */
  .keep {
    white-space: nowrap;
  }
  .closes {
    color: var(--muted);
  }
  /* Above the row's stretched jump area (z-index), with a hit area 5px taller
     and 3px wider each way than the number (padding, taken back by the
     margin so the text does not move). */
  .lk {
    position: relative;
    z-index: 1;
    padding: 5px 3px;
    margin: -5px -3px;
    color: inherit;
    text-decoration: none;
  }
  .closes .lk:hover {
    color: var(--fg-body);
  }
  /* The link look of the Board: the number underlined at 70% of its tone,
     the arrow after it (.ext in base.css), both full strength on hover. The
     arrow never wraps away from its number in a narrow lane cell. */
  .num {
    white-space: nowrap;
    text-decoration: underline;
    text-decoration-thickness: 1px;
    text-underline-offset: 3px;
    text-decoration-color: color-mix(in srgb, currentColor 70%, transparent);
  }
  .lk:hover .num,
  .lk:focus-visible .num {
    text-decoration-color: currentColor;
  }
</style>
