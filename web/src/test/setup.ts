// jsdom has neither matchMedia nor the Web Animations API. Reduced motion is
// on in tests, so every transition is instant.
if (typeof window !== 'undefined') {
  window.matchMedia ??= ((query: string) => ({
    matches: query.includes('prefers-reduced-motion'),
    media: query,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;

  Element.prototype.animate ??= function () {
    const a = {
      onfinish: null as null | (() => void),
      oncancel: null,
      currentTime: 0,
      playState: 'finished',
      finished: Promise.resolve(),
      cancel() {},
      finish() {},
      play() {},
      pause() {},
      reverse() {},
      effect: null,
    };
    queueMicrotask(() => a.onfinish?.());
    return a as unknown as Animation;
  };
  Element.prototype.getAnimations ??= () => [];

  // Size bindings use ResizeObserver; jsdom has no layout, so it never fires.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}
