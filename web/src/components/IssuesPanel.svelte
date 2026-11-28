<script lang="ts">
  // The Issues panel: the open issues of the repositories of the selected
  // projects, read only, grouped by milestone, each with the agents on the
  // board that are on it or looked at it; pinned issues lead their repository. A non-modal dialog on the
  // right like Catch-up; Escape, Close or a click outside closes it.
  import { onMount } from 'svelte';
  import ActionNotice from './ActionNotice.svelte';
  import type { Notice } from '../data/store.svelte';
  import { fade, fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import type { Card, IssuePinRequest, IssuesSummary, OpenIssue, Project, RepoIssues } from '../contract.gen';
  import type { IssuesState } from '../data/issues.svelte';
  import {
    agentSeen,
    agentTags,
    byMilestone,
    judged,
    NO_MILESTONE,
    openIssueHref,
    passesAgent,
    repoIssuesHref,
    sectionsFor,
    withPins,
    type AgentFilter,
    type AgentTag,
  } from '../data/issues';
  import { clean } from '../data/text';
  import {
    AGENT_FILTER,
    agentRelation,
    agentsUnknownText,
    everySeenText,
    forgeName,
    noneSeenText,
    NO_AGENT_SEEN,
    PINNED_CLOSED,
    PINNED_HEADING,
    PINNED_OUTSIDE,
    pinLabel,
    PINS_UNSAVED_AFTER,
    PINS_UNSAVED_BEFORE,
  } from '../data/words';
  import { when } from '../data/time';
  import { ms as motionMs, VIEW_MS } from '../motion';
  import type { Refocus } from './CatchupPanel.svelte';

  let {
    id,
    is,
    now,
    summary,
    projects,
    scope,
    gitlabHosts = [],
    cards = new Map(),
    onpin,
    notice = null,
    ondismiss,
    toggle,
    top,
    onclose,
  }: {
    id: string;
    is: IssuesState;
    now: number;
    /** The snapshot's issues: its asOf is the list's while it is of the rows shown (the same rev). */
    summary?: IssuesSummary;
    /** The projects the selection covers (projectsOf): their repositories are listed. */
    projects: readonly Project[];
    /** The selection in words, for the title. */
    scope: string;
    /** The GitLab hosts an issue may link to (Snapshot.gitlabHosts). */
    gitlabHosts?: readonly string[];
    /** The cards on the board by id: an issue's agents are named from them. */
    cards?: ReadonlyMap<string, Card>;
    /** Pins or unpins an issue (shown at once, taken back on a refusal). */
    onpin?: (repoKey: string, req: IssuePinRequest) => void;
    /** A refused pin's notice: shown inside the panel, in its tab order, while it is open. */
    notice?: Notice | null;
    ondismiss?: () => void;
    toggle: HTMLElement | undefined;
    /** Where the panel starts, below the header (px). */
    top: number;
    onclose: (refocus: Refocus) => void;
  } = $props();

  let panel = $state<HTMLElement>();
  // The milestone shown: null for all of them, '' for issues in none.
  let picked = $state<string | null>(null);
  // The Agent filter; it combines with the milestone one.
  let agent = $state<AgentFilter>('any');

  // The list with the pins pressed here that it does not show yet.
  const data = $derived(is.data ? withPins(is.data, is.pins) : null);
  const sections = $derived(sectionsFor(data, projects));
  const open = $derived(sections.reduce((n, s) => n + (s.repo.state === 'ok' ? s.repo.open : 0), 0));
  // No repository answered yet: the count is not known, not zero.
  const known = $derived(sections.some((s) => s.repo.state === 'ok'));
  // Every milestone of the listed issues, in byMilestone's order, with how many are listed.
  const milestones = $derived.by(() => {
    const all = byMilestone(sections.flatMap((s) => s.repo.issues));
    return all.map((g) => ({ milestone: g.milestone, n: g.issues.length }));
  });
  const listed = $derived(milestones.reduce((n, m) => n + m.n, 0));
  // One order for chips and groups: each repository's groups follow the
  // chips' order, which counts every repository's due dates.
  const rank = $derived(new Map(milestones.map((m, i) => [m.milestone, i])));
  const groupsOf = (r: RepoIssues) =>
    byMilestone(r.issues).sort((a, b) => (rank.get(a.milestone) ?? 0) - (rank.get(b.milestone) ?? 0));
  // The filters' rows: the milestones when there are two or more; the Agent
  // filter while a listed issue was judged (the server sent its agents).
  const milestoneRow = $derived(milestones.length > 1);
  const agentRow = $derived(listed > 0 && sections.some((s) => s.repo.issues.some(judged)));
  // A filter whose row is gone (or a milestone no longer listed) shows all
  // again, so nothing it hid, an outside pin included, is out of reach.
  const active = $derived(milestoneRow && picked !== null && milestones.some((m) => m.milestone === picked) ? picked : null);
  const agentOn = $derived<AgentFilter>(agentRow ? agent : 'any');
  // A look that says again what is shown moves the snapshot's asOf, not its
  // rev: the rows are not fetched again, and their "as of" is the snapshot's.
  const asOfAt = $derived(summary?.asOf && data && summary.rev === data.rev ? summary.asOf : data?.asOf);
  const asOf = $derived(asOfAt ? when(asOfAt, now) : '');
  // The Agent filter's counts follow the milestone filter: the listed issues
  // in the milestone shown, by whether an agent on the board is on them.
  const agentCounts = $derived.by(() => {
    const inMs = sections.flatMap((s) => s.repo.issues).filter((i) => active === null || clean(i.milestone) === active);
    // An issue not judged is in neither: nothing is said about its agents.
    const sure = inMs.filter(judged);
    const seen = sure.filter((i) => agentSeen(i, cards)).length;
    return { any: inMs.length, with: seen, none: sure.length - seen } satisfies Record<AgentFilter, number>;
  });
  const shows = (i: OpenIssue) => (active === null || clean(i.milestone) === active) && passesAgent(i, agentOn, cards);
  // Pinned rows outside the listed ones are not judged by either filter: shown only with both at All and Any.
  const unfiltered = $derived(active === null && agentOn === 'any');
  const pinReq = (r: RepoIssues, i: OpenIssue, pinned: boolean): IssuePinRequest => ({ forge: r.forge, host: r.host, repo: r.repo, number: i.number, pinned });
  // What a repository says when the filters leave none of its listed issues.
  function noneNote(r: RepoIssues): string {
    const ms = active === null ? '' : ` in ${msName(active)}`;
    const inMs = r.issues.filter((i) => active === null || clean(i.milestone) === active);
    // None in the milestone at all: said as the milestone filter says it.
    if (agentOn === 'any' || inMs.length === 0) return r.open > r.issues.length ? `None of the listed issues is${ms}.` : `None${ms}.`;
    if (!inMs.some(judged)) return agentsUnknownText(ms);
    return agentOn === 'none' ? everySeenText(ms) : noneSeenText(ms);
  }

  // Focus follows a pressed star (by repository key and number) when its row
  // moves between the Pinned group and its milestone, or back on a refusal.
  let follow: string | null = null;
  const pinOf = (key: string) => panel?.querySelector<HTMLElement>(`[data-pin="${CSS.escape(key)}"]`) ?? null;
  function press(r: RepoIssues, i: OpenIssue, pinned: boolean) {
    follow = `${r.key}#${i.number}`;
    onpin?.(r.key, pinReq(r, i, pinned));
  }
  // The pin's notice, in the panel: Retry and Close lead back to the issue's star (or the panel).
  const shown = $derived.by((): Notice | null => {
    const n = notice;
    if (!n || n.kind !== 'failed' || !n.retry) return n;
    const retry = n.retry;
    return {
      ...n,
      retry: () => {
        // The Retry button goes with the notice: focus goes to the star, and follows it (onfocusin).
        retry();
        (n.issue ? pinOf(n.issue) : null)?.focus();
      },
    };
  });
  function dismiss() {
    const key = notice?.kind === 'failed' ? notice.issue : undefined;
    ondismiss?.();
    const star = key ? pinOf(key) : null;
    if (star) star.focus();
    else panel?.focus();
  }

  // The notice a refused pin raises (ActionNotice) is part of what the panel
  // started: pressing its Retry or Close leaves the panel open.
  const inNotice = (n: Node) => !!(n instanceof Element ? n : n.parentElement)?.closest('[data-action-notice]');

  onMount(() => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (panel?.contains(t) || toggle?.contains(t) || inNotice(t)) return;
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

  // Focus may go back to the Issues toggle while the panel stays open (it
  // is the control that opened it): Escape there closes it too.
  $effect(() => {
    const t = toggle;
    if (!t) return;
    t.addEventListener('keydown', onkeydown);
    return () => t.removeEventListener('keydown', onkeydown);
  });

  // Anything inside the panel can be taken away while it has focus: an issue
  // that was closed, a milestone that is gone, "Try again" when the read
  // starts. Whatever the cause, focus goes back to the panel (so Escape still
  // closes it) after the panel's contents change and focus is nowhere inside.
  // focusIn is true from the moment focus enters the panel until it moves to
  // a control outside it; a focusout with no target (a node being removed,
  // the window losing focus) does not end it.
  let focusIn = false;
  onMount(() => {
    if (!panel) return;
    const el = panel;
    const seen = new MutationObserver(() => {
      if (!focusIn || el.contains(document.activeElement)) return;
      const star = follow ? pinOf(follow) : null;
      if (star) star.focus();
      else el.focus();
    });
    seen.observe(el, { childList: true, subtree: true });
    return () => seen.disconnect();
  });

  function onfocusin(e: FocusEvent) {
    focusIn = true;
    // Focus that lands elsewhere (not a star, not the notice) stops following.
    const t = e.target as HTMLElement;
    if (t.dataset?.pin) follow = t.dataset.pin;
    else if (!t.closest?.('[data-action-notice]') && t !== panel) follow = null;
  }

  function onfocusout(e: FocusEvent) {
    const to = e.relatedTarget as Node | null;
    if (!to) return;
    focusIn = !!panel?.contains(to);
    if (panel?.contains(to) || toggle?.contains(to) || inNotice(to)) return;
    onclose('stay');
  }

  const msName = (m: string) => clean(m) || NO_MILESTONE;
  // A milestone's due date is a day: shown as one, in UTC as the forges keep it.
  const DAY = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });
  const due = (iso: string | undefined) => (iso && !Number.isNaN(Date.parse(iso)) ? `due ${DAY.format(new Date(iso))}` : '');
