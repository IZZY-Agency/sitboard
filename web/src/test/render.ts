// Mounts a component into the jsdom document. Test-only.
import { flushSync, mount, unmount, type Component } from 'svelte';
import type { Snapshot } from '../contract.gen';
import type { Actions, ActionResult } from '../data/actions';
import type { Feed, FeedState } from '../data/client';

export function render<P extends Record<string, unknown>>(C: Component<P>, props: P) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(C, { target, props });
  flushSync();
  return {
    target,
    destroy() {
      unmount(app);
      target.remove();
    },
  };
}

export const settle = async () => {
  for (let i = 0; i < 3; i++) await new Promise((r) => setTimeout(r, 0));
  flushSync();
};

export function feedOf(first: FeedState) {
  let on: ((s: FeedState) => void) | null = null;
  const feed: Feed = {
    start(cb) {
      on = cb;
      cb(first);
      return () => {};
    },
  };
  return {
    feed,
    push(s: FeedState) {
      on?.(s);
      flushSync();
    },
  };
}

export function live(snapshot: Snapshot) {
  return feedOf({ conn: 'live', snapshot });
}

export function recordActions(answer: Partial<Record<'jump' | 'seen' | 'forgetGrouping' | 'setGrouping' | 'pinIssue', ActionResult>> = {}) {
  const calls: Array<[string, string] | [string, string, string]> = [];
  const actions: Actions = {
    jump: async (id) => {
      calls.push(['jump', id]);
      return answer.jump ?? { ok: true };
    },
    seen: async (id) => {
      calls.push(['seen', id]);
      return answer.seen ?? { ok: true };
    },
    forgetGrouping: async (id) => {
      calls.push(['forgetGrouping', id]);
      return answer.forgetGrouping ?? { ok: true };
    },
    setGrouping: async (id, mode) => {
      calls.push(['setGrouping', id, mode]);
      return answer.setGrouping ?? { ok: true };
    },
    pinIssue: async (req) => {
      calls.push(['pinIssue', `${req.forge}|${req.host}|${req.repo}#${req.number}`, String(req.pinned)]);
      return answer.pinIssue ?? { ok: true };
    },
  };
  return { calls, actions };
}

export const text = (el: Element | null | undefined) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();
