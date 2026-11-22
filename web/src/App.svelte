<script lang="ts">
  import { onMount, setContext, tick, untrack } from 'svelte';
  import { fade } from 'svelte/transition';
  import type { Card, GroupingMode, GroupingSetting } from './contract.gen';
  import type { Actions } from './data/actions';
  import type { Feed } from './data/client';
  import { indexOf, sourcesReady } from './data/derive';
  import { ALL, countsOf, filterCards, grouping as groupingOf, sameSel, selName, validSel, type Sel } from './data/groups';
  import { clock } from './data/time';
  import { BoardState } from './data/store.svelte';
  import { iconVersion, showSignal, tabSignal, warmLostIcon } from './data/nudge';
  import { browserPermission, NotifySwitch, OUTCOME_TEXT, startNotifier } from './data/notifier.svelte';
  import type { Permission } from './data/notify';
  import { MOVES } from './data/critter';
  import { loadGrouping, loadView, saveGrouping, saveView, type Grouping, type View } from './data/view';
  import { flash, ms, VIEW_MS } from './motion';
  import { createCatchupClient, type CatchupClient } from './data/catchup';
  import { CatchupState } from './data/catchup.svelte';
  import { createLeftoversClient, type LeftoversClient } from './data/leftovers';
  import CatchupButton from './components/CatchupButton.svelte';
  import CatchupPanel, { type Refocus } from './components/CatchupPanel.svelte';
  import SpendButton from './components/SpendButton.svelte';
  import SpendPanel from './components/SpendPanel.svelte';
  import { NOT_A_BILL, PENDING_NOTE, todayNote, todaySpoken, todayText, todayTitle } from './data/spend';
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

  let {
    feed,
    actions,
    catchup = createCatchupClient((...a) => fetch(...a)),
    leftovers = createLeftoversClient((...a) => fetch(...a)),
    tick: every = 10_000,
    permission = browserPermission(),
  }: { feed: Feed; actions: Actions; catchup?: CatchupClient; leftovers?: LeftoversClient; tick?: number; permission?: Permission } = $props();

  const board = new BoardState(() => actions);
  const cu = new CatchupState(() => catchup);
  // Every critter on the page reads its move on a change of state from here.
  setContext(MOVES, board.moves);
  let view = $state<View>(loadView());
  let now = $state(Date.now());

  onMount(() => {
    const stop = board.start(feed);
    const stopCatchup = cu.start();
    const clock = setInterval(() => (now = Date.now()), every);
    return () => {
      stop();
      stopCatchup();
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
  // The tab's title and icon say how many agents need you, from every project.
  const signal = $derived(tabSignal(board.conn, snap?.cards ?? null));
  // The badges' addresses carry the build sitboard last said it is.
  const iconV = $derived(iconVersion(board.raw?.build));
  $effect(() => showSignal(document, signal, iconV));
  // Loaded while live, once per build, so the browser has it when it is needed.
  let warmed = '';
  $effect(() => {
    if (board.conn !== 'live' || !iconV || warmed === iconV) return;
    warmed = iconV;
    warmLostIcon(document, iconV);
  });

  // The desktop notification: off until the owner turns it on in the header.
  // svelte-ignore state_referenced_locally
  const notify = new NotifySwitch(permission);
  let notifier: ReturnType<typeof startNotifier> | null = null;
  onMount(() => {
    notifier = startNotifier(notify);
    return () => {
      notifier?.stop();
      clearTimeout(sayTimer);
    };
  });
  const NOTIFY_WHAT = 'Turn on notifications';
  // What a click on the switch came to, said once by the page's live region:
  // what needs the owner to act as a notice, the rest as a status line.
  async function toggleNotify() {
    const r = await notify.toggle();
    if (r === 'busy') return;
    if (r === 'denied' || r === 'none') {
      board.notice = { kind: 'failed', what: NOTIFY_WHAT, message: OUTCOME_TEXT[r] };
      return;
    }
    if (board.notice?.kind === 'failed' && board.notice.what === NOTIFY_WHAT) board.dismiss();
    board.said = '';
    sayTimer = setTimeout(() => (board.said = OUTCOME_TEXT[r]), 0); // emptied first, so the same words twice are read twice
  }
  let sayTimer: ReturnType<typeof setTimeout> | undefined;

  // Every snapshot the board accepts, in order; seen marks do not matter here.
  $effect(() => {
    const s = board.raw;
    if (s) notifier?.observe(s);
  });
  // While the connection is down the board is shown as it was, and time stops with it.
  const frozen = $derived(board.lostAt !== null && board.conn !== 'live');
  const viewNow = $derived(frozen ? (board.lostAt as number) : now);
  const announce = $derived.by(() => {
    const n = board.notice;
    if (!n) return '';
    if (n.kind === 'copy')
      return `${n.why === 'no_agterm' ? 'Not in tmux, agterm not found' : 'Not in agterm or tmux'}. Folder ${n.folder || 'unknown'}. Session ${n.name || 'unnamed'}.`;
    return `${n.what} failed: ${n.message}.`;
  });
  const said = $derived(announce || board.said);
  const onforget = (p: { id: string; name: string }) => board.forgetGrouping(p);
  const onsetting = (p: { id: string; name: string }, mode: GroupingMode) => board.setFolderGrouping(p, mode);
  const onauto = (s: GroupingSetting) => board.setFolderGrouping({ id: s.projectId, name: s.name }, 'auto');
  // project id -> its grouping setting, for the parts row's controls.
  const settings = $derived(new Map((snap?.groupingSettings ?? []).map((s) => [s.projectId, s.mode])));
  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && board.notice) board.dismiss();
  }
  const ix = $derived(snap ? indexOf(snap) : null);
  // The catch-up list drops the merge requests of a GitLab host that leaves.
  $effect(() => {
    const hosts = ix?.gitlabHosts;
    if (hosts) untrack(() => cu.setGitlabHosts(hosts));
  });
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

  // Catch-up: a panel over the current view. Opening it loads the list again
  // and marks nothing read.
  const CATCHUP_ID = 'catchup-panel';
  let catchupOpen = $state(false);
  let catchupBtn = $state<HTMLButtonElement>();
  let headerEl = $state<HTMLElement>();
  let panelTop = $state(0);
  // The panel starts where the header ends on screen, and follows it while the page scrolls.
  function measure() {
    panelTop = Math.max(0, Math.round(headerEl?.getBoundingClientRect().bottom ?? 0));
  }
  $effect(() => {
    if (!catchupOpen && !spendOpen) return;
    measure();
    window.addEventListener('scroll', measure, { passive: true });
    window.addEventListener('resize', measure);
    return () => {
      window.removeEventListener('scroll', measure);
      window.removeEventListener('resize', measure);
    };
  });
  const onBoard = $derived(new Set(snap ? snap.cards.map((c) => c.id) : []));
  const showCatchup = $derived(board.conn !== 'unauthorized' && board.conn !== 'outdated');
  // Leftovers show private paths: a lost key from any poll (the board's or
  // Catch-up's) or a page older than the server turns them off, and an open
  // sheet goes at once.
  const leftoversOn = $derived(showCatchup && cu.status !== 'unauthorized');

  // Spend: today's estimate in the header, and the project × day panel. One
  // panel is open at a time.
  const SPEND_ID = 'spend-panel';
  let spendOpen = $state(false);
  let spendBtn = $state<HTMLButtonElement>();
  const spendToday = $derived(snap ? todayText(snap.spend, now) : '');
  const spendTodaySpoken = $derived(snap ? todaySpoken(snap.spend, now) : '');
  const spendPending = $derived(snap?.spend.pending ?? false);
  const spendTodayTitle = $derived.by(() => {
    const head = (snap ? todayTitle(snap.spend, now) : '') || `Nothing used today. ${NOT_A_BILL}`;
    return `${head}\n${spendPending ? `${PENDING_NOTE}\n` : ''}Today's use and the last 7 days, by project.`;
  });
  async function openSpend() {
    catchupOpen = false;
    spendOpen = true;
    await tick();
    document.getElementById(SPEND_ID)?.focus();
  }
  function closeSpend(refocus: Refocus) {
    spendOpen = false;
    if (refocus === 'button') spendBtn?.focus();
  }

  async function openCatchup() {
    spendOpen = false;
    catchupOpen = true;
    void cu.refresh();
    await tick();
    document.getElementById(CATCHUP_ID)?.focus();
  }
  function closeCatchup(refocus: Refocus) {
    catchupOpen = false;
    if (refocus === 'button') catchupBtn?.focus();
  }
  // "Card" in the panel: close it, then bring the card into view and focus it.
  // A project filter that hides the card is lifted first.
  async function showCard(cardId: string) {
    catchupOpen = false;
    const find = () => [...document.querySelectorAll<HTMLElement>('[data-card]')].find((e) => e.dataset.card === cardId);
    if (!find() && sel.kind !== 'all') {
      picked = ALL;
      await tick();
    }
    const el = find();
    if (!el) return;
    el.scrollIntoView?.({ block: 'center', behavior: ms(1) ? 'smooth' : 'auto' });
    const target = el.matches('button') ? el : el.querySelector<HTMLElement>('button');
    target?.focus({ preventScroll: true });
    flash(el);
  }
  const onseen = (c: Card) => void board.seen(c);
</script>

<svelte:window {onkeydown} />

<div class="page">
  <div bind:this={headerEl}>
    <Header {view} onview={setView} agents={snap ? snap.cards.length : null} {projects} conn={board.conn} generatedAt={snap?.generatedAt ?? null} notify={notify.state} onnotify={() => void toggleNotify()}>
      {#snippet catchup()}
        {#if showCatchup && snap}
          <SpendButton
            bind:button={spendBtn}
            figure={spendToday}
            spoken={spendTodaySpoken}
            pending={spendPending}
            note={snap ? todayNote(snap.spend, now) : ''}
            title={spendTodayTitle}
            open={spendOpen}
            controls={SPEND_ID}
            onclick={() => (spendOpen ? closeSpend('button') : void openSpend())}
          />
        {/if}
        {#if showCatchup}
          <CatchupButton
            bind:button={catchupBtn}
            count={cu.count}
            listed={cu.data?.items.length ?? 0}
            status={cu.status}
            open={catchupOpen}
            controls={CATCHUP_ID}
            onclick={() => (catchupOpen ? closeCatchup('button') : void openCatchup())}
          />
        {/if}
      {/snippet}
    </Header>
  </div>
  {#if catchupOpen && showCatchup}
    <CatchupPanel id={CATCHUP_ID} {cu} {now} {onBoard} gitlabHosts={ix?.gitlabHosts ?? []} toggle={catchupBtn} top={panelTop} onclose={closeCatchup} onshowcard={(id) => void showCard(id)} />
  {/if}
  {#if spendOpen && showCatchup && snap}
    <SpendPanel id={SPEND_ID} spend={snap.spend} {now} toggle={spendBtn} top={panelTop} onclose={closeSpend} />
  {/if}

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
              <ProjectTiles g={grp} {sel} onselect={select} mode={grouping} onmode={setGrouping} {onforget} {onsetting} {settings} />
            {:else}
              <ProjectRail g={grp} {sel} onselect={select} mode={grouping} onmode={setGrouping} {onforget} {onsetting} {settings} />
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

  <StatusBar snapshot={board.conn === 'unauthorized' ? null : snap} {onauto} leftovers={leftoversOn ? leftovers : undefined} />

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
