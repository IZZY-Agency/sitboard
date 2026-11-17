<script lang="ts">
  // The result of an action that needs the owner: a folder and name to copy when
  // there is no terminal to focus, or what failed with a retry.
  import { fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import type { Notice } from '../data/store.svelte';
  import { MOVE_MS, ms } from '../motion';

  let { notice, ondismiss }: { notice: Notice; ondismiss: () => void } = $props();

  let copied = $state<string | null>(null);

  async function copy(what: string, text: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = what;
    } catch {
      copied = null;
    }
  }

  $effect(() => {
    void notice;
    copied = null;
  });
</script>

<section
  class="notice"
  class:failed={notice.kind === 'failed'}
  aria-label={notice.kind === 'copy' ? 'Where this session runs' : 'Action failed'}
  transition:fly={{ y: 6, duration: ms(MOVE_MS), easing: cubicOut }}
>
  {#if notice.kind === 'copy'}
    <h2 class="label">{notice.why === 'no_agterm' ? 'agterm not found' : 'Not in agterm'}</h2>
    <p>Open it yourself: the folder and the session name, to copy.</p>
    <dl>
      <dt>Folder</dt>
      <dd>
        <span class="u">{notice.folder || 'unknown'}</span>
        {#if notice.folder}<button type="button" class="act" onclick={() => copy('folder', notice.folder)}>{copied === 'folder' ? 'Copied' : 'Copy'}</button>{/if}
      </dd>
      <dt>Session</dt>
      <dd>
        <span class="u">{notice.name || 'unnamed'}</span>
        {#if notice.name}<button type="button" class="act" onclick={() => copy('name', notice.name)}>{copied === 'name' ? 'Copied' : 'Copy'}</button>{/if}
      </dd>
    </dl>
  {:else}
    <h2 class="label">Failed</h2>
    <p><span class="u">{notice.what}</span> failed: <span class="u">{notice.message}</span>.</p>
    <div class="row"><button type="button" class="act primary" onclick={notice.retry}>Retry</button></div>
  {/if}
  <button type="button" class="close" onclick={ondismiss} aria-label="Close">Close</button>
</section>

<style>
  .notice {
    position: fixed;
    right: var(--gutter);
    bottom: 52px;
    z-index: 3;
    width: min(420px, calc(100vw - 2 * var(--gutter)));
    padding: 18px 16px 12px;
    background: var(--surface);
    border: 1px solid var(--amber-line);
  }
  .notice.failed {
    border-color: var(--failing-line);
  }
  h2 {
    position: absolute;
    top: -9px;
    left: 14px;
    margin: 0;
    padding: 0 8px;
    background: var(--bg);
    color: var(--amber);
  }
  .failed h2 {
    color: var(--state-failing);
  }
  p {
    margin: 0 0 10px;
    padding-right: 64px;
    font-size: var(--text-m);
  }
  dl {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 6px 12px;
    margin: 0 0 6px;
    font-size: var(--text-m);
  }
  dt {
    color: var(--muted);
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    padding-top: 2px;
  }
  dd {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0;
    min-width: 0;
    color: var(--fg);
  }
  dd > span {
    overflow-wrap: anywhere;
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .act,
  .close {
    min-height: 32px;
    padding: 0 10px;
    border: 1px solid var(--border);
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--fg-body);
    transition:
      border-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .act {
    flex: none;
    margin-left: auto;
  }
  .row .act {
    margin-left: 0;
  }
  .act:hover,
  .close:hover {
    border-color: var(--border-hover);
    color: var(--fg);
  }
  .act:active,
  .close:active {
    transform: translateY(1px);
  }
  .act.primary {
    background: var(--amber);
    border-color: var(--amber);
    color: var(--bg);
    font-weight: 700;
  }
  .act.primary:hover {
    background: var(--amber-hover);
    color: var(--bg);
  }
  .close {
    position: absolute;
    top: 8px;
    right: 8px;
    border-color: transparent;
    color: var(--muted);
  }
</style>
