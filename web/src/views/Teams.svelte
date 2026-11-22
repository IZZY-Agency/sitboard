<script lang="ts">
  // Teams: the owner's queue beside one panel per coordinator with its workers,
  // and one panel of solo agents.
  import { flip } from 'svelte/animate';
  import type { Card as CardT, Snapshot } from '../contract.gen';
  import {
    agentName,
    byLongest,
    jumpLabel,
    cardTitle,
    effortOf,
    inbox,
    modelFamily,
    modelLabel,
    modelUnknown,
    cardPrSummary,
    cardPrSummaryParts,
    projectName,
    stateWord,
    teams,
    timeInState,
    uncertainRole,
    type Index,
    type Line,
    type RowLine,
  } from '../data/derive';
  import { since } from '../data/time';
  import { hasGitLab, requestsHeading } from '../data/words';
  import { flipOpts, makeMove } from '../motion';
  import Count from '../components/Count.svelte';
  import InboxItem from '../components/InboxItem.svelte';
  import Critter from '../components/Critter.svelte';
  import RowLinks from '../components/RowLinks.svelte';
  import { critterState } from '../data/critter';
  import { SPOKEN_ESTIMATE, TOKENS_ONLY, TOKEN_FIGURE, fmtTokens, fmtUSD, hasSpend, isPartial, spendTitle, tokenCount } from '../data/spend';

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

  function prCell(card: CardT): RowLine | Line | null {
    const s = cardPrSummaryParts(card, ix);
    if (s) return s;
    if ((ix.workers.get(card.id)?.length ?? 0) > 0) {
      const up = since(card.agent.startedAt, now);
      return { text: up ? `coordinator · up ${up}` : 'coordinator', tone: 'body' };
    }
    return null;
  }

  // A click that reached the row itself, not the jump button or a link: a
  // raised cell (.tip). The button and the links handle their own clicks.
  function rowClick(e: MouseEvent, card: CardT) {
    if (!(e.target instanceof Element) || !e.target.closest('a, button')) onjump(card);
  }

  function idleReason(card: CardT): Line {
    const parts = [projectName(card, ix)];
    const s = cardPrSummary(card, ix);
    if (s) parts.push(s.text);
    if (card.agent.status === 'unknown') parts.push('status unknown');
    else if (snapshot.agtermPresent && card.agent.jump === 'none') parts.push('not in agterm or tmux');
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
          <InboxItem {card} {ix} {now} flashToken={arrivals.get(card.id) ?? 0} {onjump} {onseen} />
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
          <InboxItem {card} {ix} {now} reason={idleReason(card)} showProject={false} {onjump} {onseen} />
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
              <span>State</span><span>Agent</span><span>Doing</span><span>Model</span><span>{requestsHeading(hasGitLab(ix.gitlabHosts))}</span><span class="r">{TOKEN_FIGURE === 'output' ? 'Out' : 'Tokens'}</span><span class="r">≈ $</span><span class="r">For</span>
            </div>
            {#each team.rows as r (r.card.id)}
              {@const card = r.card}
              {@const name = agentName(card)}
              {@const pr = prCell(card)}
              {@const unsure = uncertainRole(card, ix)}
              {@const model = modelLabel(card.agent.model)}
              {@const family = modelFamily(card.agent.model)}
              {@const effort = effortOf(card)}
              <!-- The agent's name is the jump button; its hit area covers the
                   whole row, and the links in Pull requests sit above it (RowLinks). -->
              <!-- A pointer-only click for the cells raised above the jump area
                   (.tip, for their tooltips): they still jump. Keyboards and
                   screen readers use the button. -->
              <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
              <div
                class="trow"
                data-card={card.id}
                onclick={(e) => rowClick(e, card)}
                animate:flip={flipOpts()}
                in:receive={{ key: card.id }}
                out:send={{ key: card.id }}
              >
                <span class="state" class:needs={card.column === 'needs_you'}>{stateWord(card)}</span>
                <span class="who" style:--depth={r.depth}
                  ><button
                    type="button"
                    class="jump"
                    aria-describedby={hasSpend(card.spend) ? `tok-${card.id} usd-${card.id}` : undefined}
                    onclick={() => onjump(card)}
                    ><span class="visually-hidden">{jumpLabel(card)}</span
                    ><Critter id={card.id} mood={critterState(card)} size={20} /><span class="agent u">{name}</span></button
                  ></span
                >
                <span class="doing">
                  {#if team.solo}<span class="proj u">{projectName(card, ix)}</span>{/if}{#if unsure}<span class="proj">{unsure}</span>{/if}<span class="u">{cardTitle(card)}</span>
                </span>
                <span class="mcell"
                  ><span class="model tip u {family ? `model-${family}` : ''}" class:unknown={modelUnknown(card.agent.model)} title={model}>{model}</span
                  >{#if effort}<span class="visually-hidden">{', effort '}</span><span class="eff tip effort-{effort.level}" class:launch={effort.launch} title={effort.title}
                      >{effort.text}{#if effort.launch}<span class="at">{' at launch'}</span>{/if}</span
                    >{/if}</span
                >
                <span class="prs tone-{pr?.tone ?? 'muted'}"
                  >{#if pr && 'parts' in pr}<RowLinks parts={pr.parts} keep />{:else if pr}<span class="u">{pr.text}</span>{/if}</span
                >
                {#if hasSpend(card.spend)}
                  <!-- Raised above the jump area (.tip) for their tooltips; the
                       jump button is described by them for a keyboard. -->
                  {@const codex = card.agent.tool === 'codex'}
                  <span class="num tip" id="tok-{card.id}" title={spendTitle(card.spend, codex)}
                    ><span class="visually-hidden">{TOKEN_FIGURE === 'output' ? ', output tokens ' : ', tokens '}</span>{fmtTokens(tokenCount(card.spend.tokens))}</span
                  >
                  <span class="num tip" id="usd-{card.id}" title={spendTitle(card.spend, codex)}
                    >{#if codex}<span class="none tonly" aria-hidden="true">{TOKENS_ONLY}</span><span class="visually-hidden">, {TOKENS_ONLY}</span
                      >{:else if card.spend.priced || isPartial(card.spend)}<span aria-hidden="true"
                        >{'≈ '}{fmtUSD(card.spend.usd).replace('$', '')}{#if !card.spend.priced}<span class="more">+ unpriced</span>{/if}</span
                      ><span class="visually-hidden"
                        >{`, about ${fmtUSD(card.spend.usd)}${card.spend.priced ? '' : ' plus unpriced use'}, ${SPOKEN_ESTIMATE}`}</span
                      >{:else}<span class="none" aria-hidden="true">no price</span><span class="visually-hidden">, no price</span>{/if}</span
                  >
                {:else}
                  <span class="num none" aria-hidden="true">—</span><span class="num none" aria-hidden="true">—</span>
                {/if}
                <span class="for">{timeInState(card, now)}</span>
              </div>
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
    min-width: 1060px;
  }
  .trow {
    /* The critter's eyes are holes in the page ground, or in the hover wash. */
    --hole: var(--bg);
    position: relative;
    display: grid;
    grid-template-columns: 160px 170px minmax(0, 1fr) 128px 230px 72px 80px 84px;
    align-items: center;
    width: 100%;
    min-height: 46px;
    padding: 6px 16px;
    border-bottom: 1px solid var(--rule);
    transition:
      background-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  /* The wash shows only while the pointer is on the jump area, not on a link. */
  .trow:has(.jump:hover, .tip:hover) {
    --hole: color-mix(in srgb, rgb(221 218 207) 4%, var(--bg));
    background: rgb(221 218 207 / 4%);
  }
  .trow:has(.jump:active, .tip:active) {
    transform: translateY(1px);
  }
  .jump {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  /* The button's hit area, stretched over the whole row; its focus ring too. */
  .jump::after {
    content: '';
    position: absolute;
    inset: 0;
  }
  .jump:focus-visible {
    outline: none;
  }
  .jump:focus-visible::after {
    outline: 2px solid var(--amber);
    outline-offset: -2px;
  }
  /* A cell with a tooltip sits above the jump area, so the pointer reaches its
     title; a click on it still jumps (rowClick). */
  .tip {
    position: relative;
    z-index: 1;
    cursor: pointer;
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
  .who {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    padding-left: calc(var(--depth, 0) * 16px);
  }
  .agent {
    min-width: 0;
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
  /* The model's name, with its effort level on a second line. 16 characters
     of the name fit ("model unknown", "gpt-5.4-codex" with room) and the
     longest level, "xhigh at launch", with room in its smaller size; a longer
     id is cut with an ellipsis and stays whole in the tooltip. The padding
     keeps it off the Pull requests text. */
  .mcell {
    display: flex;
    flex-direction: column;
    min-width: 0;
    padding-right: 12px;
  }
  .model {
    font-size: var(--text-m);
    color: var(--model, inherit);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .model.unknown {
    color: var(--muted);
  }
  /* From high up the level sits on a small filled patch, heavier (base.css);
     set at launch, it has no patch. */
  .eff {
    align-self: flex-start;
    padding: 0 4px;
    margin-left: -4px;
    background: var(--effort-fill, none);
    font-size: var(--text-s);
    font-weight: var(--effort-weight, 400);
    line-height: 1.3;
    color: var(--effort);
    white-space: nowrap;
  }
  .eff.launch {
    background: none;
  }
  /* Set at launch: said in words, never by dimming (dim reads as low), and no brighter than the faintest level. */
  .at {
    color: var(--effort-low);
    font-weight: 400;
  }
  /* The line is short by construction (prSummaryParts), so it wraps rather than being cut. */
  .prs {
    padding-right: 12px;
    font-size: var(--text-s);
    line-height: 1.35;
    color: var(--tone);
    overflow-wrap: anywhere;
  }
  .for {
    font-size: var(--text-m);
    color: var(--muted);
  }
  .num {
    text-align: right;
    font-size: var(--text-s);
    color: var(--fg-body);
    white-space: nowrap;
  }
  .num.none,
  .num .none {
    color: var(--muted);
  }
  /* A Codex agent's "tokens only": the small size fits the narrow column. */
  .num .tonly {
    font-size: var(--text-xs);
  }
  /* "+ unpriced" on a line of its own: the column is too narrow for both. */
  .num .more {
    display: block;
    color: var(--amber);
    font-size: var(--text-xs);
  }
</style>
