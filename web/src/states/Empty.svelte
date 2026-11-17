<script lang="ts">
  // Zero cards: say so honestly and name what was found.
  import type { Snapshot } from '../contract.gen';
  import { failingSessionSources, SOURCE_LABEL } from '../data/derive';
  import { clean } from '../data/text';

  let { snapshot }: { snapshot: Snapshot } = $props();

  const found = $derived(snapshot.sources.filter((s) => s.state === 'ok').map((s) => SOURCE_LABEL[s.kind]));
  const missing = $derived(snapshot.sources.filter((s) => s.state === 'missing').map((s) => SOURCE_LABEL[s.kind]));
  const off = $derived(snapshot.sources.filter((s) => s.state === 'off').map((s) => SOURCE_LABEL[s.kind]));
  const failing = $derived(snapshot.sources.filter((s) => s.state === 'failing').map((s) => SOURCE_LABEL[s.kind]));
  const broken = $derived(failingSessionSources(snapshot));
  const noAgentTool = $derived(
    broken.length === 0 && !snapshot.sources.some((s) => (s.kind === 'claude' || s.kind === 'codex') && s.state === 'ok'),
  );
</script>

<section class="empty">
  {#if broken.length}
    <h1 class="warn">Cannot read the agent sessions</h1>
    <ul class="broken">
      {#each broken as b (b.kind)}
        <li><span class="failing">{SOURCE_LABEL[b.kind]} failing</span>{#if clean(b.detail)}<span class="u"> · {clean(b.detail)}</span>{/if}</li>
      {/each}
    </ul>
    <p>Agents may be running that sitboard cannot see. The board fills in as soon as the source works again.</p>
  {:else if noAgentTool}
    <h1>No agent sessions running</h1>
    <p>Neither Claude Code nor Codex was found on this machine. sitboard shows their sessions once one of them is installed and running.</p>
  {:else}
    <h1>No agent sessions running</h1>
    <p>sitboard shows the Claude Code and Codex agents running on this machine. Start one and it appears here within a second.</p>
  {/if}
  <dl>
    <dt>Found</dt>
    <dd>{found.length ? found.join(', ') : 'nothing'}</dd>
    {#if missing.length}<dt>Not found</dt><dd>{missing.join(', ')}</dd>{/if}
    {#if off.length}<dt>Off</dt><dd>{off.join(', ')}</dd>{/if}
    {#if failing.length}<dt>Failing</dt><dd class="failing">{failing.join(', ')}</dd>{/if}
  </dl>
  {#if snapshot.plainShells > 0}
    <p class="muted">{snapshot.plainShells} plain {snapshot.plainShells === 1 ? 'shell is' : 'shells are'} open with no agent in {snapshot.plainShells === 1 ? 'it' : 'them'}.</p>
  {/if}
</section>

<style>
  .empty {
    padding: 64px var(--gutter);
    max-width: 640px;
  }
  h1 {
    margin: 0 0 10px;
    font-family: var(--font-title);
    font-size: 22px;
    font-weight: 500;
    color: var(--fg);
  }
  p {
    margin: 0 0 20px;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 16px;
    margin: 0 0 20px;
    padding: 12px 14px;
    border: 1px solid var(--border);
  }
  dt {
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--muted);
    padding-top: 1px;
  }
  dd {
    margin: 0;
    color: var(--fg);
  }
  .failing {
    color: var(--state-failing);
  }
  h1.warn {
    color: var(--amber);
  }
  .broken {
    margin: 0 0 12px;
    padding: 0;
    list-style: none;
    color: var(--fg);
  }
  .muted {
    color: var(--muted);
  }
</style>
