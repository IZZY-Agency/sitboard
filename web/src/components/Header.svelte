<script lang="ts">
  import Mark from './Mark.svelte';
  import type { Conn } from '../data/client';
  import type { View } from '../data/view';
  import { clock } from '../data/time';
  import Count from './Count.svelte';
  import ViewSwitcher from './ViewSwitcher.svelte';

  let {
    view,
    onview,
    agents,
    projects,
    conn,
    generatedAt,
  }: {
    view: View;
    onview: (v: View) => void;
    agents: number | null;
    projects: number | null;
    conn: Conn;
    generatedAt: string | null;
  } = $props();

  const live = $derived.by(() => {
    switch (conn) {
      case 'live':
        return { text: generatedAt ? `Live · ${clock(generatedAt)}` : 'Live', kind: 'live' };
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
  .live {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 3px 8px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    border: 1px solid var(--amber-edge);
    background: var(--amber-soft);
    color: var(--amber);
  }
  .live .dot {
    width: 6px;
    height: 6px;
    background: var(--amber);
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
