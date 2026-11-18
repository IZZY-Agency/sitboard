<script lang="ts">
  // Teams: the owner's queue beside one panel per coordinator with its workers,
  // and one panel of solo agents.
  import { flip } from 'svelte/animate';
  import type { Card as CardT, Snapshot } from '../contract.gen';
  import {
    agentName,
    byLongest,
    canJump,
    cardTitle,
    columnTone,
    inbox,
    modelFamily,
    modelLabel,
    modelUnknown,
    cardPrSummary,
    projectName,
    stateWord,
    teams,
    timeInState,
    uncertainRole,
    type Index,
    type Line,
  } from '../data/derive';
  import { since } from '../data/time';
  import { flipOpts, makeMove } from '../motion';
  import Count from '../components/Count.svelte';
  import InboxItem from '../components/InboxItem.svelte';

  let {
    snapshot,
    ix,
    now,
    arrivals,
    onjump,
    onseen,
  }: {
    snapshot: Snapshot;
    ix: Index;
    now: number;
    arrivals: ReadonlyMap<string, number>;
    onjump: (c: CardT) => void;
    onseen: (c: CardT) => void;
  } = $props();

  const needs = $derived(inbox(snapshot.cards));
  const waiting = $derived(snapshot.cards.filter((c) => c.column === 'idle').sort(byLongest));
  const panels = $derived(teams(snapshot));
  const [send, receive] = makeMove();

  function prCell(card: CardT): Line | null {
    const s = cardPrSummary(card, ix);
    if (s) return s;
    if ((ix.workers.get(card.id)?.length ?? 0) > 0) {
      const up = since(card.agent.startedAt, now);
      return { text: up ? `coordinator · up ${up}` : 'coordinator', tone: 'body' };
    }
    return null;
  }

  function idleReason(card: CardT): Line {
    const parts = [projectName(card, ix)];
    const s = cardPrSummary(card, ix);
    if (s) parts.push(s.text);
    if (card.agent.status === 'unknown') parts.push('status unknown');
    else if (snapshot.agtermPresent && !card.agent.terminalId) parts.push('not in agterm');
    return { text: parts.join(' · '), tone: 'muted' };
  }
</script>