</script>

{#snippet tag(t: AgentTag)}
  <span class="ag" class:weak={t.kind === 'looked'}><span class="agn u">{t.name}</span><span class="via">{` · ${agentRelation(t.kind)}`}</span></span>
{/snippet}

{#snippet row(r: RepoIssues, i: OpenIssue, out: boolean)}
  {@const href = openIssueHref(r, i, gitlabHosts)}
  {@const tags = agentTags(i, cards)}
  {@const closed = out && i.state === 'closed'}
  {@const title = clean(i.title)}
  <li class="it" class:closed>
    {#if href}
      <a class="num" {href} target="_blank" rel="noopener noreferrer"
        >#{i.number}<span class="visually-hidden">{` on ${forgeName(r.forge)}, opens in a new tab`}</span></a
      >
    {:else}
      <span class="num">#{i.number}</span>
    {/if}
    <!-- A pin known only by its number (the forge gave no title) shows the number and its note. -->
    <span class="ttl">{title || (out ? '' : 'untitled issue')}</span>
    {#if !closed}
      <button
        type="button"
        class="pin"
        class:on={i.pinned}
        data-pin="{r.key}#{i.number}"
        aria-pressed={i.pinned}
        aria-label={pinLabel(i.number)}
        onclick={() => press(r, i, !i.pinned)}
        ><span aria-hidden="true">{i.pinned ? '★' : '☆'}</span></button
      >
    {/if}
    <span class="meta">
      {#if out}
        {#if closed}
          <span class="gone">{PINNED_CLOSED}</span>{' · '}<button type="button" class="unpin" onclick={() => onpin?.(r.key, pinReq(r, i, false))}
            >unpin<span class="visually-hidden">{` issue #${i.number}`}</span></button
          >
        {:else}
          <span class="free">{PINNED_OUTSIDE}</span>
        {/if}
        <!-- Not judged as listed rows are: an agent the server names shows, never "no agent seen". -->
        {#each tags as t (t.cardId)}{@render tag(t)}{/each}
      {:else}
        {#if i.updatedAt}<span class="at"><span class="visually-hidden">{'updated '}</span>{when(i.updatedAt, now)}</span>{/if}
        {#each i.labels as l, li (li)}<span class="lb">{clean(l)}</span>{/each}
        <!-- Nothing about agents on an issue the server did not judge. -->
        {#each tags as t (t.cardId)}{@render tag(t)}{:else}{#if judged(i)}<span class="free">{NO_AGENT_SEEN}</span>{/if}{/each}
      {/if}
    </span>
  </li>
{/snippet}

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
  {onfocusin}
  {onfocusout}
  in:fly={{ x: 16, duration: motionMs(VIEW_MS), easing: cubicOut }}
  out:fade={{ duration: motionMs(VIEW_MS) }}
>
  <div class="head">
    <h2 id="{id}-title" class="title">Issues<span class="scope">{' · '}{scope}</span></h2>
    <span id="{id}-since" class="since">
      {#if !data}
        &nbsp;
      {:else}
        {#if !known}<span class="seg">not answered yet</span>{:else}<span class="seg">{open} open</span>{/if}{#if known && listed < open}{' · '}<span class="seg">{listed} listed here</span>{/if}{' · '}<span class="seg">read only</span>{#if asOf}{' · '}<span class="seg">as of {asOf}</span>{/if}
      {/if}
    </span>
    <div class="acts">
      <button type="button" class="close" onclick={() => onclose('button')}>Close<span class="key" aria-hidden="true">Esc</span></button>
    </div>
  </div>

  {#if is.status === 'unauthorized'}
    <p class="msg warn" role="alert">This tab lost its key. Open the link sitboard printed in the terminal again.</p>
  {:else if is.status === 'failed' && is.error}
    <p class="msg warn" role="alert">
      {data ? `Could not load it again: ${is.error}. This is the list as it was.` : `Could not load the issues: ${is.error}.`}
      <button type="button" class="retry" onclick={() => void is.refresh()}>Try again</button>
    </p>
  {/if}

  <div class="body">
    {#if milestoneRow}
      <div class="filter" role="group" aria-label="Milestone, counting the issues listed here">
        <span class="fl" aria-hidden="true">Listed</span>
        <button type="button" class="chip" aria-pressed={active === null} onclick={() => (picked = null)}>All<span class="cn">{listed}</span></button>
        {#each milestones as m (m.milestone)}
          <button type="button" class="chip" aria-pressed={active === m.milestone} onclick={() => (picked = m.milestone)}
            ><span class="u">{msName(m.milestone)}</span><span class="cn">{m.n}</span></button
          >
        {/each}
      </div>
    {/if}
    {#if agentRow}
      <div class="filter" class:second={milestoneRow} role="group" aria-label="Agent, counting the issues listed here{active === null ? '' : ` in ${msName(active)}`}">
        <span class="fl" aria-hidden="true">Agent</span>
        {#each AGENT_FILTER as f (f.value)}
          <button type="button" class="chip" aria-pressed={agentOn === f.value} onclick={() => (agent = f.value)}>{f.label}<span class="cn">{agentCounts[f.value]}</span></button>
        {/each}
      </div>
    {/if}
    {#if data?.unsaved}
      <p class="msg note">{PINS_UNSAVED_BEFORE}<span class="flag">--no-state</span>{PINS_UNSAVED_AFTER}</p>
    {/if}
    {#if !data}
      {#if is.status === 'loading'}<p class="msg">Loading…</p>{/if}
    {:else if data.state === 'off'}
      <p class="empty">Issues are off: sitboard runs without GitHub and GitLab lookups.</p>
    {:else if sections.length === 0}
      <p class="empty">{data.state === 'loading' ? 'Asking for the issues…' : 'None of these projects has a repository sitboard can ask about.'}</p>
    {:else}
      {#each sections as s (s.key)}
        {@const pinnedRows = s.repo.state === 'ok' ? s.repo.issues.filter((i) => i.pinned && shows(i)) : []}
        {@const outside = s.repo.state === 'ok' && unfiltered ? s.repo.pinned : []}
        {@const groups = groupsOf({ ...s.repo, issues: s.repo.issues.filter((i) => !i.pinned && shows(i)) })}
        {@const more = s.repo.state === 'ok' ? s.repo.open - s.repo.issues.length : 0}
        {@const list = more > 0 ? repoIssuesHref(s.repo, gitlabHosts) : null}
        <section class="repo" aria-labelledby="{id}-{s.key}" data-repo={s.key}>
          <h3 id="{id}-{s.key}" class="rh">
            <span class="pname u">{s.name}</span>
            {#if clean(s.repo.repo) !== s.name}<span class="path">{clean(s.repo.repo)}</span>{/if}
            {#if s.repo.state === 'ok'}<span class="n">{s.repo.open}</span>{/if}
          </h3>
          {#if s.repo.state === 'pending'}
            <p class="note">Not asked yet: {forgeName(s.repo.forge)} has not answered about this repository.</p>
          {:else if s.repo.state === 'disabled'}
            <p class="note">Issues are turned off in this repository.</p>
          {:else if s.repo.state === 'notFound'}
            <p class="note">{forgeName(s.repo.forge)} did not find this repository, or this login cannot see it.</p>
          {:else if s.repo.open === 0}
            <p class="note">No open issues.</p>
          {:else if groups.length === 0 && pinnedRows.length === 0}
            <p class="note">{noneNote(s.repo)}</p>
          {/if}
          {#if pinnedRows.length || outside.length}
            <div class="ms">
              <h4 class="msh">{PINNED_HEADING}</h4>
              <ul>
                {#each pinnedRows as i (i.number)}{@render row(s.repo, i, false)}{/each}
                {#each outside as i (`o${i.number}`)}{@render row(s.repo, i, true)}{/each}
              </ul>
            </div>
          {/if}
          {#each groups as g (g.milestone)}
            <div class="ms">
              {#if active === null}
                <h4 class="msh"><span class="u">{msName(g.milestone)}</span>{#if g.issues[0]?.milestoneDue}<span class="due">{due(g.issues[0].milestoneDue)}</span>{/if}</h4>
              {/if}
              <ul>
                {#each g.issues as i (i.number)}{@render row(s.repo, i, false)}
                {/each}
              </ul>
            </div>
          {/each}
          {#if more > 0}
            {#if list}
              <a class="more" href={list} target="_blank" rel="noopener noreferrer">+{more} more on {forgeName(s.repo.forge)} ↗<span class="visually-hidden">, opens in a new tab</span></a>
            {:else}
              <p class="note">+{more} more on {forgeName(s.repo.forge)}</p>
            {/if}
          {/if}
        </section>
      {/each}
    {/if}
  </div>
  <!-- After the list, so Tab reaches it without leaving the dialog. -->
  {#if shown}<ActionNotice notice={shown} ondismiss={dismiss} />{/if}
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
  .panel:focus {
    outline: none;
  }
  /* Title and the line under it on the left, Close on the right. */
  .head {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 4px 12px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .title {
    margin: 0;
    min-width: 0;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--fg);
    overflow-wrap: anywhere;
  }
  .scope {
    color: var(--muted);
    font-weight: 400;
  }
  .since {
    grid-column: 1;
    grid-row: 2;
    color: var(--muted);
  }
  .acts {
    grid-column: 2;
    grid-row: 1 / span 2;
    display: flex;
    gap: 8px;
  }
  .close {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-height: 44px;
    padding: 0 14px;
    border: 1px solid var(--border);
    color: var(--fg);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
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
  /* Inside the scrolling body, so many milestones never leave the list without room. */
  .filter {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: -4px -16px 0;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 32px;
    max-width: 100%;
    padding: 0 10px;
    border: 1px solid var(--border);
    color: var(--fg-body);
    font-size: var(--text-xs);
    letter-spacing: 0.06em;
  }
  .chip .u {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chip:hover {
    color: var(--fg);
    background: rgb(221 218 207 / 5%);
  }
  .chip[aria-pressed='true'] {
    border-color: var(--amber);
    color: var(--amber);
  }
  .seg {
    white-space: nowrap;
  }
  .fl {
    align-self: center;
    margin-right: 2px;
    color: var(--muted);
    font-size: var(--text-xs);
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
  }
  .cn {
    color: var(--muted);
    font-variant-numeric: tabular-nums;
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
  .repo {
    padding-top: 12px;
  }
  .rh {
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
  .rh::after {
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
    max-width: 50%;
  }
  .path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    text-transform: none;
    letter-spacing: 0.02em;
    min-width: 0;
  }
  .n {
    font-variant-numeric: tabular-nums;
  }
  .note {
    margin: 6px 0;
    color: var(--muted);
  }
  .ms {
    padding-top: 6px;
  }
  .msh {
    display: flex;
    gap: 8px;
    align-items: baseline;
    margin: 4px 0 0;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.06em;
    color: var(--fg-body);
  }
  .due {
    color: var(--muted);
    font-weight: 400;
  }
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .it {
    display: grid;
    grid-template-columns: 5ch minmax(0, 1fr) auto; /* "#1234" in the mono face, the title, the star */
    align-items: baseline;
    gap: 2px 10px;
    padding: 6px 0;
    border-bottom: 1px solid var(--rule);
  }
  .it:last-child {
    border-bottom: 0;
  }
  .num {
    display: inline-flex;
    align-items: center;
    min-height: 32px;
    color: var(--fg-body);
    font-variant-numeric: tabular-nums;
  }
  a.num {
    color: var(--state-review);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  a.num:hover {
    color: var(--fg);
  }
  .ttl {
    min-width: 0;
    font-family: var(--font-title);
    color: var(--fg);
    overflow-wrap: anywhere;
  }
  .meta {
    grid-column: 2 / 4;
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px;
  }
  .at {
    margin-right: 6px;
    color: var(--muted);
    white-space: nowrap;
  }
  .lb {
    max-width: 100%;
    padding: 0 5px;
    border: 1px solid var(--border);
    color: var(--muted);
    font-size: var(--text-xs);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* The second filter row sits right under the first, one rule between the two. */
  .filter:has(+ .filter.second) {
    border-bottom: 0;
    padding-bottom: 6px;
  }
  .filter.second {
    margin-top: 0;
    padding-top: 0;
  }
  .filter .fl {
    min-width: 7ch;
  }
  .msg.note {
    border-bottom: 1px solid var(--border);
    margin: 0 -16px;
  }
  .flag {
    color: var(--fg-body);
    white-space: nowrap;
  }
  /* The star: pinned in amber, unpinned muted; a 32px hit area. */
  .pin {
    align-self: start;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 32px;
    min-height: 32px;
    border: 1px solid transparent;
    color: var(--muted);
    font-size: var(--text-m);
  }
  .pin:hover {
    color: var(--fg);
    border-color: var(--border);
  }
  .pin.on {
    color: var(--amber);
  }
  .pin.on:hover {
    border-color: var(--amber-edge);
  }
  /* An agent on the issue, named from its card: solid for "on it" (its pull
     request closes the issue), dashed and hollow for "looked at". */
  .ag {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    max-width: 100%;
    padding: 0 6px;
    border: 1px solid var(--border);
    color: var(--fg-body);
    font-size: var(--text-xs);
    white-space: nowrap;
  }
  .ag::before {
    content: '';
    flex: none;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--state-working);
  }
  .ag.weak {
    border-style: dashed;
    color: var(--muted);
  }
  .ag.weak::before {
    background: transparent;
    border: 1px solid var(--muted);
  }
  .agn {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .via {
    flex: none;
    color: var(--muted);
  }
  .free {
    color: var(--muted);
    font-size: var(--text-xs);
  }
  .gone {
    color: var(--state-done);
    font-size: var(--text-xs);
  }
  .it.closed .ttl,
  .it.closed .num {
    opacity: 0.6;
  }
  .unpin {
    display: inline-flex;
    align-items: center;
    min-height: 32px;
    color: var(--fg-body);
    font-size: var(--text-xs);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .unpin:hover {
    color: var(--fg);
  }
  .more {
    display: inline-flex;
    align-items: center;
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
  }
</style>
