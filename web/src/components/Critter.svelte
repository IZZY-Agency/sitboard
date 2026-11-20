<script lang="ts">
  // The agent's critter, in place of the state dot. Its shape comes from the
  // card id; its eyes, outline and the line under its feet carry the state.
  // `mood` is its state (critterState). Decoration only: the state is said in words next to it, as before.
  import { getContext, untrack } from 'svelte';
  import { prefersReducedMotion } from 'svelte/motion';
  import { critter, critterPhase, drawing, EYES, MOVE_MS, MOVES, union, type CritterState, type EyeKind } from '../data/critter';
  import { onScreen } from '../onscreen.svelte';

  let { id, mood, size = 24 }: { id: string; mood: CritterState; size?: number } = $props();

  const m = $derived(critter(id));
  const d = $derived(drawing(m, size <= 20));
  const phase = $derived(critterPhase(id));
  const kinds: EyeKind[] = ['eo', 'ew', 'ec', 'eh', 'ex'];

  // It moves only while it can be seen: on screen (onscreen.svelte.ts) and in a tab that is shown.
  // null until the page's observer first reports on it.
  let el = $state<HTMLSpanElement>();
  let near = $state<boolean | null>(null);
  $effect(() => {
    if (el) return onScreen.watch(el, (v) => (near = v));
  });
  const shown = $derived(near === true && onScreen.tabShown);
  const known = $derived(near !== null);

  // The move on a change of state: the page sets a token when this card's state changes.
  // It plays only if the critter can be seen when the token comes (or when the
  // observer first reports on a critter that just mounted), never later when it scrolls in.
  const moves = getContext<ReadonlyMap<string, number> | undefined>(MOVES);
  const token = $derived(moves?.get(id) ?? 0);
  let tx = $state<CritterState | ''>('');
  // Ends the move; it outlives the token, which the page may drop while the move plays.
  let end: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    if (!token || !known || prefersReducedMotion.current) return;
    if (!untrack(() => shown)) return;
    // No empty frame before it: a new state always brings a different move
    // class, and a different animation name starts again by itself.
    untrack(() => (tx = mood));
    clearTimeout(end);
    end = setTimeout(() => (tx = ''), MOVE_MS);
  });
  $effect(() => () => clearTimeout(end));

  // Every part that moves is an HTML box over a still drawing: Chrome animates
  // a box on the compositor, but a moving part inside an SVG is laid out again
  // on the main thread every frame. Each box covers the critter's 24 unit
  // square; --u is one unit in CSS pixels, and each part moves about the box
  // around its own drawing, as it did inside the SVG.
  const u = $derived(size / 24);
  const px = (v: number) => `${+(v * u).toFixed(4)}px`;
  // Where the open eyes rest in this state: the eyes blink about all their shapes as placed.
  const at = $derived<readonly [number, number]>(
    mood === 'working' || mood === 'working_failing' ? d.gaze.work : mood === 'with_coordinator' ? [d.gaze.look, 0] : mood === 'in_review' ? [0, -d.gaze.up] : [0, 0],
  );
  const eyes = $derived(union(d.box.eyes, [d.box.eo[0] + at[0], d.box.eo[1] + at[1], d.box.eo[2] + at[0], d.box.eo[3] + at[1]]));
</script>

<span
  bind:this={el}
  class="cr st-{mood}{tx ? ` tx tx-${tx}` : ''}"
  class:still={!shown}
  style:width="{size}px"
  style:height="{size}px"
  style:--u="{u}px"
  style:--ph="{phase}s"
  style:--wx={px(d.gaze.work[0])}
  style:--wy={px(d.gaze.work[1])}
  style:--up={px(-d.gaze.up)}
  style:--look={px(d.gaze.look)}
  style:--bo="{px((d.box.body[0] + d.box.body[2]) / 2)} {px(d.box.body[3])}"
  style:--eo="{px((eyes[0] + eyes[2]) / 2)} {px((eyes[1] + eyes[3]) / 2)}"
  aria-hidden="true"
  data-critter={m.key}
  data-state={mood}
