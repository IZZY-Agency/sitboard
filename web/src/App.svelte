<script lang="ts">
  import { onMount } from 'svelte';
  import { fade } from 'svelte/transition';
  import type { Card } from './contract.gen';
  import type { Actions } from './data/actions';
  import type { Feed } from './data/client';
  import { indexOf, sourcesReady } from './data/derive';
  import { ALL, countsOf, filterCards, grouping as groupingOf, sameSel, selName, validSel, type Sel } from './data/groups';
  import { clock } from './data/time';
  import { BoardState } from './data/store.svelte';
  import { loadGrouping, loadView, saveGrouping, saveView, type Grouping, type View } from './data/view';
  import { ms, VIEW_MS } from './motion';
  import Header from './components/Header.svelte';
  import StatusBar from './components/StatusBar.svelte';
  import ActionNotice from './components/ActionNotice.svelte';
  import ProjectRail from './components/ProjectRail.svelte';
  import ProjectTiles from './components/ProjectTiles.svelte';
  import Loading from './states/Loading.svelte';
  import Empty from './states/Empty.svelte';
  import ErrorState from './states/ErrorState.svelte';
  import Board from './views/Board.svelte';
  import Lanes from './views/Lanes.svelte';
  import Teams from './views/Teams.svelte';

  let { feed, actions, tick = 10_000 }: { feed: Feed; actions: Actions; tick?: number } = $props();

  const board = new BoardState(() => actions);
  let view = $state<View>(loadView());
  let now = $state(Date.now());

  onMount(() => {
    const stop = board.start(feed);
    const clock = setInterval(() => (now = Date.now()), tick);
    return () => {
      stop();
      clearInterval(clock);
    };
  });

  // The retry countdown needs a faster clock while it is shown.
  $effect(() => {
    if (board.conn !== 'unreachable') return;
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  const snap = $derived(board.snapshot);
  // While the connection is down the board is shown as it was, and time stops with it.
  const frozen = $derived(board.lostAt !== null && board.conn !== 'live');
  const viewNow = $derived(frozen ? (board.lostAt as number) : now);
  const announce = $derived.by(() => {
    const n = board.notice;
    if (!n) return '';
    if (n.kind === 'copy')
      return `${n.why === 'no_agterm' ? 'agterm not found' : 'Not in agterm'}. Folder ${n.folder || 'unknown'}. Session ${n.name || 'unnamed'}.`;
    return `${n.what} failed: ${n.message}.`;
  });
  const said = $derived(announce || board.said);
  const onforget = (p: { id: string; name: string }) => board.forgetGrouping(p);
  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && board.notice) board.dismiss();
  }
  const ix = $derived(snap ? indexOf(snap) : null);
  // Header counts stay totals: top-level projects, whatever is selected.
  // The grouping is worked out once per snapshot and shared by everything below.
  const grp = $derived(snap ? groupingOf(snap) : null);
  const projects = $derived(grp ? grp.tops.length : null);

  // One project selection for all three views; how it is shown is a per-viewer choice.
  let grouping = $state<Grouping>(loadGrouping());
  let picked = $state<Sel>(ALL);
  const sel = $derived(grp ? validSel(grp, picked) : ALL);
  const shown = $derived.by(() => {
    if (!snap || !grp || sel.kind === 'all') return snap;
    const cards = [...filterCards(grp, sel)];
    return { ...snap, cards, counts: countsOf(cards) };
  });
  const scope = $derived(grp && sel.kind !== 'all' ? selName(grp, sel) : 'All projects');
  function select(s: Sel) {
    picked = sameSel(s, sel) && s.kind !== 'all' ? ALL : s;
  }
  function setGrouping(g: Grouping) {
    grouping = g;
    saveGrouping(g);
  }

  function setView(v: View) {
    view = v;
    saveView(v);
  }
  const onjump = (c: Card) => void board.jump(c);
  const onseen = (c: Card) => void board.seen(c);
</script>

<svelte:window {onkeydown} />

