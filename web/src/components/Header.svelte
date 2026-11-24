<script lang="ts">
  import type { Snippet } from 'svelte';
  import Mark from './Mark.svelte';
  import type { Conn } from '../data/client';
  import type { View } from '../data/view';
  import { clock } from '../data/time';
  import Count from './Count.svelte';
  import ViewSwitcher from './ViewSwitcher.svelte';
  import NotifyToggle from './NotifyToggle.svelte';
  import type { SwitchState } from '../data/notify';

  let {
    view,
    onview,
    agents,
    projects,
    conn,
    generatedAt,
    notify = null,
    onnotify = () => {},
    catchup,
  }: {
    view: View;
    onview: (v: View) => void;
    agents: number | null;
    projects: number | null;
    conn: Conn;
    generatedAt: string | null;
    notify?: SwitchState | null;
    onnotify?: () => void;
    /** The Catch-up button, right after the totals. */
    catchup?: Snippet;
  } = $props();

  const live = $derived.by(() => {
    switch (conn) {
      case 'live':
        return { text: generatedAt ? `Live · ${clock(generatedAt)}` : 'Live', kind: 'on' };
      case 'reconnecting':
        return { text: 'Reconnecting', kind: 'warn' };
      case 'unreachable':
        return { text: 'Not connected', kind: 'warn' };
      case 'connecting':
        return { text: 'Connecting', kind: 'quiet' };
      default:
        return { text: 'Stopped', kind: 'warn' };
    }
  });
</script>

<header>
  <div class="brand">
    <Mark />
    <span class="word">sit<span class="accent">board</span></span>
  </div>
  <ViewSwitcher {view} {onview} />
  <div class="right">
    {#if agents !== null && projects !== null}
      <span class="totals">
        <span class="num"><Count value={agents} /></span>
        {agents === 1 ? 'agent' : 'agents'} ·
        <span class="num"><Count value={projects} /></span>
        {projects === 1 ? 'project' : 'projects'}
      </span>
    {/if}
    <!-- The header's controls (Catch-up, then Notify): one line above 560 px, their own row below. -->
    <span class="controls">{@render catchup?.()}{#if notify}<NotifyToggle state={notify} ontoggle={onnotify} />{/if}</span>
    <span class="live {live.kind}" role="status"><span class="dot" aria-hidden="true"></span>{live.text}</span>
  </div>
</header>

<style>
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 28px;
    padding: 8px var(--gutter);
    border-bottom: 1px solid var(--border);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .word {
    font-size: var(--text-lead);
    font-weight: 500;
    letter-spacing: 0.02em;
    color: var(--fg);
  }
  .accent {
    color: var(--amber);
  }
  .right {
    margin-left: auto;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 16px;
    font-size: var(--text-m);
    color: var(--muted);
  }
  .num {
    color: var(--fg);
  }
  /* A phone: the controls stay on one row and the totals drop below them. */
  .controls {
    display: contents;
  }
  /* A phone: Live goes up beside the brand, the switcher has its own row, the
     controls theirs, and the totals come last. Holds at 360 px with both
     controls in their widest state. */
  @media (max-width: 559px) {
    header {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      grid-template-areas:
        'brand live'
        'views views'
        'controls controls'
        'totals totals';
      gap: 8px 12px;
    }
    .brand {
      grid-area: brand;
    }
    header > :global(nav) {
      grid-area: views;
      justify-self: start;
    }
    .right {
      display: contents;
    }
    .controls {
      grid-area: controls;
      display: flex;
      flex-wrap: wrap;
      justify-content: flex-end;
      gap: 8px;
    }
    .controls:empty {
      display: none;
    }
    .live {
      grid-area: live;
      justify-self: end;
    }
    .totals {
      grid-area: totals;
      justify-self: end;
    }
  }
  /* The Live chip has the box of the header's other chips (Spend, Catch-up,
     Notify): 44 px tall, 12 px aside, the same type. */
  .live {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    min-height: 44px;
    padding: 0 12px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    white-space: nowrap;
    border: 1px solid var(--border);
  }
  .live .dot {
    position: relative;
    width: 6px;
    height: 6px;
  }
  /* Connected: green, and the dot breathes with a ring that grows out of it
     and fades. Only this state moves; the ring is drawn over the chip, so
     nothing shifts. base.css stops it under reduced motion. */
  .live.on {
    border-color: var(--working-edge);
    background: var(--working-soft);
    color: var(--state-working);
  }
  .live.on .dot {
    background: var(--state-working);
    animation: live-dot 2s ease-in-out infinite;
  }
  .live.on .dot::after {
    content: '';
    position: absolute;
    inset: 0;
    border: 1px solid var(--state-working);
    opacity: 0;
    animation: live-ring 2s ease-out infinite;
  }
  @keyframes live-dot {
    50% {
      opacity: 0.45;
    }
  }
  @keyframes live-ring {
    0% {
      opacity: 0.7;
      transform: scale(1);
    }
    100% {
      opacity: 0;
      transform: scale(2.6);
    }
  }
  .live.warn {
    border-color: var(--failing-line);
    background: transparent;
    color: var(--state-failing);
  }
  .live.warn .dot {
    background: var(--state-failing);
  }
  .live.quiet {
    border-color: var(--border);
    background: transparent;
    color: var(--muted);
  }
  .live.quiet .dot {
    background: var(--muted);
  }
</style>