>
  <svg class="gnd" viewBox="0 0 24 24" focusable="false"><path d={d.ground} /></svg>
  <span class="bT">
    <span class="bL">
      <svg class="shape" viewBox="0 0 24 24" focusable="false">
        {#if d.tops}<path d={d.tops} />{/if}
        <path d={d.body} />
      </svg>
      {#if d.mark}<svg class="mk" viewBox="0 0 24 24" focusable="false"><path d={d.mark} /></svg>{/if}
      <span class="eyes">
        {#each kinds as k (k)}
          {#if k === 'eo'}
            <span class="look"><svg class="eg eo" class:on={EYES[mood] === k} viewBox="0 0 24 24" focusable="false"><path d={d.eyes[k]} /></svg></span>
          {:else}
            <svg class="eg {k}" class:on={EYES[mood] === k} viewBox="0 0 24 24" focusable="false"><path d={d.eyes[k]} /></svg>
          {/if}
        {/each}
      </span>
    </span>
  </span>
  <svg class="pip" viewBox="0 0 24 24" focusable="false"><path d={d.pip} /></svg>
</span>

<style>
  /* Cream body, grey when idle; holes in the colour behind (--hole, set by
     each place); the state colour only on the ground line, where the dot was. */
  .cr {
    --tone: var(--state-idle);
    --body: var(--fg);
    position: relative;
    display: block;
    flex: none;
  }
  /* The boxes that move, each over the critter's whole square. */
  .bT,
  .bL,
  .eyes,
  .look {
    position: absolute;
    inset: 0;
  }
  /* The still drawings: one SVG each, on the same square. */
  .cr svg {
    position: absolute;
    inset: 0;
    display: block;
    width: 100%;
    height: 100%;
    overflow: visible;
  }
  .st-needs_you,
  .st-blocked,
  .st-unseen {
    --tone: var(--state-needs);
  }
  .st-working,
  .st-stale {
    --tone: var(--state-working);
  }
  /* Busy with failing checks: working, standing on the failing red. */
  .st-working_failing {
    --tone: var(--state-failing);
  }
  .st-with_coordinator {
    --tone: var(--state-coordinator);
  }
  .st-in_review {
    --tone: var(--state-review);
  }
  .st-done {
    --tone: var(--state-done);
  }
  .st-idle {
    --tone: var(--state-idle);
    --body: var(--muted);
  }
  .st-failing {
    --tone: var(--state-failing);
  }
  .st-unknown {
    --tone: var(--muted);
    --body: var(--muted);
  }

  .gnd {
    fill: var(--tone);
  }
  .shape {
    fill: var(--body);
    stroke: var(--body);
    stroke-width: 1.2;
    stroke-linejoin: round;
  }
  /* Hollow means stuck: blocked on you, or busy with no sign of life. */
  :is(.st-blocked, .st-stale, .st-unknown) .shape {
    fill: none;
  }
  .st-unknown .shape {
    stroke-dasharray: 2 1.6;
  }
  .mk {
    fill: var(--hole, var(--surface));
    transition: opacity 200ms ease-out;
  }
  :is(.st-blocked, .st-stale, .st-unknown) .mk {
    opacity: 0;
  }

  /* The eyes sit on whole screen pixels at both sizes, filled with the colour behind. */
  .eg {
    opacity: 0;
    fill: var(--hole, var(--surface));
    shape-rendering: crispEdges;
    transition: opacity 160ms ease-out;
  }
  :is(.st-blocked, .st-stale) .eg {
    fill: var(--body);
  }
  .eg.on {
    opacity: 1;
  }
  /* Open eyes look at the work, across at the coordinator, up at the review:
     a whole pixel or two each, as far as the body leaves room (drawing().gaze). */
  .look {
    transition: transform 320ms var(--ease-out);
  }
  :is(.st-working, .st-working_failing) .look {
    transform: translate(var(--wx), var(--wy));
  }
  .st-with_coordinator .look {
    transform: translate(var(--look), 0);
  }
  .st-in_review .look {
    transform: translate(0, var(--up));
  }

  /* A turn you have not seen: a small cream square in the corner. */
  .pip {
    opacity: 0;
    fill: var(--fg);
    stroke: var(--hole, var(--surface));
    stroke-width: 1.5;
    paint-order: stroke;
    transition: opacity 200ms ease-out;
  }
  .st-unseen .pip {
    opacity: 1;
  }

  /* The body moves about the middle of its feet, the eyes about their middle. */
  .bT,
  .bL {
    transform-origin: var(--bo);
  }
  .eyes {
    transform-origin: var(--eo);
  }

  /* The calm loops. Working and needing you move; in review and with the
     coordinator blink now and then; the rest are still. */
  :is(.st-working, .st-working_failing) .bL {
    animation: bob 1.6s ease-in-out infinite;
    animation-delay: var(--ph);
  }
  :is(.st-working, .st-working_failing) .eyes {
    animation: blinkLoop 4.5s linear infinite;
    animation-delay: var(--ph);
  }
  :is(.st-needs_you, .st-unseen) .bL {
    animation: hopLoop 2.6s ease-out infinite;
    animation-delay: var(--ph);
  }
  .st-blocked .bL {
    animation: wiggleLoop 2.8s ease-in-out infinite;
    animation-delay: var(--ph);
  }
  :is(.st-needs_you, .st-blocked, .st-unseen) .gnd {
    animation: pulse 2s ease-in-out infinite;
    animation-delay: var(--ph);
  }
  :is(.st-with_coordinator, .st-in_review) .eyes {
    animation: blinkLoop 7s linear infinite;
    animation-delay: var(--ph);
  }

  /* Out of sight (scrolled away, not rendered, a hidden tab): no loop at all. */
  .still :global(*) {
    animation: none !important;
  }

  /* The move on a change of state, under a second. */
  .tx.tx .eyes {
    animation: blink 260ms ease-in-out;
  }
  .tx-working .bT {
    animation: stretch 480ms var(--ease-out);
  }
  :is(.tx-needs_you, .tx-blocked, .tx-unseen, .tx-done) .bT {
    animation: hop 620ms var(--ease-out);
  }
  :is(.tx-failing, .tx-working_failing) .bT {
    animation: shake 460ms ease-in-out;
  }
  :is(.tx-idle, .tx-stale) .bT {
    animation: sink 560ms var(--ease-out);
  }

  /* Distances are in the critter's own units (--u), as they were inside the SVG. */
  @keyframes pulse {
    50% {
      opacity: 0.35;
    }
  }
  @keyframes blinkLoop {
    0%,
    93%,
    100% {
      transform: scaleY(1);
    }
    95.5% {
      transform: scaleY(0.1);
    }
  }
  @keyframes blink {
    0%,
    100% {
      transform: scaleY(1);
    }
    45% {
      transform: scaleY(0.1);
    }
  }
  @keyframes bob {
    0%,
    100% {
      transform: translateY(0);
    }
    50% {
      transform: translateY(calc(var(--u) * -1.1));
    }
  }
  @keyframes hopLoop {
    0%,
    70%,
    86%,
    100% {
      transform: translateY(0);
    }
    78% {
      transform: translateY(calc(var(--u) * -2.6));
    }
    91% {
      transform: translateY(calc(var(--u) * -0.7));
    }
    96% {
      transform: translateY(0);
    }
  }
  @keyframes hop {
    0%,
    70%,
    100% {
      transform: translateY(0);
    }
    35% {
      transform: translateY(calc(var(--u) * -2.8));
    }
    85% {
      transform: translateY(calc(var(--u) * -0.7));
    }
  }
  @keyframes wiggleLoop {
    0%,
    80%,
    96%,
    100% {
      transform: rotate(0);
    }
    84% {
      transform: rotate(-7deg);
    }
    88% {
      transform: rotate(6deg);
    }
    92% {
      transform: rotate(-4deg);
    }
  }
  @keyframes shake {
    0%,
    100% {
      transform: translateX(0);
    }
    20% {
      transform: translateX(calc(var(--u) * -1.5));
    }
    40% {
      transform: translateX(calc(var(--u) * 1.5));
    }
    60% {
      transform: translateX(calc(var(--u) * -1));
    }
    80% {
      transform: translateX(calc(var(--u) * 0.6));
    }
  }
  @keyframes stretch {
    0%,
    100% {
      transform: scale(1, 1);
    }
    40% {
      transform: scale(0.93, 1.1);
    }
  }
  @keyframes sink {
    0%,
    100% {
      transform: translateY(0) scale(1, 1);
    }
    45% {
      transform: translateY(calc(var(--u) * 0.6)) scale(1.06, 0.9);
    }
  }
</style>
