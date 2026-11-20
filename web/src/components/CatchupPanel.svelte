<script lang="ts">
  // The Catch-up panel: a non-modal dialog on the right, over whatever view is
  // open. Opening it never marks anything read; only "Caught up" does, up to
  // the newest item shown. Escape, Close or a click outside closes it.
  import { onMount, tick } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import type { CatchupState } from '../data/catchup.svelte';
  import { FOLD_AT, groupCatchup, rowLink, rowWord, type CatchupRow } from '../data/catchup';
  import { clean } from '../data/text';
  import { when } from '../data/time';
  import { ms, VIEW_MS } from '../motion';

  export type Refocus = 'button' | 'stay';

  const WINDOW_MS = 7 * 24 * 60 * 60 * 1000;

  let {
    id,
    cu,
    now,
    onBoard,
    toggle,
    top,
    onclose,
    onshowcard,
  }: {
    id: string;
    cu: CatchupState;
    now: number;
    /** Card ids on the board now: a row links to its card only while it is there. */
    onBoard: ReadonlySet<string>;
    toggle: HTMLElement | undefined;
    /** Where the panel starts, below the header (px). */
    top: number;
    onclose: (refocus: Refocus) => void;
    onshowcard: (cardId: string) => void;
  } = $props();

  let panel = $state<HTMLElement>();
  let heading = $state<HTMLElement>();
  let open = $state(new Set<string>()); // projects shown in full
  let said = $state('');

  const data = $derived(cu.data);
  const groups = $derived(data ? groupCatchup(data.items) : []);
  const count = $derived(data ? data.items.length : 0);
  const total = $derived(data ? data.total : 0);
  // A marker older than the window says nothing: items go back 7 days at most.
  const since = $derived(data?.marker && Date.parse(data.marker) > now - WINDOW_MS ? when(data.marker, now) : '');
  const shownLine = $derived(total > count ? `newest ${count} of ${total}` : count ? String(count) : '');

  onMount(() => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (panel?.contains(t) || toggle?.contains(t)) return;
      onclose('stay');
    };
    document.addEventListener('pointerdown', down, true);
    return () => document.removeEventListener('pointerdown', down, true);
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation(); // the action notice listens for Escape too
    onclose('button');
  }

  // Non-modal, like About: Tab may leave it, and it closes once focus is
  // somewhere other than the panel or its button (on a phone the page behind
  // is covered, so focus there would be lost from sight).
  function onfocusout(e: FocusEvent) {
    const to = e.relatedTarget as Node | null;
    if (!to || panel?.contains(to) || toggle?.contains(to)) return;
    onclose('stay');
  }

  // "Caught up" is disabled once it worked, and "Show more" goes away: focus
  // moves on to the heading, or to the first row it showed, never to the page.
  async function caughtUp() {
    said = '';
    if (await cu.markRead())
      said = data?.unsaved
        ? 'Marked read until sitboard stops. Anything the board learns from now on shows here.'
        : 'Marked read. Anything the board learns from now on shows here.';
    await tick();
    heading?.focus();
  }

  async function expand(projectId: string) {
    open = new Set(open).add(projectId);
    await tick();
    const section = [...(panel?.querySelectorAll<HTMLElement>('[data-project]') ?? [])].find((s) => s.dataset.project === projectId);
    section?.querySelectorAll<HTMLElement>('li')[FOLD_AT]?.focus();
  }

  // A session with no name: one still on the board is an agent; one that has
  // left it (no card) is an ended session. Nothing is guessed.
  const name = (r: CatchupRow) => clean(r.agentName) || (r.cardId ? 'unnamed agent' : 'ended session');
  const what = (r: CatchupRow) => clean(r.title);
</script>

<div
  bind:this={panel}
  {id}
  class="panel"
  style:--top="{top}px"
  role="dialog"
  aria-labelledby="{id}-title"
  aria-describedby="{id}-since"
  tabindex="-1"
  {onkeydown}
  {onfocusout}
  in:fly={{ x: 16, duration: ms(VIEW_MS), easing: cubicOut }}
  out:fade={{ duration: ms(VIEW_MS) }}