<div class="teams">
  <aside aria-label="Your queue">
    <section aria-labelledby="q-needs">
      <div class="head">
        <span class="dot tone-needs"></span>
        <h2 class="label" id="q-needs">Needs you</h2>
        <span class="n"><Count value={needs.length} /></span>
        <span class="rule" aria-hidden="true"></span>
      </div>
      {#each needs as card (card.id)}
        <div animate:flip={flipOpts()} in:receive={{ key: 'q' + card.id }} out:send={{ key: 'q' + card.id }}>
          <InboxItem {card} {ix} {now} agtermPresent={snapshot.agtermPresent} flashToken={arrivals.get(card.id) ?? 0} {onjump} {onseen} />
        </div>
      {:else}
        <p class="empty">Nobody needs you right now.</p>
      {/each}
    </section>
    <section aria-labelledby="q-wait">
      <div class="head">
        <span class="dot tone-idle"></span>
        <h2 class="label" id="q-wait">Waiting for your message</h2>
        <span class="n"><Count value={waiting.length} /></span>
        <span class="rule" aria-hidden="true"></span>
      </div>
      {#each waiting as card (card.id)}
        <div animate:flip={flipOpts()} in:receive={{ key: 'w' + card.id }} out:send={{ key: 'w' + card.id }}>
          <InboxItem {card} {ix} {now} agtermPresent={snapshot.agtermPresent} reason={idleReason(card)} showProject={false} {onjump} {onseen} />
        </div>
      {:else}
        <p class="empty">No agent is waiting for your next message.</p>
      {/each}
    </section>
  </aside>

  <main class="panels">
    {#each panels as team (team.key)}
      <section class="panel" class:solo={team.solo} aria-label={team.path ? `${team.label} · ${team.path}` : team.label}>
        <h2 class="label ptitle u">{team.label}</h2>
        <div class="pmeta">
          <span class="u">{team.path}</span>
          {#if team.projects.length}<span class="projs u">{team.projects.join(' · ')}</span>{/if}
          <span class="right">{team.meta}</span>
        </div>
        <div class="scroller">
          <div class="tbl">
            <div class="trow thead" aria-hidden="true">
              <span>State</span><span>Agent</span><span>Doing</span><span>Model</span><span>Pull requests</span><span class="r">For</span>
            </div>
            {#each team.rows as r (r.card.id)}
              {@const card = r.card}
              {@const name = agentName(card)}
              {@const pr = prCell(card)}
              {@const unsure = uncertainRole(card, ix)}
              {@const model = modelLabel(card.agent.model)}
              {@const family = modelFamily(card.agent.model)}
              <button
                type="button"
                class="trow"
                animate:flip={flipOpts()}
                in:receive={{ key: card.id }}
                out:send={{ key: card.id }}
                onclick={() => onjump(card)}
              >
                <span class="visually-hidden">{canJump(card, snapshot.agtermPresent) ? 'Jump to agterm:' : 'Show where it runs:'}</span>
                <span class="state" class:needs={card.column === 'needs_you'}>
                  <span class="dot tone-{columnTone(card.column)}" class:pulse={card.column === 'working'}></span>{stateWord(card)}
                </span>
                <span class="agent u" style:--depth={r.depth}>{name}</span>
                <span class="doing">
                  {#if team.solo}<span class="proj u">{projectName(card, ix)}</span>{/if}{#if unsure}<span class="proj">{unsure}</span>{/if}<span class="u">{cardTitle(card)}</span>
                </span>
                <span class="model u {family ? `model-${family}` : ''}" class:unknown={modelUnknown(card.agent.model)} title={model}>{model}</span>
                <span class="prs tone-{pr?.tone ?? 'muted'} u"
                  >{#each (pr?.text ?? '').split(' · ').filter(Boolean) as part, i (i)}{#if i}{' · '}{/if}<span class="part" class:closes={part.startsWith('closes ')}>{part}</span>{/each}</span
                >
                <span class="for">{timeInState(card, now)}</span>
              </button>
            {/each}
          </div>
        </div>
      </section>
    {/each}
  </main>
</div>

<style>
  .teams {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 32px;
    padding: 24px var(--gutter) 40px;
  }
  aside {
    flex: 1 1 300px;
    display: flex;
    flex-direction: column;
    gap: 28px;
    min-width: 0;
  }
  aside section {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  h2 {
    margin: 0;
    white-space: nowrap;
  }
  .n {
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .rule {
    flex: 1;
    height: 1px;
    background: var(--rule-strong);
  }
  .empty {
    margin: 0;
    padding: 14px 12px;
    border: 1px dashed var(--border);
    font-size: var(--text-s);
    color: var(--muted);
  }
  .panels {
    flex: 999 1 640px;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 32px;
    padding-top: 6px;
  }
  .panel {
    position: relative;
    border: 1px solid var(--border);
  }
  .ptitle {
    position: absolute;
    top: -9px;
    left: 14px;
    max-width: calc(100% - 28px);
    padding: 0 8px;
    background: var(--bg);
    color: var(--amber);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .solo .ptitle {
    color: var(--fg);
  }
  .pmeta {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 16px;
    padding: 16px 16px 10px;
    font-size: var(--text-s);
    color: var(--muted);
  }
  .projs {
    color: var(--fg-body);
  }
  .right {
    margin-left: auto;
  }
  .scroller {
    overflow-x: auto;
  }
  .tbl {
    min-width: 920px;
  }
  .trow {
    display: grid;
    grid-template-columns: 160px 170px minmax(0, 1fr) 120px 250px 84px;
    align-items: center;
    width: 100%;
    min-height: 46px;
    padding: 6px 16px;
    border-bottom: 1px solid var(--rule);
    transition:
      background-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  button.trow:hover {
    background: rgb(221 218 207 / 4%);
  }
  button.trow:active {
    transform: translateY(1px);
  }
  button.trow:focus-visible {
    outline-offset: -2px;
  }
  .trow:last-child {
    border-bottom: 0;
  }
  .thead {
    min-height: 0;
    padding: 8px 16px;
    background: var(--surface);
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--muted);
  }
  .r,
  .for {
    text-align: right;
  }
  .state {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: var(--text-m);
    color: var(--fg-body);
  }
  .state.needs {
    color: var(--amber);
  }
  .agent {
    padding-left: calc(var(--depth, 0) * 16px);
    font-size: var(--text-m);
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .doing {
    padding-right: 16px;
    font-family: var(--font-title);
    font-size: var(--text-title);
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .proj {
    margin-right: 10px;
    font-family: var(--font-ui);
    font-size: var(--text-s);
    color: var(--muted);
  }
  /* 15 characters fit ("model unknown", "gpt-5.4-codex" with room); a longer
     id is cut with an ellipsis and stays whole in the tooltip. The padding
     keeps it off the Pull requests text. */
  .model {
    padding-right: 12px;
    font-size: var(--text-m);
    color: var(--model, inherit);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .model.unknown {
    color: var(--muted);
  }
  /* The line is short by construction (prSummary), so it wraps rather than being cut. */
  .prs {
    padding-right: 12px;
    font-size: var(--text-s);
    line-height: 1.35;
    color: var(--tone);
    overflow-wrap: anywhere;
  }
  .part {
    white-space: nowrap;
  }
  .part.closes {
    color: var(--muted);
  }
  .for {
    font-size: var(--text-m);
    color: var(--muted);
  }
</style>
