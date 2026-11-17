<script lang="ts">
  // The About panel: a non-modal dialog above the status bar's left end.
  // Escape or a click outside closes it; Tab stays inside while it is open.
  import { onMount } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import Mark from './Mark.svelte';
  import type { Build } from '../contract.gen';
  import { LINKS, MORE_LINKS, UPDATE_COMMAND, commitText, platformText, versionText } from '../data/about';
  import { ms, VIEW_MS } from '../motion';

  /** Where focus goes on close: back to the button, or left where it is. */
  export type Refocus = 'button' | 'stay';

  let {
    id,
    build,
    toggle,
    onclose,
  }: {
    id: string;
    build: Build | null;
    toggle: HTMLElement | undefined;
    onclose: (refocus: Refocus) => void;
  } = $props();

  const COPY_MS = 2500;
  const CONTROL = 'button, a[href], input, select, textarea, summary, [tabindex], [contenteditable="true"]';

  let panel = $state<HTMLElement>();
  let code = $state<HTMLElement>();
  let copy = $state<'idle' | 'copied' | 'selected'>('idle');
  let timer: ReturnType<typeof setTimeout> | undefined;

  onMount(() => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (panel?.contains(t) || toggle?.contains(t)) return;
      // A control keeps (or, in Safari, never takes) its own focus; a click on
      // anything else would leave focus nowhere, so the button takes it back.
      const control = t instanceof Element && t.closest(CONTROL);
      onclose(control ? 'stay' : 'button');
    };
    document.addEventListener('pointerdown', down, true);
    return () => {
      document.removeEventListener('pointerdown', down, true);
      clearTimeout(timer);
    };
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation(); // the action notice listens for Escape too
    onclose('button');
  }

  // A non-modal disclosure: Tab may leave it, and it closes once focus is
  // somewhere other than the panel or its button. No relatedTarget means the
  // window lost focus or the click handler above decides; nothing to do here.
  function onfocusout(e: FocusEvent) {
    const to = e.relatedTarget as Node | null;
    if (!to || panel?.contains(to) || toggle?.contains(to)) return;
    onclose('stay');
  }

  async function copyCommand() {
    clearTimeout(timer);
    try {
      if (!navigator.clipboard) throw new Error('no clipboard');
      await navigator.clipboard.writeText(UPDATE_COMMAND);
      copy = 'copied';
    } catch {
      // Refused or unavailable: select the command so the keyboard can copy it.
      if (code) window.getSelection()?.selectAllChildren(code);
      copy = 'selected';
    }
    timer = setTimeout(() => (copy = 'idle'), COPY_MS);
  }

  const copyWord = $derived(copy === 'copied' ? 'Copied' : copy === 'selected' ? 'Selected' : 'Copy');
  const copyStatus = $derived(
    copy === 'copied' ? 'Copied to the clipboard' : copy === 'selected' ? 'Selected; copy it with your keyboard' : '',
  );
</script>

<div
  bind:this={panel}
  {id}
  class="about"
  role="dialog"
  aria-labelledby="{id}-title"
  tabindex="-1"
  {onkeydown}
  {onfocusout}
  in:fly={{ y: 6, duration: ms(VIEW_MS), easing: cubicOut }}
  out:fade={{ duration: ms(VIEW_MS) }}
>
  <div class="top">
    <Mark />
    <h2 id="{id}-title" class="word"><span class="visually-hidden">{'About '}</span>sit<span class="accent">board</span></h2>
  </div>

  {#if build}
    <dl class="build">
      <div><dt>Version</dt> <dd class="u">{versionText(build)}</dd></div>
      <div><dt>Commit</dt> <dd class="u">{commitText(build.commit)}</dd></div>
      <div><dt>Platform</dt> <dd class="u">{platformText(build.os, build.arch)}</dd></div>
    </dl>
  {/if}

  <p class="privacy">Runs only on this machine. Nothing is sent anywhere; GitHub is read through your own gh.</p>

  <section aria-labelledby="{id}-update">
    <h3 id="{id}-update">Update</h3>
    <div class="cmd">
      <code bind:this={code}>{UPDATE_COMMAND}</code>
      <button type="button" class="copy" class:copied={copy === 'copied'} onclick={copyCommand}>{copyWord}</button>
    </div>
    <span class="visually-hidden" role="status">{copyStatus}</span>
  </section>

  <dl class="links">
    {#each LINKS as l (l.href)}
      <div><dt>{l.label}</dt> <dd><a href={l.href} target="_blank" rel="noopener noreferrer">{l.text}</a></dd></div>
    {/each}
  </dl>
  <p class="more">
    {#each MORE_LINKS as l, i (l.href)}{#if i}<span class="sep" aria-hidden="true">·</span>{/if}<a
        href={l.href}
        target="_blank"
        rel="noopener noreferrer">{l.text}</a
      >{/each}
  </p>
</div>

<style>
  .about {
    position: absolute;
    left: var(--gutter);
    bottom: calc(100% + 8px);
    z-index: 3;
    width: min(360px, calc(100vw - 2 * var(--gutter)));
    max-height: calc(100dvh - 120px);
    overflow-y: auto;
    padding: 16px 16px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: var(--text-m);
    color: var(--fg-body);
  }
  /* Focus lands on the panel itself when it opens; it is not a control, so no ring. */
  .about:focus {
    outline: none;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 14px;
  }
  .word {
    margin: 0;
    font-size: var(--text-lead);
    font-weight: 500;
    letter-spacing: 0.02em;
    color: var(--fg);
  }
  .accent {
    color: var(--amber);
  }
  dl {
    display: grid;
    gap: 6px;
    margin: 0;
  }
  dl > div {
    display: grid;
    grid-template-columns: 76px minmax(0, 1fr);
    align-items: baseline;
    gap: 12px;
  }
  dt,
  h3 {
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--muted);
  }
  dd {
    margin: 0;
    color: var(--fg);
    overflow-wrap: anywhere;
  }
  .privacy {
    margin: 14px 0;
    padding: 10px 0;
    border-top: 1px solid var(--rule);
    border-bottom: 1px solid var(--rule);
    line-height: 1.5;
  }
  h3 {
    margin: 0 0 6px;
  }
  .cmd {
    display: flex;
    border: 1px solid var(--border);
    background: var(--bg);
  }
  code {
    flex: 1;
    min-width: 0;
    padding: 8px 10px;
    font-family: var(--font-ui);
    color: var(--fg);
    overflow-wrap: anywhere;
  }
  .copy {
    flex: none;
    min-width: 76px;
    min-height: 32px;
    padding: 0 12px;
    border-left: 1px solid var(--border);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    text-align: center;
    color: var(--fg);
    transition:
      background-color var(--press) ease-out,
      color var(--press) ease-out;
  }
  .copy:hover {
    background: rgb(221 218 207 / 6%);
  }
  .copy.copied {
    color: var(--state-working);
  }
  .copy:focus-visible {
    outline-offset: -2px;
  }
  .links {
    margin-top: 14px;
  }
  a {
    color: var(--amber);
    text-decoration: none;
    overflow-wrap: anywhere;
  }
  a:hover {
    color: var(--amber-hover);
    text-decoration: underline;
    text-underline-offset: 3px;
  }
  .more {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 8px;
    margin: 12px 0 0;
  }
  .sep {
    color: var(--muted);
  }
</style>
