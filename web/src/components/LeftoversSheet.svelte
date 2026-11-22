<script lang="ts">
  // Leftovers, opened from the status bar: a sheet over the bottom of
  // whatever view is open, listing merged work still on this machine. A
  // non-modal dialog like Catch-up: Escape, Close or a click outside closes
  // it; Tab may leave it, which closes it. The list is for reading: sitboard
  // removes nothing and offers no way to.
  import { onMount, tick } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import type { Leftover, LeftoverKind, LeftoverPart } from '../contract.gen';
  import { heldLine, prHref, verdict, type LeftoversClient } from '../data/leftovers';
  import { LeftoversState } from '../data/leftovers.svelte';
  import { clean, exact } from '../data/text';
  import { clock, since } from '../data/time';
  import { ms, VIEW_MS } from '../motion';
  import type { Refocus } from './About.svelte';

  let {
    id,
    client,
    projects,
    toggle,
    onclose,
    now = Date.now(),
  }: {
    id: string;
    client: LeftoversClient;
    /** project id -> name, as the board shows it. */
    projects: ReadonlyMap<string, string>;
    toggle: HTMLElement | undefined;
    onclose: (refocus: Refocus) => void;
    now?: number;
  } = $props();

  const lo = new LeftoversState(() => client);
  let sheet = $state<HTMLElement>();
  let heading = $state<HTMLElement>();

  // The sheet sits above the status bar, which takes several lines on a
  // phone: it may be as tall as most of the room above the bar.
  let room = $state<number | null>(null);
  function measure() {
    const bar = sheet?.offsetParent as HTMLElement | null;
    if (bar) room = Math.max(200, Math.floor(bar.getBoundingClientRect().top * 0.75));
  }

  onMount(() => {
    measure();
    window.addEventListener('resize', measure);
    const stop = lo.start();
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (sheet?.contains(t) || toggle?.contains(t)) return;
      onclose('stay');
    };
    document.addEventListener('pointerdown', down, true);
    // The sheet and its trigger are one place for focus: it closes when focus
    // leaves both, from whichever of the two it left.
    toggle?.addEventListener('focusout', onfocusout);
    void tick().then(() => heading?.focus());
    return () => {
      stop();
      toggle?.removeEventListener('focusout', onfocusout);
      document.removeEventListener('pointerdown', down, true);
      window.removeEventListener('resize', measure);
    };
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation(); // the action notice listens for Escape too
    onclose('button');
  }

  function onfocusout(e: FocusEvent) {
    const to = e.relatedTarget as Node | null;
    if (!to || sheet?.contains(to) || toggle?.contains(to)) return;
    onclose('stay');
  }

  const data = $derived(lo.data);
  const items = $derived(data?.state === 'ready' ? data.items : []);
  const held = $derived(data?.state === 'ready' ? heldLine(data.held) : '');
  // When the list shown was made: the server's last look, not this load.
  const checked = $derived.by(() => {
    const at = data?.state === 'ready' && data.asOf ? clock(data.asOf) : '';
    if (!at) return '';
    return `checked ${at}${lo.status === 'failed' ? ', could not refresh' : ''}`;
  });

  const KIND: Record<LeftoverKind, string> = { session: 'session', worktree: 'worktree', branch: 'branch', remoteBranch: 'on GitHub' };

  // Paths and branch names are shown exactly (exact()): two different names
  // never look the same. A session's name is a label, cleaned like any other.
  function partText(p: LeftoverPart): string {
    switch (p.kind) {
      case 'session': {
        const idle = since(p.idleSince, now);
        return `${clean(p.name) || 'unnamed agent'}${idle ? ` · idle ${idle}` : ''}`;
      }
      case 'worktree':
        return exact(p.path) || 'a worktree';
      case 'branch':
        return exact(p.branch);
      default:
        return `branch ${exact(p.branch)} still on GitHub`;
    }
  }

  const project = (it: Leftover) => clean(projects.get(it.projectId) ?? '') || 'unknown project';
  // Each row is its own: the same pull request can come from two clones, the
  // same number from two repositories.
  const key = (it: Leftover, i: number) => `${i}|${it.projectId}|${it.pullRequest.repo}#${it.pullRequest.number}`;
