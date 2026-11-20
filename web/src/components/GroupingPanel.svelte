<script lang="ts">
  // The folders whose grouping the owner set, opened from the status bar: a
  // folder set to never has no grouping on the board to click, so this is
  // where it is seen and set back to auto. Folders set to always are listed
  // too, so every setting is in one place. A non-modal panel like About:
  // Escape or a click outside closes it; Tab may leave it, which closes it.
  import { onMount, tick } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { cubicOut } from 'svelte/easing';
  import type { GroupingSetting } from '../contract.gen';
  import { clean } from '../data/text';
  import { ms, VIEW_MS } from '../motion';
  import type { Refocus } from './About.svelte';

  let {
    id,
    settings,
    toggle,
    onclose,
    onauto,
  }: {
    id: string;
    settings: GroupingSetting[];
    toggle: HTMLElement | undefined;
    onclose: (refocus: Refocus) => void;
    /** Sets the folder back to auto; resolves true when the server did. */
    onauto: (s: GroupingSetting) => Promise<boolean>;
  } = $props();

  const CONTROL = 'button, a[href], input, select, textarea, summary, [tabindex], [contenteditable="true"]';
  let panel = $state<HTMLElement>();
  let busy = $state<string | null>(null); // the project id whose request is out
  // After a row went back to auto, the row whose button takes the focus once
  // the snapshot has removed it: the one that took its place, or the last.
  let refocusAt: number | null = null;
  let leaving: string | null = null; // the project id of that row

  // The panel sits above the status bar, which takes several lines on a
  // phone: it may be as tall as the room above the bar, and its list
  // scrolls under the title.
  let room = $state<number | null>(null);
  function measure() {
    const bar = panel?.offsetParent as HTMLElement | null;
    if (bar) room = Math.max(160, Math.floor(bar.getBoundingClientRect().top) - 16);
  }

  onMount(() => {
    measure();
    window.addEventListener('resize', measure);
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (panel?.contains(t) || toggle?.contains(t)) return;
      const control = t instanceof Element && t.closest(CONTROL);
      onclose(control ? 'stay' : 'button');
    };
    document.addEventListener('pointerdown', down, true);
    return () => {
      document.removeEventListener('pointerdown', down, true);
      window.removeEventListener('resize', measure);
    };
  });

  $effect(() => {
    const n = settings.length;
    if (refocusAt === null || settings.some((s) => s.projectId === leaving)) return;
    const at = Math.min(refocusAt, n - 1);
    refocusAt = leaving = null;
    if (at < 0) return; // the list is empty: the status bar closes the panel
    void tick().then(() => panel?.querySelectorAll<HTMLButtonElement>('button.auto')[at]?.focus());
  });

  async function auto(s: GroupingSetting, index: number) {
    if (busy) return;
    busy = s.projectId;
    const ok = await onauto(s);
    if (ok) [refocusAt, leaving] = [index, s.projectId];
    busy = null;
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation(); // the action notice listens for Escape too
    onclose('button');
  }

  function onfocusout(e: FocusEvent) {
    const to = e.relatedTarget as Node | null;
    if (!to || panel?.contains(to) || toggle?.contains(to)) return;
    onclose('stay');
  }
</script>

<div
  bind:this={panel}
  {id}
  class="panel"
  role="dialog"
  aria-labelledby="{id}-title"
  aria-describedby="{id}-lead"
  tabindex="-1"
  style:max-height={room ? `${room}px` : undefined}
  {onkeydown}
  {onfocusout}
  in:fly={{ y: 6, duration: ms(VIEW_MS), easing: cubicOut }}
  out:fade={{ duration: ms(VIEW_MS) }}
>
  <h2 id="{id}-title">Grouping settings</h2>
  <p id="{id}-lead" class="lead">Folders that do not group as sitboard sees them. Back to auto lets sitboard decide again.</p>
  <ul>
    {#each settings as s, i (s.projectId)}
      {@const name = clean(s.name) || 'this folder'}
      <li>
        <span class="name u" title={name}>{name}</span>
        <span class="mode">{s.mode}</span>
        <span class="path u" title={clean(s.path)}>{clean(s.path)}</span>
        <button type="button" class="auto" aria-disabled={busy !== null} onclick={() => auto(s, i)}
          >Back to auto<span class="visually-hidden">{` for ${name}, set to ${s.mode}`}</span></button
        >
      </li>
    {/each}
  </ul>
</div>

<style>
  .panel {
    position: absolute;
    right: var(--gutter);
    bottom: calc(100% + 8px);
    z-index: 3;
    width: min(420px, calc(100vw - 2 * var(--gutter)));
    display: flex;
    flex-direction: column;
    max-height: calc(100dvh - 120px);
    padding: 14px 0 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: var(--text-m);
    color: var(--fg-body);
  }
  .panel:focus {
    outline: none;
  }
  h2 {
    margin: 0;
    padding: 0 16px 6px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--fg);
  }
  .lead {
    margin: 0;
    padding: 0 16px 10px;
    font-size: var(--text-s);
    line-height: 1.5;
    color: var(--muted);
  }
  ul {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: grid;
    grid-template-columns: auto auto minmax(0, 1fr) auto;
    grid-template-areas:
      'name mode . act'
      'path path path act';
    align-items: center;
    gap: 2px 8px;
    padding: 8px 8px 8px 16px;
    border-top: 1px solid var(--rule);
  }
  .name {
    grid-area: name;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--fg);
  }
  .mode {
    grid-area: mode;
    padding: 0 5px;
    border: 1px solid var(--border-hover);
    font-size: var(--text-xs);
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--fg-body);
  }
  .path {
    grid-area: path;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--text-xs);
    color: var(--muted);
  }
  .auto {
    grid-area: act;
    min-height: 32px;
    padding: 0 8px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    white-space: nowrap;
    color: var(--amber);
    border: 1px solid transparent;
    transition:
      border-color var(--press) ease-out,
      color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .auto:hover {
    color: var(--amber-hover);
    border-color: var(--amber-edge);
  }
  .auto:active {
    transform: translateY(1px);
  }
  .auto[aria-disabled='true'] {
    opacity: 0.6;
    cursor: progress;
  }
</style>
