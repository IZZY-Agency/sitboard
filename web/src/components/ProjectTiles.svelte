<script lang="ts">
  // Project tiles: "all projects", one tile per project with work in hand, the
  // idle projects in one dashed box, and the tiles | rail switch at the end.
  // Selecting a project with parts opens a second, smaller row of its parts.
  import { slide } from 'svelte/transition';
  import { bandScrolls, selName, tileBand, type Grouping, type Sel } from '../data/groups';
  import type { Grouping as Mode } from '../data/view';
  import { focusKeeper, moveFocus, onKeys, tabStop } from '../keys';
  import { ms, VIEW_MS } from '../motion';
  import Count from './Count.svelte';
  import { openIssuesText } from '../data/issues';
  import Dots from './Dots.svelte';
  import type { GroupingMode } from '../contract.gen';
  import AlwaysGrouping from './AlwaysGrouping.svelte';
  import ForgetGrouping from './ForgetGrouping.svelte';
  import GroupSwitch from './GroupSwitch.svelte';
  import PartsRow, { type PartChip } from './PartsRow.svelte';

  let {
    g,
    sel,
    onselect,
    mode,
    onmode,
    onforget,
    onsetting,
    settings,
  }: {
    g: Grouping;
    sel: Sel;
    onselect: (s: Sel) => void;
    mode: Mode;
    onmode: (m: Mode) => void;
    /** Forgets a project's grouping; resolves true when the server did. */
    onforget?: (p: { id: string; name: string }) => Promise<boolean>;
    /** Sets a folder's grouping; resolves true when the server did. */
    onsetting?: (p: { id: string; name: string }, mode: GroupingMode) => Promise<boolean>;
    /** project id -> its setting, for folders set to always or never. */
    settings?: ReadonlyMap<string, GroupingMode>;
  } = $props();

  const band = $derived(tileBand(g));
  // The selected top-level project, whether it or one of its parts is selected.
  const topId = $derived(sel.kind === 'top' ? sel.id : sel.kind === 'part' ? g.topOf(sel.id) : null);
  const current = $derived(sel.kind === 'all' ? 'all' : sel.kind === 'area' ? 'area' : (topId ?? ''));
  const top = $derived(topId ? g.topById.get(topId) : undefined);
  const parts = $derived(top ? top.entries : []);
  const chips = $derived<PartChip[]>(
    top ? [{ key: `top:${top.id}`, name: 'all parts', count: top.cards.length }, ...parts.map((p) => ({ key: `part:${p.id}`, name: p.name, count: p.count }))] : [],
  );
  const chipActive = $derived(sel.kind === 'part' ? `part:${sel.id}` : top ? `top:${top.id}` : '');
  // An area chosen in the rail stays visible here, and pressing it clears it.
  const area = $derived(sel.kind === 'area' ? selName(g, sel) : null);

  let strip = $state<HTMLElement>();
  let width = $state(0);
  let focused = $state<string | null>(null);
  const tiles = $derived([band.all, ...band.active]);
  const keys = $derived([...(area ? ['area'] : []), ...tiles.map((t) => t.key), ...band.idle.map((t) => t.key)]);
  const stop = $derived(tabStop(keys, focused, current));
  // Past two rows the band stops wrapping and scrolls sideways; a narrow screen wraps further first.
  const scroll = $derived(bandScrolls(width, tiles.length + (area ? 1 : 0) + (band.idle.length ? 2 : 0)));
  // In the scrolling band tiles fill row by row, as the arrow keys move.
  const cols = $derived(Math.ceil((tiles.length + (area ? 1 : 0)) / 2));

  // A tile that vanishes or moves into the idle box hands focus on.
  const keeper = focusKeeper(() => strip, 'button');
  $effect.pre(() => {
    void band;
    keeper.before();
  });
  $effect(() => {
    void band;
    keeper.after();
  });

  // After forgetting, focus goes back to the project's tile; the parts row
  // leaves with the next snapshot.
  let asking = $state(false); // the forget confirm is open: it takes the row

  async function forget(p: { id: string; name: string }, act = onforget) {
    const ok = (await act?.(p)) ?? false;
    if (ok) (strip?.querySelector<HTMLElement>('[aria-pressed="true"]') ?? strip?.querySelector<HTMLElement>('button'))?.focus();
    return ok;
  }

  function pickChip(key: string) {
    const [kind, id] = [key.slice(0, key.indexOf(':')), key.slice(key.indexOf(':') + 1)];
    onselect(kind === 'part' ? { kind: 'part', id } : { kind: 'top', id });
  }
</script>

