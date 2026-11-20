// Whether something can be seen, for motion that runs only where it can be.
// One IntersectionObserver for the page. On screen means inside the
// viewport, and inside every scrolled column or strip around it, with a
// margin so a critter is already moving when it scrolls in. Something not
// rendered (display: none, a view that is not shown) is not on screen.
// A hidden browser tab hides everything.
//
// The observer looks, then lets go. While it watches anything, Chrome runs
// style, layout and paint on the main thread on every frame that a
// compositor animation draws, which turns the critters' calm loops from free
// into a steady cost. So it observes every critter only when something may
// have moved them (a scroll, a resize, the page changing, the tab coming
// back), takes the one report each gives, and unobserves it at once.

/** How far outside the viewport, and outside a scrolled column, a critter already counts as on screen. */
export const SCREEN_MARGIN = 64;

type Report = (near: boolean) => void;

/**
 * The observer's margin. scrollMargin grows the viewport and every scrolled
 * box on the way (a Board column, the Lanes strip); adding rootMargin as well
 * would grow the viewport twice. A browser without scrollMargin gets
 * rootMargin, on the viewport only.
 */
export function observerOptions(hasScrollMargin: boolean): IntersectionObserverInit {
  const m = `${SCREEN_MARGIN}px`;
  return hasScrollMargin ? { scrollMargin: m, threshold: 0 } : { rootMargin: m, threshold: 0 };
}

class OnScreen {
  /** False while the browser tab is hidden. */
  tabShown = $state(true);
  // undefined: not made yet; null: the browser has no IntersectionObserver.
  #io: IntersectionObserver | null | undefined;
  #watched = new Map<Element, Report>();
  #looking = false;

  constructor() {
    if (typeof document === 'undefined') return;
    const read = () => {
      this.tabShown = document.visibilityState !== 'hidden';
      if (this.tabShown) this.look();
    };
    read();
    document.addEventListener('visibilitychange', read);
  }

  #observer(): IntersectionObserver | null {
    if (this.#io === undefined) {
      this.#io =
        typeof IntersectionObserver === 'function'
          ? new IntersectionObserver((entries, io) => {
              for (const e of entries) {
                io.unobserve(e.target);
                this.#watched.get(e.target)?.(e.isIntersecting);
              }
            }, observerOptions('scrollMargin' in IntersectionObserver.prototype))
          : null;
      if (this.#io) this.#listen();
    }
    return this.#io;
  }

  // What can move a critter in or out of sight: a scroll anywhere (captured,
  // so scrolled columns count too), a resize, and the page changing under it.
  #listen() {
    const look = () => this.look();
    window.addEventListener('scroll', look, { capture: true, passive: true });
    window.addEventListener('resize', look, { passive: true });
    new MutationObserver(look).observe(document.body, { childList: true, subtree: true, characterData: true });
  }

  /** Has the observer look at every critter once, on the next frame; several calls in a frame look once. */
  look(): void {
    if (this.#looking || this.#watched.size === 0) return;
    const io = this.#observer();
    if (!io) return;
    this.#looking = true;
    requestAnimationFrame(() => {
      this.#looking = false;
      for (const el of this.#watched.keys()) io.observe(el);
    });
  }

  /**
   * Reports whether `el` is on screen: soon after the call, then whenever it
   * may have changed. Returns the function that stops watching. Without
   * IntersectionObserver everything counts as on screen, as before.
   */
  watch(el: Element, report: Report): () => void {
    const io = this.#observer();
    if (!io) {
      report(true);
      return () => {};
    }
    this.#watched.set(el, report);
    io.observe(el);
    return () => {
      io.unobserve(el);
      this.#watched.delete(el);
    };
  }
}

export const onScreen = new OnScreen();
