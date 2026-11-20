<script lang="ts">
  // Project rail: the tiles | rail switch, "all projects", then the area groups
  // with their projects; a project with parts opens to show them indented.
  // On a narrow screen the rail folds into a button that opens it as a modal
  // drawer over the board.
  import { tick, untrack } from 'svelte';
  import { slide } from 'svelte/transition';
  import { dotsOf, sameSel, selName, tileNote, type Grouping, type Sel } from '../data/groups';
  import { loadOpen, saveOpen, type Grouping as Mode } from '../data/view';
  import { focusKeeper, isolate, moveFocus, onKeys, tabStop } from '../keys';
  import { ms, VIEW_MS } from '../motion';
  import Count from './Count.svelte';
  import Dots from './Dots.svelte';
  import type { GroupingMode } from '../contract.gen';
  import AlwaysGrouping from './AlwaysGrouping.svelte';
  import ForgetGrouping from './ForgetGrouping.svelte';
  import GroupSwitch from './GroupSwitch.svelte';

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

  const all = $derived(dotsOf(g.cards));
  const allNote = $derived(tileNote(g.cards));
  const selTop = $derived(sel.kind === 'top' ? sel.id : sel.kind === 'part' ? g.topOf(sel.id) : null);

  let opened = $state<Set<string>>(loadOpen());
  let drawer = $state(false);
  let nav = $state<HTMLElement>();
  let opener = $state<HTMLButtonElement>();
  let backdrop = $state<HTMLButtonElement>();
  let focused = $state<string | null>(null);

  const isOpen = (id: string) => opened.has(id);
  function setOpen(id: string, open: boolean) {
    const next = new Set(opened);
    if (open) next.add(id);
    else next.delete(id);
    opened = next;
    saveOpen(next);
  }

  // A newly selected project shows its parts, wherever it was selected (tiles
  // or rail). Only a change of selection opens it, so the viewer can close it.
  let lastTop: string | null = null;
  $effect(() => {
    const id = selTop;
    if (id === lastTop) return;
    lastTop = id;
    if (!id || !g.topById.get(id)?.entries.length) return;
    untrack(() => {
      if (!isOpen(id)) setOpen(id, true);
    });
  });

  const keyOf = (s: Sel) => (s.kind === 'all' ? 'all' : s.kind === 'area' ? `g:${s.key}` : s.kind === 'top' ? `p:${s.id}` : `q:${s.id}`);
  const rowKeys = $derived([
    'all',
    ...g.groups.flatMap((grp) => [
      `g:${grp.key}`,
      ...grp.projects.flatMap((p) => [`p:${p.id}`, ...(p.parts.length && isOpen(p.id) ? [...p.parts.map((e) => `q:${e.id}`), ...(sets(p.id) ? [`a:${p.id}`] : []), ...(forgets(p.id) ? [`f:${p.id}`] : [])] : [])]),
    ]),
  ]);
  const stop = $derived(tabStop(rowKeys, focused, keyOf(sel)));
  // "Always group" and "Forget grouping" show under the parts of the selected
  // project only; forget not while it is set to always ("Back to auto" first).
  const modeOf = (id: string): GroupingMode => settings?.get(id) ?? 'auto';
  const sets = (id: string) => !!onsetting && selTop === id && !!g.topById.get(id)?.parts.length;
  const forgets = (id: string) => !!onforget && selTop === id && !!g.topById.get(id)?.parts.length && modeOf(id) !== 'always';

  async function forget(p: { id: string; name: string }, act = onforget) {
    const ok = (await act?.(p)) ?? false;
    if (ok) nav?.querySelector<HTMLElement>(`[data-key="p:${CSS.escape(p.id)}"]`)?.focus();
    return ok;
  }

  // A row that vanishes, or parts that close under the focus, hand focus on.
  const keeper = focusKeeper(() => nav, '[data-key]');
  $effect.pre(() => {
    void g;
    void opened;
    keeper.before();
  });
  $effect(() => {
    void g;
    void opened;
    keeper.after();
  });

  // The open drawer is modal: everything behind it is inert until it closes.
  $effect(() => {
    if (!drawer || !nav) return;
    return isolate(nav, backdrop ? [backdrop] : []);
  });

  function pick(s: Sel) {
    onselect(s);
    if (drawer) close();
  }

  async function close() {
    drawer = false;
    // The opener is inert until the drawer's isolation lifts with this update.
    await tick();
    opener?.focus();
  }

  async function toggleDrawer() {
    if (drawer) return close();
    drawer = true;
    // Into the drawer, on the selected row, so the arrows and Escape work at once.
    await tick();
    nav?.querySelector<HTMLElement>('[data-key][tabindex="0"]')?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && drawer) {
      e.stopPropagation();
      close();
      return;
    }
    const el = document.activeElement as HTMLElement | null;
    const key = el?.dataset.key ?? '';
    // Right opens a project's parts, then moves into them; Left closes them,
    // or goes back up from a part to its project.
    if ((e.key === 'ArrowRight' || e.key === 'ArrowLeft') && key) {
      const id = key.slice(2);
      if (key.startsWith('p:') && el?.hasAttribute('aria-expanded')) {
        e.preventDefault();
        if (e.key === 'ArrowLeft') setOpen(id, false);
        else if (!isOpen(id)) setOpen(id, true);
        else nav?.querySelector<HTMLElement>(`[data-parent="${CSS.escape(id)}"]`)?.focus();
        return;
      }
      if (key.startsWith('q:') && e.key === 'ArrowLeft') {
        e.preventDefault();
        nav?.querySelector<HTMLElement>(`[data-key="p:${CSS.escape(el?.dataset.parent ?? '')}"]`)?.focus();
        return;
      }
    }
    if (nav) moveFocus(e, nav, '[data-key]', 'y');
  }
