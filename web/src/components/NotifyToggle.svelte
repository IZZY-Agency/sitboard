<script lang="ts">
  // The notification switch in the header, beside the live mark. It is off
  // until the owner turns it on; only his click asks the browser. What a click
  // comes to is said by the page's own live region, not here.
  import type { SwitchState } from '../data/notify';

  let { state, ontoggle }: { state: SwitchState; ontoggle: () => void } = $props();

  const WORD: Record<SwitchState, string> = {
    off: 'off',
    asking: 'asking…',
    on: 'on',
    denied: 'blocked',
    none: 'unavailable',
  };
  // The one hint, shown on hover and read as the button's description.
  const HINT: Record<SwitchState, string> = {
    off: 'A desktop notification when an agent starts needing you. The browser asks once.',
    asking: 'The browser is asking whether this page may show notifications.',
    on: 'A desktop notification when an agent starts needing you, while no board tab is in front. Remembered in this browser for this address.',
    denied: 'The browser blocks notifications for this page. Allow them in its site settings for this address, then click again.',
    none: 'This browser cannot show notifications. The tab title and icon still show how many agents need you.',
  };
  // Never `disabled`: a disabled button drops keyboard focus to the page.
  const inert = $derived(state === 'asking' || state === 'none');
</script>

<button type="button" class="notify {state}" aria-disabled={inert ? 'true' : undefined} title={HINT[state]} onclick={ontoggle}>
  <svg viewBox="0 0 12 12" width="12" height="12" aria-hidden="true"
    ><path d="M6 1.5a3 3 0 0 0-3 3V7L2 8.5h8L9 7V4.5a3 3 0 0 0-3-3zM5 9.5a1 1 0 0 0 2 0" /></svg
  >
  <span class="word">Notify</span>
  <span class="state">{WORD[state]}</span>
</button>

<style>
  .notify {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    min-height: 44px;
    padding: 0 12px;
    border: 1px solid var(--border);
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    white-space: nowrap;
    color: var(--fg-body);
    transition:
      background-color var(--press) ease-out,
      border-color var(--press) ease-out,
      color var(--press) ease-out;
  }
  /* Hover lightens the ground only: the state keeps its colour. */
  .notify:hover:not([aria-disabled]) {
    background: rgb(221 218 207 / 5%);
  }
  .notify:active:not([aria-disabled]) {
    transform: translateY(1px);
  }
  .off:hover {
    color: var(--fg);
    border-color: var(--border-hover);
  }
  svg path {
    fill: none;
    stroke: currentColor;
    stroke-width: 1.1;
  }
  .state {
    font-weight: 700;
  }
  .on {
    border-color: var(--amber-line);
    color: var(--amber);
  }
  .on:hover {
    color: var(--amber-hover);
    border-color: var(--amber);
  }
  .denied {
    border-color: var(--failing-line);
    color: var(--state-failing);
  }
  .denied:hover {
    border-color: var(--state-failing);
  }
  .asking,
  .none {
    border-style: dashed;
    color: var(--muted);
    cursor: default;
  }
  /* On a phone the state word is enough beside the bell; the full name stays for screen readers. */
  @media (max-width: 559px) {
    .word {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
    }
  }
</style>
