<script lang="ts">
  import type { Card } from '../contract.gen';
  import {
    agentName,
    attentionLines,
    canJump,
    cardPrs,
    cardCloses,
    cardTitle,
    columnTone,
    modelLabel,
    prLine,
    projectLabel,
    roleLine,
    timeInState,
    type Index,
    type Line,
  } from '../data/derive';
  import { flash } from '../motion';
  import PrIcon from './PrIcon.svelte';

  let {
    card,
    ix,
    now,
    agtermPresent,
    flashToken = 0,
    onjump,
    onseen,
  }: {
    card: Card;
    ix: Index;
    now: number;
    agtermPresent: boolean;
    flashToken?: number;
    onjump: (c: Card) => void;
    onseen: (c: Card) => void;
  } = $props();
  const place = $derived(projectLabel(card, ix));

  let el = $state<HTMLElement>();

  const name = $derived(agentName(card));
  const role = $derived(roleLine(card, ix));
  const flags = $derived.by((): Line[] => {
    const out = attentionLines(card, now);
    if (card.agent.status === 'unknown') out.push({ text: 'status unknown', tone: 'muted' });
    if (card.agent.status === 'ended') out.push({ text: 'session ended', tone: 'muted' });
    else if (agtermPresent && !card.agent.terminalId) out.push({ text: 'not in agterm', tone: 'muted' });
    if (card.subagents > 0) out.push({ text: `${card.subagents} subagent${card.subagents === 1 ? '' : 's'}`, tone: 'muted' });
    return out;
  });
  const prs = $derived(cardPrs(card.pullRequests));
  // Only beside pull request lines: a card with issues and no pull request shows nothing extra.
  const closes = $derived(prs.shown.length ? cardCloses(card, ix) : '');
  const unseen = $derived(card.attention.includes('unseen'));
  const failing = $derived(card.attention.includes('failing_checks'));

  $effect(() => {
    if (flashToken && el) flash(el);
  });
</script>

<article
  class="card"
  class:needs={card.column === 'needs_you'}
  class:failing={failing && card.column !== 'needs_you'}
  bind:this={el}
  data-card={card.id}
>
  <button
    class="main"
    type="button"
    onclick={() => onjump(card)}
  >
    <span class="visually-hidden">{canJump(card, agtermPresent) ? 'Jump to agterm:' : 'Show where it runs:'}</span>
    <span class="top">
      <span class="dot tone-{columnTone(card.column)}" class:pulse={card.column === 'working'}></span>
      <span class="agent u">{name}</span>
      <span class="since">{timeInState(card, now)}</span>
    </span>
    <span class="title u">{cardTitle(card)}</span>
    <span class="meta">
      <span class="project u">{place.name}{#if place.part}<span class="part">{' / '}{place.part}</span>{/if}</span>
      <span class="tag u">{modelLabel(card.agent.model)}</span>
      {#if role}<span class="u">{role}</span>{/if}
    </span>
    {#if flags.length}
      <span class="flags">
        {#each flags as f, i (i)}<span class="flag tone-{f.tone}">{f.text}</span>{/each}
      </span>
    {/if}
    {#if prs.shown.length}
      <span class="prs">
        {#each prs.shown as pr, i (`${pr.repo}#${pr.number}#${i}`)}
          {@const l = prLine(pr)}
          <span class="pr tone-{l.tone}"><PrIcon /><span>{l.text}</span></span>
        {/each}
        {#if prs.more}<span class="pr more tone-{prs.more.tone}">{prs.more.text}</span>{/if}
        {#if closes}<span class="pr more closes u">{closes}</span>{/if}
      </span>
    {/if}
  </button>
  {#if unseen}
    <button class="seen" type="button" onclick={() => onseen(card)}>Mark seen</button>
  {/if}
</article>

<style>
  .project .part {
    color: var(--muted);
  }
  .card {
    display: flex;
    flex-direction: column;
    min-width: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    transition:
      border-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .card:hover {
    border-color: var(--border-hover);
  }
  .card.needs {
    border-color: var(--amber-line);
  }
  .card.needs:hover {
    border-color: var(--amber);
  }
  .card.failing {
    border-color: var(--failing-line);
  }
  .card.failing:hover {
    border-color: var(--state-failing);
  }
  .card:has(.main:active),
  .card:has(.seen:active) {
    transform: translateY(1px);
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: 100%;
    min-width: 0;
    min-height: 44px;
    padding: 12px;
  }
  .main:focus-visible,
  .seen:focus-visible {
    outline-offset: -2px;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .agent {
    font-size: var(--text-m);
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .since {
    margin-left: auto;
    flex: none;
    font-size: var(--text-s);
    color: var(--muted);
  }
  .title {
    font-family: var(--font-title);
    font-size: var(--text-title);
    font-weight: 500;
    line-height: 1.35;
    color: var(--fg);
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    overflow-wrap: anywhere;
  }
  .meta,
  .flags {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 8px;
    font-size: var(--text-s);
    color: var(--muted);
    min-width: 0;
  }
  .meta > span {
    overflow-wrap: anywhere;
  }
  .project {
    color: var(--fg-body);
  }
  .tag {
    padding: 0 5px;
    border: 1px solid var(--border);
    font-size: var(--text-xs);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-body);
  }
  .flag {
    color: var(--tone);
  }
  .prs {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding-top: 8px;
    border-top: 1px solid var(--rule);
  }
  .pr {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: var(--text-s);
    color: var(--tone);
  }
  .pr.more {
    padding-left: 18px;
  }
  .pr.closes {
    color: var(--muted);
  }
  .seen {
    align-self: stretch;
    min-height: 36px;
    padding: 0 12px;
    border-top: 1px solid var(--rule);
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--amber);
    transition: color var(--press) ease-out;
  }
  .seen:hover {
    color: var(--amber-hover);
    background: var(--amber-soft);
  }
</style>