>
  <div class="head">
    <h2 bind:this={heading} id="{id}-title" class="title" tabindex="-1">Since you looked</h2>
    <span id="{id}-since" class="since">
      {#if !data}
        &nbsp;
      {:else}
        {since ? `since ${since}` : 'the last 7 days'}{#if shownLine}{' · '}{shownLine}{/if}
      {/if}
    </span>
    <div class="acts">
      <button type="button" class="done" disabled={!count || cu.marking} onclick={caughtUp}>{cu.marking ? 'Saving…' : 'Caught up'}</button>
      <button type="button" class="close" onclick={() => onclose('button')}>Close<span class="key" aria-hidden="true">Esc</span></button>
    </div>
  </div>

  {#if cu.status === 'unauthorized'}
    <p class="msg warn" role="alert">This tab lost its key. Open the link sitboard printed in the terminal again.</p>
  {:else}
    {#if cu.markError}
      <p class="msg warn" role="alert">Could not mark it read: {cu.markError}. Nothing was lost.</p>
    {/if}
    {#if cu.status === 'failed' && cu.loadError}
      <p class="msg warn" role="alert">
        {cu.afterMark
          ? `Marked read, but could not load what came after: ${cu.loadError}.`
          : data
            ? `Could not load it again: ${cu.loadError}. This is the list as it was.`
            : `Could not load catch-up: ${cu.loadError}.`}
        <button type="button" class="retry" onclick={() => void cu.refresh()}>Try again</button>
      </p>
    {/if}
  {/if}

  {#if data?.pending}
    <p class="msg note">Still reading the sessions that ended before sitboard started: more may appear here.</p>
  {/if}
  {#if data?.unsaved}
    <p class="msg note">Nothing here is saved: sitboard runs with <span class="flag">--no-state</span>, so after every start this lists the last 7 days again.</p>
  {/if}

  <div class="body">
    {#if !data}
      {#if cu.status === 'loading'}<p class="msg">Loading…</p>{/if}
    {:else if groups.length === 0}
      <p class="empty">Nothing new {since ? `since ${since}` : 'in the last 7 days'}.</p>
    {:else}
      {#each groups as g (g.projectId)}
        {@const full = open.has(g.projectId) || g.rows.length <= FOLD_AT + 1}
        {@const rows = full ? g.rows : g.rows.slice(0, FOLD_AT)}
        <section class="grp" aria-labelledby="{id}-{g.projectId}" data-project={g.projectId}>
          <h3 id="{id}-{g.projectId}" class="grph"><span class="pname u">{clean(g.name) || 'unknown project'}</span><span class="n">{g.total}</span></h3>
          <ul>
            {#each rows as r (r.key)}
              {@const link = rowLink(r, onBoard)}
              <li class="it" tabindex="-1">
                <span class="at">{when(r.at, now)}</span>
                <span class="k k-{r.kind}">{rowWord(r)}</span>
                <span class="what"><span class="ag u">{name(r)}</span>{#if what(r)}<span class="sep" aria-hidden="true"> · </span><span class="ttl u">{what(r)}</span>{/if}</span>
                <span class="go">
                  {#if link.kind === 'url'}
                    <a href={link.href} class:ext={!!r.pullRequest} target="_blank" rel="noopener noreferrer"
                      >{link.text}<span class="visually-hidden">{r.kind === 'artifact_published' ? ' artifact, opens in a new tab' : ' on GitHub, opens in a new tab'}</span></a
                    >
                  {:else if link.kind === 'card'}
                    <button type="button" class="card" onclick={() => onshowcard(link.cardId)}>Card<span class="visually-hidden">{`: show ${name(r)} on the board`}</span></button>
                  {/if}
                </span>
              </li>
            {/each}
          </ul>
          {#if !full}
            <button type="button" class="more" onclick={() => expand(g.projectId)}>Show {g.rows.length - rows.length} more<span class="visually-hidden">{` in ${clean(g.name) || 'this project'}`}</span></button>
          {/if}
        </section>
      {/each}
    {/if}
  </div>
  <span class="visually-hidden" role="status">{said}</span>
</div>

<style>
  .panel {
    position: fixed;
    top: var(--top);
    right: 0;
    bottom: 0;
    z-index: 4;
    width: min(460px, 100vw);
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border-left: 1px solid var(--border);
    border-top: 1px solid var(--border);
    color: var(--fg-body);
    font-size: var(--text-m);
  }
  .panel:focus,
  .title:focus,
  .it:focus {
    outline: none;
  }
  .title:focus-visible,
  .it:focus-visible {
    outline: 2px solid var(--amber);
    outline-offset: 2px;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
    padding: 12px 16px;
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
  .since {
    color: var(--muted);
  }
  .acts {
    margin-left: auto;
    display: flex;
    gap: 8px;
  }
  .done,
  .close {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-height: 44px;
    padding: 0 14px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
  }
  .done {
    background: var(--amber);
    color: var(--bg);
  }
  .done:hover:not(:disabled) {
    background: var(--amber-hover);
  }
  .done:disabled {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--muted);
    cursor: default;
  }
  .close {
    border: 1px solid var(--border);
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
    padding: 10px 16px;
    color: var(--muted);
  }
  .flag {
    white-space: nowrap;
  }
  .msg.note {
    border-bottom: 1px solid var(--border);
  }
  .msg.warn {
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
  .body {
    flex: 1;
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 4px 16px 16px;
  }
  .empty {
    margin: 24px 0;
    color: var(--muted);
  }
  .grp {
    padding-top: 12px;
  }
  .grph {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 2px;
    font-size: var(--text-xs);
    font-weight: 400;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--muted);
  }
  .grph::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--rule);
  }
  .pname {
    color: var(--fg);
    font-weight: 700;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 70%;
  }
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .it {
    display: grid;
    grid-template-columns: 76px auto minmax(0, 1fr) auto;
    align-items: baseline;
    gap: 10px;
    padding: 6px 0;
    border-bottom: 1px solid var(--rule);
  }
  .it:last-child {
    border-bottom: 0;
  }
  .at {
    color: var(--muted);
    white-space: nowrap;
  }
  .k {
    padding: 0 5px;
    border: 1px solid currentColor;
    font-size: var(--text-xs);
    letter-spacing: 0.08em;
    text-transform: uppercase;
    white-space: nowrap;
  }
  .k-turn_finished {
    color: var(--state-coordinator);
  }
  .k-pr_opened {
    color: var(--state-review);
  }
  .k-pr_merged {
    color: var(--state-done);
  }
  .k-artifact_published {
    color: var(--state-working);
  }
  .what {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .ag {
    color: var(--fg);
  }
  .sep {
    margin: 0 6px;
    color: var(--muted);
  }
  .ttl {
    font-family: var(--font-title);
  }
  .go {
    white-space: nowrap;
  }
  .go a,
  .card {
    display: inline-flex;
    align-items: center;
    min-height: 32px;
    padding: 0 6px;
    color: var(--fg-body);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .go a {
    color: var(--state-review);
  }
  .card:hover,
  .go a:hover {
    color: var(--fg);
  }
  .more {
    min-height: 36px;
    color: var(--amber);
  }
  .more:hover {
    color: var(--amber-hover);
  }
  /* A phone: the panel covers the whole page, header included, so its own Close is the way back. */
  @media (max-width: 559px) {
    .panel {
      top: 0;
      width: 100vw;
      border-left: 0;
      border-top: 0;
    }
    .it {
      display: flex;
      flex-wrap: wrap;
      gap: 2px 8px;
    }
    .what {
      order: 3;
      flex-basis: 100%;
    }
    .go {
      margin-left: auto;
    }
  }
</style>
