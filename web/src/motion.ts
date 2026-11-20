// Motion shows a change of state, and nothing moves on page load. The one
// exception is the agents' critters (Critter.svelte): a calm loop for working
// and needing-you agents, only while they can be seen.
// Svelte transitions run through the Web Animations API, so a CSS media query
// cannot stop them: every duration here reads prefersReducedMotion.

import { crossfade, fade, fly } from 'svelte/transition';
import { cubicOut } from 'svelte/easing';
import { prefersReducedMotion } from 'svelte/motion';

export const MOVE_MS = 220;
export const VIEW_MS = 150;
export const COUNT_MS = 180;

/** A duration, or 0 when the system asks for reduced motion. */
export function ms(d: number): number {
  return prefersReducedMotion.current ? 0 : d;
}

/** Options for animate:flip on cards that change position. */
export function flipOpts() {
  return { duration: ms(MOVE_MS), easing: cubicOut };
}

/**
 * A card travelling between columns or cells: send on leave, receive on
 * arrive, matched by card id. Without a counterpart a card fades in with a
 * 6 px rise, or fades out.
 */
export function makeMove() {
  return crossfade({
    duration: () => ms(MOVE_MS),
    easing: cubicOut,
    fallback(node, _params, intro) {
      return intro
        ? fly(node, { y: 6, duration: ms(MOVE_MS), easing: cubicOut })
        : fade(node, { duration: ms(160) });
    },
  });
}

/** The amber border of a card that just arrived in Needs you flashes once. */
export function flash(node: HTMLElement): void {
  if (prefersReducedMotion.current || typeof node.animate !== 'function') return;
  node.animate(
    [
      { borderColor: '#ffd35c', backgroundColor: 'rgb(245 197 66 / 14%)' },
      { borderColor: 'rgb(245 197 66 / 55%)', backgroundColor: '#101219' },
    ],
    { duration: 250, easing: 'ease-out' },
  );
}
