<script lang="ts">
  // "Always group" for a grouping on auto, "Back to auto" for one set to
  // always. One click, no confirm: either is undone right here. It stays one
  // button whichever it says, so the focus stays on it when the next
  // snapshot changes the setting. Set to always, a label says so and
  // describes the button.
  import type { GroupingMode } from '../contract.gen';

  let {
    mode,
    onset,
    compact = false,
    dataKey,
    tabindex = 0,
  }: {
    /** The grouping's setting now: auto or always (a folder set to never is no grouping). */
    mode: GroupingMode;
    /** Sets the folder's grouping; resolves true when the server did. */
    onset: (mode: GroupingMode) => Promise<boolean>;
    /** In the rail: a full-width row. */
    compact?: boolean;
    dataKey?: string;
    tabindex?: number;
  } = $props();

  const uid = $props.id();
  let busy = $state(false);
  const always = $derived(mode === 'always');

  // A second press while one is out gets the same answer from the store.
  async function press() {
    busy = true;
    await onset(always ? 'auto' : 'always');
    busy = false;
  }
</script>

<div class="always" class:compact>
  {#if always}<span id="{uid}-l" class="label"><span class:visually-hidden={compact}>{'Grouping '}</span><b>always</b></span>{/if}
  <button
    type="button"
    class="trigger"
    data-key={dataKey}
    {tabindex}
    aria-describedby={always ? `${uid}-l` : undefined}
    aria-disabled={busy}
    onclick={press}>{always ? 'Back to auto' : 'Always group'}</button
  >
</div>

<style>
  .always {
    display: flex;
    align-items: center;
    gap: 2px 10px;
  }
  .label {
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    white-space: nowrap;
    color: var(--muted);
  }
  .label b {
    font-weight: 700;
    color: var(--fg);
  }
  .trigger {
    min-height: 32px;
    padding: 0 10px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    white-space: nowrap;
    color: var(--muted);
    border: 1px solid transparent;
    transition:
      border-color var(--press) ease-out,
      color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .trigger:hover {
    color: var(--fg);
    border-color: var(--border-hover);
  }
  .trigger:active {
    transform: translateY(1px);
  }
  .trigger:focus-visible {
    outline-offset: -2px;
  }
  .trigger[aria-disabled='true'] {
    opacity: 0.6;
    cursor: progress;
  }
  /* The rail: a row like "Forget grouping" below it, the label at the row's start. */
  .compact {
    flex-wrap: wrap;
    gap: 0 10px;
    padding-left: 40px;
  }
  .compact .trigger {
    display: flex;
    align-items: center;
    min-height: 36px;
    padding: 0 16px 0 0;
    border: 0;
  }
  .compact:not(:has(.label)) .trigger {
    flex: 1;
  }
  .compact .trigger:hover {
    background: rgb(221 218 207 / 4%);
  }
</style>