<div class="band">
  <div
    class="tiles"
    class:scroll
    style:--cols={cols}
    role="toolbar"
    aria-label="Projects"
    bind:this={strip}
    bind:clientWidth={width}
    {@attach onKeys(
      (e) => strip && moveFocus(e, strip, 'button', 'x'),
      (e) => (focused = (e.target as HTMLElement).dataset.key ?? focused),
    )}
  >
    {#if area}
      <button type="button" class="tile area" data-key="area" tabindex={stop === 'area' ? 0 : -1} aria-pressed="true" onclick={() => onselect({ kind: 'all' })}>
        <span class="top"><span class="name u" title="area: {area}">area: {area}</span></span>
        <span class="note">press to show all projects</span>
      </button>
    {/if}
    {#each tiles as t (t.key)}
      <button
        type="button"
        class="tile"
        class:needs={t.needs}
        data-key={t.key}
        tabindex={t.key === stop ? 0 : -1}
        aria-pressed={t.key === current}
        onclick={() => onselect(t.sel)}
      >
        <span class="top"><span class="name u" title={t.name}>{t.name}</span>{' '}<span class="n"><Count value={t.count} /></span></span>
        <Dots dots={t.dots} more={t.more} />
        <span class="note" title={t.note}>{t.note}</span>
        {#if t.issues !== null}<span class="note iss">{openIssuesText(t.issues)}</span>{/if}
      </button>
    {/each}
    {#if band.idle.length}
      <div class="idle" role="group" aria-label="Idle projects">
        <span class="ihead" aria-hidden="true">Idle projects · <Count value={band.idle.length} /></span>
        <span class="ilist"
          >{#each band.idle as t (t.key)}<button
              type="button"
              class="ibtn"
              data-key={t.key}
              tabindex={t.key === stop ? 0 : -1}
              aria-pressed={t.key === current}
              onclick={() => onselect(t.sel)}
              ><span class="dot tone-idle" aria-hidden="true"></span><span class="name u" title={t.name}>{t.name}</span>{' '}<span
                class="n"><span class="visually-hidden">{'agents '}</span>{t.count}</span
              >{#if t.issues !== null && t.issues > 0}<span class="iss" title={openIssuesText(t.issues)}
                  ><span class="visually-hidden">{', '}</span>{t.issues}<span class="visually-hidden">{t.issues === 1 ? ' open issue' : ' open issues'}</span></span
                >{/if}</button
            >{/each}</span
        >
      </div>
    {/if}
  </div>
  <GroupSwitch {mode} {onmode} />
</div>

{#if top && parts.length}
  <div class="parts" transition:slide={{ duration: ms(VIEW_MS) }}>
    <PartsRow label="Parts of {top.name}" {chips} active={chipActive} onselect={pickChip}>
      {#snippet end()}
        {#if top.parts.length}
          {@const p = { id: top.id, name: top.name }}
          {@const mode = settings?.get(top.id) ?? 'auto'}
          {#key top.id}<div class="acts">
              {#if onsetting && !(asking && mode !== 'always')}<AlwaysGrouping {mode} onset={(m) => onsetting(p, m)} />{/if}
              {#if onforget && mode !== 'always'}<ForgetGrouping
                  bind:asking
                  name={top.name}
                  onforget={() => forget(p)}
                  onnever={onsetting ? () => forget(p, (q) => onsetting(q, 'never')) : undefined}
                />{/if}
            </div>{/key}
        {/if}
      {/snippet}
    </PartsRow>
  </div>
{/if}

<style>
  /* Always group, then Forget grouping; a confirm that opens takes the row. */
  .acts {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    justify-content: flex-end;
    gap: 4px;
  }

  .band {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 12px var(--gutter);
    border-bottom: 1px solid var(--rule);
  }
  .tiles {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: stretch;
    gap: 8px;
  }
  /* Two rows at most, then sideways. */
  /* Tiles fill two rows left to right, the way the arrow keys move; the idle
     box takes the last column across both rows. */
  .tiles.scroll {
    display: grid;
    grid-template-columns: repeat(var(--cols), 168px) auto;
    grid-template-rows: repeat(2, minmax(76px, auto));
    overflow-x: auto;
    padding-bottom: 4px;
  }
  .tile {
    flex: 1 1 150px;
    min-width: 150px;
    max-width: 260px;
    min-height: 76px;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    gap: 6px;
    padding: 9px 11px;
    background: var(--surface);
    border: 1px solid var(--border);
    transition:
      background-color var(--press) ease-out,
      border-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .scroll .tile {
    max-width: none;
  }
  .tile:hover {
    border-color: var(--border-hover);
  }
  .tile:active,
  .ibtn:active {
    transform: translateY(1px);
  }
  .tile.needs {
    border-color: var(--amber-line);
  }
  .tile[aria-pressed='true'] {
    background: var(--amber-soft);
    border-color: var(--amber);
  }
  .tile:focus-visible,
  .ibtn:focus-visible {
    outline-offset: -2px;
  }
  .top {
    display: flex;
    align-items: baseline;
    gap: 8px;
    min-width: 0;
  }
  .name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tile .name {
    font-size: var(--text-m);
    color: var(--fg);
  }
  .n {
    margin-left: auto;
    flex: none;
    font-size: var(--text-m);
    color: var(--muted);
  }
  .note {
    font-size: var(--text-xs);
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .idle {
    flex: 2 1 320px;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 9px 11px 3px;
    border: 1px dashed var(--border);
  }
  /* The idle box keeps the band's two rows and scrolls its own list. */
  .scroll .idle {
    grid-column: -2;
    grid-row: 1 / span 2;
    width: 360px;
    max-height: 176px;
    overflow-y: auto;
  }
  .ihead {
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--muted);
  }
  .ilist {
    display: flex;
    flex-wrap: wrap;
    gap: 0 2px;
  }
  .ibtn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    min-height: 36px;
    padding: 0 8px;
    font-size: var(--text-m);
    color: var(--fg-body);
    border-bottom: 2px solid transparent;
    transition:
      color var(--press) ease-out,
      border-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .ibtn:hover {
    color: var(--fg);
  }
  .ibtn[aria-pressed='true'] {
    color: var(--fg);
    border-bottom-color: var(--amber);
  }
  /* Narrow: the switch takes its own line above the tiles. */
  @media (max-width: 599px) {
    .band {
      flex-direction: column-reverse;
      align-items: stretch;
    }
    .band > :global(.switch) {
      align-self: flex-end;
    }
    .tiles {
      flex: none;
    }
  }
  .ibtn .n {
    margin-left: 2px;
    font-size: var(--text-s);
  }
  /* Open issues, as on the rail: a small ring and the number. */
  .ibtn .iss {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    margin-left: 6px;
    font-size: var(--text-xs);
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .ibtn .iss::before {
    content: '';
    width: 7px;
    height: 7px;
    border: 1px solid currentColor;
    border-radius: 50%;
  }
</style>
