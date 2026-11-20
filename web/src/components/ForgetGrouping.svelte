<script lang="ts">
  // "Forget grouping" for a project that groups parts. Two steps, so it is
  // never done by accident: the button opens a confirm, and Keep sits where
  // the button was and takes the focus, so a second click or Enter keeps the
  // grouping. Escape keeps it too. Keep looks like the suggested answer (the
  // primary style, the focus ring shown); Forget once and Never group are
  // plain bordered buttons. Forget once lets the folder group again later;
  // Never group sets it to never (undone from the status bar's list).
  // In the tiles' parts row the confirm is right-aligned like the button, and
  // Keep is at least as wide as the button was, so Forget is never under the
  // pointer of a double click.
  import { tick } from 'svelte';
  import { onKeys } from '../keys';

  let {
    name,
    onforget,
    onnever,
    compact = false,
    dataKey,
    tabindex = 0,
    asking = $bindable(false),
  }: {
    name: string;
    /** Resolves true when the server forgot the grouping. */
    onforget: () => Promise<boolean>;
    /** Resolves true when the server set the folder to never group. */
    onnever?: () => Promise<boolean>;
    /** In the rail: the confirm opens below the button, Keep first. */
    compact?: boolean;
    dataKey?: string;
    tabindex?: number;
    /** True while the confirm is open (the tiles hide Always group then). */
    asking?: boolean;
  } = $props();

  const uid = $props.id();
  let step = $state<'idle' | 'confirm' | 'busy'>('idle');
  let width = $state(0); // the button's width when it was pressed
  let trigger = $state<HTMLButtonElement>();
  let keepBtn = $state<HTMLButtonElement>();
  $effect(() => {
    asking = step !== 'idle';
  });

  async function open() {
    width = Math.ceil(trigger?.getBoundingClientRect().width ?? 0);
    step = 'confirm';
    await tick();
    keepBtn?.focus();
  }

  async function keep() {
    if (step === 'busy') return;
    step = 'idle';
    await tick();
    trigger?.focus();
  }

  // While the request is out both buttons say they are unavailable
  // (aria-disabled, not disabled: a disabled button would drop the focus).
  async function forget(act: () => Promise<boolean> = onforget) {
    if (step === 'busy') return;
    step = 'busy';
    const ok = await act();
    // On success the grouping leaves the board with the next snapshot, and
    // this control with it; until then it stays quiet.
    if (!ok) {
      step = 'confirm';
      await keep();
    }
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape' || step !== 'confirm') return;
    e.stopPropagation();
    void keep();
  }
</script>

{#if step === 'idle'}
  <button bind:this={trigger} type="button" class="trigger" class:compact data-key={dataKey} {tabindex} onclick={open}>Forget grouping</button>
{:else}
  <div class="confirm" class:compact role="group" aria-label="Forget the grouping of {name}" aria-describedby="{uid}-q" {@attach onKeys(onkeydown)}>
    <p id="{uid}-q">
      Forget the grouping of <span class="u">{name}</span>? Its parts are no longer grouped under it.{#if onnever}{' '}Forget once lets it group
        again later; Never group keeps them apart.{/if}
    </p>
    <div class="acts">
      {#if compact}
        <button bind:this={keepBtn} type="button" class="act keep" aria-disabled={step === 'busy'} onclick={keep}>Keep</button>
        <button type="button" class="act" aria-disabled={step === 'busy'} onclick={() => forget()}>Forget once</button>
        {#if onnever}<button type="button" class="act" aria-disabled={step === 'busy'} onclick={() => forget(onnever)}>Never group</button>{/if}
      {:else}
        {#if onnever}<button type="button" class="act" aria-disabled={step === 'busy'} onclick={() => forget(onnever)}>Never group</button>{/if}
        <button type="button" class="act" aria-disabled={step === 'busy'} onclick={() => forget()}>Forget once</button>
        <button bind:this={keepBtn} type="button" class="act keep" style:min-width={width ? `${width}px` : undefined} aria-disabled={step === 'busy'} onclick={keep}
          >Keep</button
        >
      {/if}
    </div>
  </div>
{/if}

<style>
  .trigger,
  .act {
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
  .trigger:hover,
  .act:hover {
    color: var(--fg);
    border-color: var(--border-hover);
  }
  .trigger:active,
  .act:active {
    transform: translateY(1px);
  }
  .trigger:focus-visible {
    outline-offset: -2px;
  }
  .trigger.compact {
    display: flex;
    align-items: center;
    width: 100%;
    min-height: 36px;
    padding: 0 16px 0 40px;
    font-size: var(--text-xs);
    border: 0;
  }
  .trigger.compact:hover {
    background: rgb(221 218 207 / 4%);
  }
  /* The answers stay on the button's line at every width: when the
     question does not fit beside them it wraps below them (wrap-reverse
     puts the first line at the bottom), and they are top-aligned
     (flex-end is the top in wrap-reverse). */
  .confirm {
    display: flex;
    align-items: flex-end;
    flex-wrap: wrap-reverse;
    justify-content: flex-end;
    gap: 6px 10px;
    min-width: 0;
  }
  .confirm.compact p {
    padding-top: 0;
  }
  .confirm.compact {
    flex-direction: column;
    flex-wrap: nowrap;
    align-items: stretch;
    padding: 8px 16px 10px 40px;
    background: var(--amber-soft);
  }
  p {
    margin: 0;
    padding-top: 8px;
    min-width: 0;
    font-size: var(--text-s);
    color: var(--fg-body);
    overflow-wrap: anywhere;
  }
  .u {
    color: var(--fg);
  }
  /* Keep is last: when the answers wrap, wrap-reverse keeps it on the top
     row, at the right, where the button was. */
  .acts {
    display: flex;
    flex-wrap: wrap-reverse;
    justify-content: flex-end;
    gap: 6px;
  }
  .confirm.compact .acts {
    flex-wrap: wrap;
    justify-content: flex-start;
  }
  .act {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-color: var(--border);
    color: var(--fg-body);
  }
  /* The suggested answer: the page's primary button (as in the action notice). */
  .act.keep {
    background: var(--amber);
    border-color: var(--amber);
    color: var(--bg);
  }
  /* The page's focus ring, shown however the confirm was opened (a pointer
     click too): it says where Enter goes. */
  .act.keep:focus {
    outline: 2px solid var(--amber);
    outline-offset: 2px;
  }
  .act.keep:hover {
    background: var(--amber-hover);
    border-color: var(--amber-hover);
    color: var(--bg);
  }
  .act[aria-disabled='true'] {
    opacity: 0.6;
    cursor: progress;
  }
</style>