<div class="page">
  <Header {view} onview={setView} agents={snap ? snap.cards.length : null} {projects} conn={board.conn} generatedAt={snap?.generatedAt ?? null} />

  {#if frozen && snap && board.conn !== 'unauthorized' && board.conn !== 'outdated'}
    <p class="lost" role="alert">
      Connection lost · board as of {clock(new Date(board.lostAt as number).toISOString())} · retrying
    </p>
  {/if}

  <div class="content" class:frozen>
    {#if board.conn === 'unauthorized' || board.conn === 'outdated'}
      <ErrorState kind={board.conn} {now} />
    {:else if !snap || !ix || !grp}
      {#if board.conn === 'unreachable'}
        <ErrorState kind="unreachable" retryAt={board.retryAt} {now} />
      {:else}
        <Loading />
      {/if}
    {:else if snap.cards.length === 0 && !sourcesReady(snap)}
      <Loading label="First scan" text="Reading sessions…" />
    {:else if snap.cards.length === 0}
      <Empty snapshot={snap} />
    {:else}
      <div class="frame" class:railed={grouping === 'rail'}>
        {#key grouping}
          <div class="grouping" in:fade={{ duration: ms(VIEW_MS) }}>
            {#if grouping === 'tiles'}
              <ProjectTiles g={grp} {sel} onselect={select} mode={grouping} onmode={setGrouping} {onforget} />
            {:else}
              <ProjectRail g={grp} {sel} onselect={select} mode={grouping} onmode={setGrouping} {onforget} />
            {/if}
          </div>
        {/key}
        <div class="stack">
          {#key view}
            <div class="view" in:fade={{ duration: ms(VIEW_MS) }} out:fade={{ duration: ms(VIEW_MS) }}>
              {#if view === 'board'}
                <Board snapshot={shown ?? snap} {ix} now={viewNow} arrivals={board.arrivals} {onjump} {onseen} />
              {:else if view === 'lanes'}
                <Lanes snapshot={shown ?? snap} {ix} {scope} now={viewNow} arrivals={board.arrivals} {onjump} {onseen} />
              {:else}
                <Teams snapshot={shown ?? snap} {ix} now={viewNow} arrivals={board.arrivals} {onjump} {onseen} />
              {/if}
            </div>
          {/key}
        </div>
      </div>
    {/if}
  </div>

  <StatusBar snapshot={board.conn === 'unauthorized' ? null : snap} />

  <div class="visually-hidden" role="status" aria-live="polite">{said}</div>
  {#if board.notice}
    <ActionNotice notice={board.notice} ondismiss={() => board.dismiss()} />
  {/if}
</div>

<style>
  .page {
    min-height: 100vh;
    display: flex;
    flex-direction: column;
  }
  .content {
    flex: 1;
    min-width: 0;
    transition:
      opacity var(--press) ease-out,
      filter var(--press) ease-out;
  }
  /* Not live: grey and slightly dimmed, still above 4.5:1 for muted text. */
  .content.frozen {
    opacity: 0.85;
    filter: grayscale(1);
  }
  .lost {
    margin: 0;
    padding: 8px var(--gutter);
    border-bottom: 1px solid var(--failing-line);
    background: rgb(236 124 124 / 8%);
    color: var(--state-failing);
    font-size: var(--text-m);
    font-weight: 700;
    letter-spacing: 0.06em;
  }
  /* Tiles sit above the view; the rail sits beside it and the view takes the rest. */
  .frame {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .frame.railed {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: stretch;
  }
  /* The rail's column carries its surface the full height; the rail inside sticks. */
  .railed .grouping {
    display: flex;
    background: var(--surface);
    border-right: 1px solid var(--border);
  }
  @media (max-width: 899px) {
    .frame.railed {
      display: flex;
      flex-direction: column;
    }
    .railed .grouping {
      background: none;
      border-right: 0;
    }
  }
  /* During a view switch both views share one cell, so they cross-fade in place. */
  .stack {
    display: grid;
  }
  .view {
    grid-area: 1 / 1;
    min-width: 0;
  }
</style>