</script>

<div
  bind:this={sheet}
  {id}
  class="sheet"
  role="dialog"
  aria-labelledby="{id}-title"
  aria-describedby="{id}-sub"
  tabindex="-1"
  style:max-height={room ? `${room}px` : undefined}
  {onkeydown}
  {onfocusout}
  in:fly={{ y: 12, duration: ms(VIEW_MS), easing: cubicOut }}
  out:fade={{ duration: ms(VIEW_MS) }}
>
  <div class="head">
    <h2 bind:this={heading} id="{id}-title" class="title" tabindex="-1">Leftovers</h2>
    <span id="{id}-sub" class="sub">merged work still on this machine · sitboard removes nothing</span>
    {#if checked}<span class="asof" class:stale={lo.status === 'failed'}>{checked}</span>{/if}
    <button type="button" class="close" onclick={() => onclose('button')}>Close<span class="key" aria-hidden="true">Esc</span></button>
  </div>

  {#if lo.status === 'unauthorized'}
    <p class="msg warn" role="alert">This tab lost its key. Open the link sitboard printed in the terminal again.</p>
  {:else if lo.status === 'failed'}
    <p class="msg warn" role="alert">
      {data ? `Could not load it again: ${lo.error}. This is the list as it was.` : `Could not load leftovers: ${lo.error}.`}
      <button type="button" class="retry" onclick={() => void lo.refresh()}>Try again</button>
    </p>
  {/if}

  <div class="body">
    {#if !data}
      {#if lo.status === 'loading'}<p class="msg">Loading…</p>{/if}
    {:else if data.state === 'off'}
      <p class="msg">Leftovers need GitHub: only a merged pull request is evidence enough. They are off while GitHub is (with <span class="flag">--no-github</span>, or when gh is missing or signed out).</p>
    {:else if data.state === 'noGit'}
      <p class="msg">Leftovers need git, and sitboard cannot find it.</p>
    {:else if data.state === 'loading'}
      <p class="msg">sitboard is still looking. The first look runs a minute after it starts, then every ten minutes.</p>
    {:else}
      {#if items.length === 0}
        <p class="msg">{held || data.partial ? 'Nothing to list.' : 'Nothing left over.'}</p>
      {/if}
      {#each items as it, row (key(it, row))}
        {@const href = prHref(it)}
        {@const ago = since(it.mergedAt, now)}
        <section class="pr" aria-labelledby="{id}-row{row}">
          <h3 class="pr-h" id="{id}-row{row}">
            <span class="proj u">{project(it)}</span>
            {#if href}
              <a class="num" {href} target="_blank" rel="noopener noreferrer"
                >#{it.pullRequest.number}<span class="visually-hidden"> on GitHub, opens in a new tab</span></a
              >
            {:else}
              <span class="num plain">#{it.pullRequest.number}</span>
            {/if}
            <span class="ttl u">{clean(it.pullRequest.title)}</span>
            {#if ago}<span class="when">merged {ago} ago</span>{/if}
          </h3>
          <ul class="parts">
            {#each it.parts as p, i (i)}
              <li class="part">
                <span class="k k-{p.kind}">{KIND[p.kind]}</span>
                <span class="v u">{partText(p)}</span>
                {#if p.kind === 'branch' && p.path}
                  <span class="where">in <span class="u">{exact(p.path)}</span></span>
                {/if}
                {#if p.kind === 'worktree' && p.ignored > 0}
                  <span class="warn-ign"
                    >It holds {p.ignored} git-ignored {p.ignored === 1 ? 'file or folder' : 'files or folders'} (such as .env or node_modules).</span
                  >
                {/if}
              </li>
            {/each}
          </ul>
          <p class="verdict" class:wait={it.waiting}>{verdict(it)}</p>
        </section>
      {/each}
      {#if items.length > 0}
        <p class="held">sitboard sees only your own processes: it cannot tell whether another user of this computer works in one of these folders.</p>
      {/if}
      {#if held}
        <p class="held"><span class="hb">Held back:</span> {held} They still hold work, or sitboard could not be sure.</p>
      {/if}
      {#if data.partial}
        <p class="held">Some repositories could not be checked this time, have more than 100 branches, or GitHub may not have given every pull request: this list may be incomplete.</p>
      {/if}
    {/if}
  </div>
</div>

<style>
  .sheet {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 100%;
    z-index: 3;
    display: flex;
    flex-direction: column;
    max-height: 70dvh;
    background: var(--surface);
    border-top: 1px solid var(--border);
    color: var(--fg-body);
    font-size: var(--text-m);
    text-align: left;
  }
  .sheet:focus,
  .title:focus {
    outline: none;
  }
  .title:focus-visible {
    outline: 2px solid var(--amber);
    outline-offset: 2px;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
    padding: 8px var(--gutter);
    border-bottom: 1px solid var(--border);
  }
  .title {
    margin: 0;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--fg);
  }
  .sub {
    color: var(--muted);
    font-size: var(--text-s);
  }
  .close {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-height: 36px;
    padding: 0 12px;
    border: 1px solid var(--border);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--fg);
  }
  .close:hover {
    background: rgb(221 218 207 / 5%);
  }
  .key {
    padding: 0 4px;
    border: 1px solid var(--border);
    color: var(--muted);
    font-weight: 400;
    letter-spacing: 0.04em;
  }
  .msg {
    margin: 0;
    padding: 12px 0;
    color: var(--muted);
  }
  .msg.warn {
    padding: 10px var(--gutter);
    border-bottom: 1px solid var(--failing-line);
    background: rgb(236 124 124 / 8%);
    color: var(--state-failing);
  }
  .retry {
    margin-left: 6px;
    color: var(--fg);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .flag {
    white-space: nowrap;
    color: var(--fg-body);
  }
  .body {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 0 var(--gutter) 12px;
  }
  .pr {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px 0;
    border-bottom: 1px solid var(--rule);
  }
  .pr-h {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    margin: 0;
    font-size: var(--text-m);
    font-weight: 400;
  }
  .proj {
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--muted);
  }
  .num {
    color: var(--state-done);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .num.plain {
    text-decoration: none;
  }
  .num:hover {
    color: var(--fg);
  }
  .ttl {
    flex: 1 1 160px;
    min-width: 0;
    font-family: var(--font-title);
    color: var(--fg);
    overflow-wrap: anywhere;
  }
  .when {
    font-size: var(--text-s);
    color: var(--muted);
    white-space: nowrap;
  }
  .parts {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .part {
    display: grid;
    grid-template-columns: 84px minmax(0, 1fr);
    gap: 2px 10px;
    align-items: baseline;
    font-size: var(--text-s);
  }
  .k {
    justify-self: start;
    padding: 0 5px;
    border: 1px solid currentColor;
    font-size: var(--text-xs);
    letter-spacing: 0.08em;
    text-transform: uppercase;
    white-space: nowrap;
    color: var(--state-coordinator);
  }
  .k-branch {
    color: var(--state-review);
  }
  .k-remoteBranch {
    color: var(--state-working);
  }
  .k-session {
    color: var(--state-idle);
  }
  .v,
  .where {
    min-width: 0;
    overflow-wrap: anywhere;
    white-space: pre-wrap; /* exact names keep every space */
    color: var(--fg-body);
  }
  .where {
    grid-column: 2;
    color: var(--muted);
  }
  .where .u {
    color: var(--fg-body);
  }
  .asof {
    color: var(--muted);
    font-size: var(--text-s);
  }
  .asof.stale {
    color: var(--amber);
  }
  .warn-ign {
    grid-column: 2;
    color: var(--amber);
  }
  .verdict {
    margin: 0;
    font-size: var(--text-s);
    color: var(--state-working);
  }
  .verdict.wait {
    color: var(--muted);
  }
  .held {
    margin: 0;
    padding: 10px 0 0;
    font-size: var(--text-s);
    color: var(--muted);
  }
  .hb {
    color: var(--fg-body);
  }
  @media (max-width: 559px) {
    .part {
      grid-template-columns: minmax(0, 1fr);
    }
    .warn-ign,
    .where {
      grid-column: 1;
    }
  }
</style>
