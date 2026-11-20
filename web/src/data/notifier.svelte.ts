// The notification switch and the notifier, wired to the browser. The rules
// live in notify.ts; this file only holds the state the header shows and the
// browser calls.

import type { Snapshot } from '../contract.gen';
import {
  CHANNEL_NAME,
  Notifier,
  SWITCH_KEY,
  Presence,
  TAG,
  exclusive,
  loadSwitch,
  saveSwitch,
  sentStore,
  switchState,
  type Channel,
  type NotifierDeps,
  type Permission,
  type SwitchState,
} from './notify';

export function browserPermission(w: (Window & typeof globalThis) | undefined = globalThis.window): Permission {
  const N = () => (w && 'Notification' in w ? w.Notification : undefined);
  return {
    supported: () => N() !== undefined,
    current: () => N()?.permission ?? 'denied',
    request: async () => {
      const n = N();
      if (!n) return 'denied';
      // Older Safari answers through a callback only.
      return new Promise<NotificationPermission>((resolve) => {
        const p = n.requestPermission((r) => resolve(r));
        if (p && typeof p.then === 'function') p.then(resolve, () => resolve(n.permission));
      });
    },
  };
}

/** Shows one notification; a click brings the board's tab to the front. */
export function browserShow(w: Window & typeof globalThis = window): (body: string) => void {
  return (body) => {
    try {
      const n = new w.Notification('sitboard', { body, tag: TAG });
      n.onclick = () => {
        w.focus();
        n.close();
      };
    } catch {
      // The browser refused (permission taken back meanwhile): nothing to show.
    }
  };
}

/** What a click on the switch came to. */
export type Outcome = 'on' | 'off' | 'denied' | 'dismissed' | 'none' | 'busy';

/** What the page says after a click; a notice for what needs the owner to act. */
export const OUTCOME_TEXT: Record<Exclude<Outcome, 'busy'>, string> = {
  on: 'Desktop notifications on.',
  off: 'Desktop notifications off.',
  dismissed: 'The browser did not allow notifications. They stay off.',
  denied: 'the browser blocks notifications for this page. Allow them in its site settings for this address, then click Notify again',
  none: 'this browser cannot show notifications. The tab title and icon still show how many agents need you',
};

export class NotifySwitch {
  chosen = $state(false);
  asking = $state(false);
  /** Bumped when the browser's permission may have changed (focus, a click). */
  #rev = $state(0);
  #p: Permission;
  #save: (on: boolean) => void;

  state = $derived.by((): SwitchState => {
    void this.#rev;
    if (this.asking) return 'asking';
    return switchState(this.chosen, this.#p);
  });

  constructor(p: Permission, chosen: boolean = loadSwitch(), save: (on: boolean) => void = saveSwitch) {
    this.#p = p;
    this.chosen = chosen;
    this.#save = save;
  }

  /** Another tab of this address changed the switch: follow it, without saving again. */
  follow(value: string | null): void {
    this.chosen = value === 'on';
  }

  /** The browser's permission may have changed in its settings: read it again. */
  recheck(): void {
    this.#rev++;
  }

  /**
   * The owner's click. The browser is asked only from here, never at load.
   * Says what came of it, for the page to tell him.
   */
  async toggle(): Promise<Outcome> {
    this.recheck();
    if (this.asking) return 'busy';
    const s = this.state;
    if (s === 'none') return 'none';
    if (s === 'on') {
      this.#set(false);
      return 'off';
    }
    if (s === 'denied') return 'denied'; // only the browser's settings can undo it
    if (this.#p.current() === 'granted') {
      this.#set(true);
      return 'on';
    }
    this.asking = true;
    try {
      const r = await this.#p.request();
      this.#set(r === 'granted');
      return r === 'granted' ? 'on' : r === 'denied' ? 'denied' : 'dismissed';
    } finally {
      this.asking = false;
      this.recheck();
    }
  }

  #set(on: boolean) {
    this.chosen = on;
    this.#save(on);
  }
}

/** Starts the notifier for this tab; returns it with a stop function. */
export function startNotifier(sw: NotifySwitch, w: Window & typeof globalThis = window): { observe: (s: Snapshot) => void; stop: () => void } {
  const self = () => w.document.visibilityState === 'visible' && w.document.hasFocus();
  let ch: Channel | null = null;
  try {
    ch = typeof w.BroadcastChannel === 'function' ? new w.BroadcastChannel(CHANNEL_NAME) : null;
  } catch {
    ch = null;
  }
  const presence = new Presence(ch, self, (fn, ms) => w.setTimeout(fn, ms));
  const deps: NotifierDeps = {
    enabled: () => sw.state === 'on',
    looking: () => presence.looking(),
    show: browserShow(w),
    sent: sentStore(),
    exclusive: exclusive(w.navigator?.locks),
    now: () => w.Date.now(),
    setTimeout: (fn, ms) => w.setTimeout(fn, ms),
    clearTimeout: (id) => w.clearTimeout(id as number),
  };
  const n = new Notifier(deps);
  const recheck = () => sw.recheck();
  const follow = (e: StorageEvent) => {
    if (e.key === SWITCH_KEY || e.key === null) sw.follow(e.key === null ? null : e.newValue);
  };
  w.addEventListener('storage', follow);
  w.addEventListener('focus', recheck);
  w.document.addEventListener('visibilitychange', recheck);
  return {
    observe: (s) => n.observe(s),
    stop: () => {
      n.stop();
      presence.close();
      w.removeEventListener('storage', follow);
      w.removeEventListener('focus', recheck);
      w.document.removeEventListener('visibilitychange', recheck);
    },
  };
}
