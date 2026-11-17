<script lang="ts">
  import type { Snapshot } from '../contract.gen';
  import { SOURCE_LABEL, sourceLine } from '../data/derive';
  import { clean } from '../data/text';
  import { statusVersion } from '../data/about';
  import { tick } from 'svelte';
  import About, { type Refocus } from './About.svelte';

  let { snapshot }: { snapshot: Snapshot | null } = $props();

  const ABOUT_ID = 'about-sitboard';
  let open = $state(false);
  let btn = $state<HTMLButtonElement>();
  const build = $derived(snapshot?.build ?? null);
  const version = $derived(statusVersion(build));

  async function show() {
    open = true;
    // Focus from here, not from the panel's mount: a panel reopened during its
    // closing fade is resumed, not mounted again.
    await tick();
    document.getElementById(ABOUT_ID)?.focus();
  }

  function close(refocus: Refocus) {
    open = false;
    if (refocus !== 'button') return;
    btn?.focus();
    // A click on plain content blurs whatever is focused after this handler; take it back after.
    setTimeout(() => {
      const a = document.activeElement;
      if (!a || a === document.body) btn?.focus();
    });
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      e.preventDefault();
      e.stopPropagation();
      close('button');
    }
  }
</script>

<footer class:raised={open}>
  <button
    bind:this={btn}
    type="button"
    class="me"
    aria-expanded={open}
    aria-controls={open ? ABOUT_ID : undefined}
    onclick={() => (open ? close('button') : show())}
    {onkeydown}
    ><svg aria-hidden="true" width="7" height="8" viewBox="0 0 7 8"><path d="M0 0l7 4-7 4z" fill="#f5c542" /></svg><span
      class="visually-hidden">{'About '}</span
    ><span class="name">sitboard{#if version}{' '}<span class="ver">{version}</span>{/if}</span><span
      class="caret"
      aria-hidden="true"
    ></span></button
  >
  {#if open}
    <About id={ABOUT_ID} {build} toggle={btn} onclose={close} />
  {/if}
  <span>local only</span>
  {#if snapshot}
    <ul class="sources" aria-label="Sources">
      {#each snapshot.sources as src (src.kind)}
        {@const l = sourceLine(src, snapshot)}
        <li title={clean(src.version) ? `${SOURCE_LABEL[src.kind]} ${clean(src.version)}${src.tested ? '' : ', not a tested version'}` : undefined}>
          {SOURCE_LABEL[src.kind]} <span class="v tone-{l.tone} u">{l.text}</span>
        </li>
      {/each}
      {#if snapshot.plainShells > 0}
        <li><span class="v">{snapshot.plainShells}</span> plain {snapshot.plainShells === 1 ? 'shell' : 'shells'} hidden</li>
      {/if}
    </ul>
  {/if}
</footer>

<style>
  footer {
    position: sticky;
    bottom: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 18px;
    min-height: 34px;
    padding: 6px var(--gutter);
    background: var(--surface);
    border-top: 1px solid var(--border);
    font-size: var(--text-xs);
    color: var(--muted);
    z-index: 2;
  }
  /* Open About sits above the action notice (z-index 3), which is fixed to the page. */
  footer.raised {
    z-index: 4;
  }
  .me {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-height: 24px;
    margin: -2px -6px;
    padding: 2px 6px;
    transition:
      background-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .me:hover,
  .me[aria-expanded='true'] {
    background: rgb(221 218 207 / 6%);
  }
  .me:active {
    transform: translateY(1px);
  }
  .name {
    color: var(--fg);
  }
  .ver {
    color: var(--muted);
  }
  .caret {
    display: inline-block;
    width: 6px;
    height: 12px;
    margin-left: -4px;
    background: var(--amber);
    animation: blink 1.1s steps(1, end) infinite;
  }
  @keyframes blink {
    50% {
      opacity: 0;
    }
  }
  .sources {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 18px;
    margin: 0 0 0 auto;
    padding: 0;
    list-style: none;
  }
  .v {
    color: var(--fg);
  }
  .v.tone-muted {
    color: var(--muted);
  }
  .v.tone-failing {
    color: var(--state-failing);
  }
</style>