</script>

<button
  bind:this={opener}
  type="button"
  class="opener"
  aria-expanded={drawer}
  aria-controls="project-rail"
  onclick={toggleDrawer}
  onkeydown={(e) => {
    if (e.key === 'Escape' && drawer) {
      e.stopPropagation();
      close();
    }
  }}
>
  <span class="label">Projects</span>{' '}<span class="cur u">{selName(g, sel)}</span>
</button>
{#if drawer}
  <button bind:this={backdrop} type="button" class="backdrop" tabindex="-1" aria-label="Close projects" onclick={close}></button>
{/if}

<nav
  id="project-rail"
  class="rail"
  class:open={drawer}
  aria-label="Projects"
  role={drawer ? 'dialog' : undefined}
  aria-modal={drawer ? 'true' : undefined}
  bind:this={nav}
  {@attach onKeys(onkeydown, (e) => (focused = (e.target as HTMLElement).dataset.key ?? focused))}
>
  <div class="rhead"><GroupSwitch {mode} {onmode} /></div>
  <ul>
    <li>
      <button
        type="button"
        class="row"
        data-key="all"
        tabindex={stop === 'all' ? 0 : -1}
        aria-current={sel.kind === 'all' ? 'true' : undefined}
        onclick={() => pick({ kind: 'all' })}
      >
        <span class="name">all projects</span><Dots dots={all.dots.slice(0, 8)} small />{' '}<span class="n"
          ><Count value={g.cards.length} /></span
        ><span class="visually-hidden">{', '}{allNote}</span>
      </button>
    </li>
    {#each g.groups as grp (grp.key)}
      {@const gsel: Sel = { kind: 'area', key: grp.key }}
      <li class="group">
        <button
          type="button"
          class="ghead"
          data-key="g:{grp.key}"
          tabindex={stop === `g:${grp.key}` ? 0 : -1}
          aria-current={sameSel(sel, gsel) ? 'true' : undefined}
          onclick={() => pick(gsel)}
        >
          <span class="glabel u" title={grp.label}>{grp.label}</span>{' '}<span class="gn"><Count value={grp.count} /></span><span
            class="rule"
            aria-hidden="true"
          ></span>
        </button>
        <ul>
          {#each grp.projects as p (p.id)}
            {@const hasParts = p.parts.length > 0}
            {@const open = hasParts && isOpen(p.id)}
            <li>
              <div class="prow">
                {#if hasParts}
                  <button
                    type="button"
                    class="tog"
                    tabindex="-1"
                    aria-label="{open ? 'Hide' : 'Show'} the parts of {p.name}"
                    aria-expanded={open}
                    onclick={() => setOpen(p.id, !open)}>{open ? '▾' : '▸'}</button
                  >
                {/if}
                <button
                  type="button"
                  class="row"
                  data-key="p:{p.id}"
                  tabindex={stop === `p:${p.id}` ? 0 : -1}
                  aria-current={sel.kind === 'top' && sel.id === p.id ? 'true' : undefined}
                  aria-expanded={hasParts ? open : undefined}
                  class:within={selTop === p.id && sel.kind === 'part'}
                  onclick={() => pick({ kind: 'top', id: p.id })}
                >
                  <span class="name u" title={p.name}>{p.name}</span><Dots dots={p.dots.slice(0, 8)} small />{' '}<span class="n"
                    ><Count value={p.count} /></span
                  ><span class="visually-hidden">{', '}{p.note}</span>
                </button>
              </div>
              {#if open}
                <ul class="parts" transition:slide={{ duration: ms(VIEW_MS) }}>
                  {#each p.parts as e (e.id)}
                    <li>
                      <button
                        type="button"
                        class="row part"
                        data-key="q:{e.id}"
                        data-parent={p.id}
                        tabindex={stop === `q:${e.id}` ? 0 : -1}
                        aria-current={sel.kind === 'part' && sel.id === e.id ? 'true' : undefined}
                        onclick={() => pick({ kind: 'part', id: e.id })}
                      >
                        <span class="name u" title={e.name}>{e.name}</span><Dots dots={e.dots.slice(0, 8)} small />{' '}<span
                          class="n"><Count value={e.count} /></span
                        ><span class="visually-hidden">{', '}{e.note}</span>
                      </button>
                    </li>
                  {/each}
                  {#if sets(p.id) && onsetting}
                    {@const ap = { id: p.id, name: p.name }}
                    <li>
                      {#key p.id}<AlwaysGrouping
                          compact
                          mode={modeOf(p.id)}
                          dataKey="a:{p.id}"
                          tabindex={stop === `a:${p.id}` ? 0 : -1}
                          onset={(m) => onsetting(ap, m)}
                        />{/key}
                    </li>
                  {/if}
                  {#if forgets(p.id)}
                    {@const fp = { id: p.id, name: p.name }}
                    <li>
                      {#key p.id}<ForgetGrouping
                          compact
                          name={p.name}
                          dataKey="f:{p.id}"
                          tabindex={stop === `f:${p.id}` ? 0 : -1}
                          onforget={() => forget(fp)}
                          onnever={onsetting ? () => forget(fp, (q) => onsetting(q, 'never')) : undefined}
                        />{/key}
                    </li>
                  {/if}
                </ul>
              {/if}
            </li>
          {/each}
        </ul>
      </li>
    {/each}
  </ul>
</nav>

<style>
  .rail {
    position: sticky;
    top: 0;
    align-self: start;
    max-height: 100vh;
    overflow-y: auto;
    width: 256px;
    padding: 12px 0 24px;
  }
  .rhead {
    padding: 0 16px 8px;
  }
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-height: 40px;
    padding: 0 16px 0 24px;
    font-size: var(--text-m);
    color: var(--fg);
    transition:
      background-color var(--press) ease-out,
      transform var(--press) ease-out;
  }
  .row:hover,
  .ghead:hover {
    background: rgb(221 218 207 / 4%);
  }
  .row:active,
  .ghead:active {
    transform: translateY(1px);
  }
  .row[aria-current='true'],
  .ghead[aria-current='true'] {
    background: var(--amber-soft);
  }
  /* Selected: an amber bar at the left edge. The toggle alone uses ▸ and ▾. */
  .row,
  .ghead {
    position: relative;
  }
  .row[aria-current='true']::before,
  .ghead[aria-current='true']::before,
  .row.within::before {
    content: '';
    position: absolute;
    top: 8px;
    bottom: 8px;
    left: 0;
    width: 2px;
    background: var(--amber);
  }
  .row.within::before {
    opacity: 0.45;
  }
  .row:focus-visible,
  .ghead:focus-visible,
  .tog:focus-visible {
    outline-offset: -2px;
  }
  .name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row :global(.dots) {
    margin-left: auto;
    flex: none;
  }
  .n {
    flex: none;
    min-width: 18px;
    text-align: right;
    color: var(--muted);
  }
  .part {
    padding-left: 40px;
    font-size: var(--text-s);
    color: var(--fg-body);
  }
  .prow {
    position: relative;
  }
  .tog {
    position: absolute;
    top: 0;
    bottom: 0;
    left: 4px;
    z-index: 1;
    width: 18px;
    font-size: var(--text-xs);
    color: var(--muted);
    text-align: center;
  }
  .tog:hover {
    color: var(--fg);
  }
  .ghead {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-height: 32px;
    margin-top: 10px;
    padding: 0 16px;
    font-size: var(--text-xs);
    font-weight: 700;
    letter-spacing: var(--label-tracking);
    text-transform: uppercase;
    color: var(--muted);
  }
  .glabel {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .gn {
    flex: none;
    font-weight: 400;
  }
  .rule {
    flex: 1;
    min-width: 12px;
    height: 1px;
    background: var(--rule-strong);
  }
  .opener,
  .backdrop {
    display: none;
  }

  /* Narrow screens: the rail folds into a button and opens over the board. */
  @media (max-width: 899px) {
    .opener {
      display: flex;
      align-items: center;
      gap: 10px;
      width: 100%;
      min-height: 44px;
      padding: 0 var(--gutter);
      border-bottom: 1px solid var(--rule);
      font-size: var(--text-m);
      color: var(--fg);
    }
    .opener .label {
      color: var(--muted);
    }
    .cur {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .opener[aria-expanded='true'] {
      background: var(--amber-soft);
    }
    .rail {
      display: none;
    }
    .rail.open {
      display: block;
      position: fixed;
      top: 0;
      bottom: 0;
      left: 0;
      z-index: 5;
      width: min(300px, 86vw);
      max-height: none;
      /* The wide rail aligns to the top of its column; the drawer fills the screen height. */
      align-self: stretch;
      background: var(--surface);
      border-right: 1px solid var(--border);
    }
    .backdrop {
      display: block;
      position: fixed;
      inset: 0;
      z-index: 4;
      background: rgb(10 11 16 / 70%);
    }
  }
</style>
