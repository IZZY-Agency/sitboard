import { describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import { NotifySwitch, browserPermission, browserShow, startNotifier } from './notifier.svelte';
import type { Permission } from './notify';

interface Fake {
  now: NotificationPermission;
  asked: number;
  release: (r: NotificationPermission) => void;
  perm: Permission;
}

function fakePermission(start: NotificationPermission, answer: NotificationPermission = 'granted', supported = true): Fake {
  const p: Fake = {
    now: start,
    asked: 0,
    release: () => {},
    perm: {
      supported: () => supported,
      current: () => p.now,
      request: () => {
        p.asked++;
        return new Promise<NotificationPermission>((resolve) => {
          p.release = (r: NotificationPermission) => {
            p.now = r;
            resolve(r);
          };
          if (answer !== ('wait' as NotificationPermission)) p.release(answer);
        });
      },
    },
  };
  return p;
}

function make(start: NotificationPermission, answer?: NotificationPermission, chosen = false, supported = true) {
  const p = fakePermission(start, answer, supported);
  const saved: boolean[] = [];
  let sw!: NotifySwitch;
  const cleanup = $effect.root(() => {
    sw = new NotifySwitch(p.perm, chosen, (on) => saved.push(on));
  });
  return { p, sw, saved, cleanup };
}

describe('NotifySwitch', () => {
  it('is off, and asks nothing, until the owner clicks', () => {
    const { p, sw } = make('default');
    expect(sw.state).toBe('off');
    expect(p.asked).toBe(0);
  });

  it('a click asks the browser once; "asking" while its question is open; on and remembered when allowed', async () => {
    const { p, sw, saved } = make('default', 'wait' as NotificationPermission);
    const done = sw.toggle();
    flushSync();
    expect(sw.state).toBe('asking');
    void sw.toggle(); // a second click while asking asks nothing more
    expect(p.asked).toBe(1);
    p.release('granted');
    await done;
    expect(sw.state).toBe('on');
    expect(saved).toEqual([true]);
  });

  it('a refusal shows "blocked" and is not saved as on; a dismissed question leaves it off', async () => {
    const no = make('default', 'denied');
    await no.sw.toggle();
    expect(no.sw.state).toBe('denied');
    expect(no.saved).toEqual([false]);
    const later = make('default', 'default');
    await later.sw.toggle();
    expect(later.sw.state).toBe('off');
  });

  it('turns on without asking when the browser already allows it, and off again', async () => {
    const { p, sw, saved } = make('granted');
    await sw.toggle();
    expect(sw.state).toBe('on');
    await sw.toggle();
    expect(sw.state).toBe('off');
    expect(saved).toEqual([true, false]);
    expect(p.asked).toBe(0);
  });

  it('blocked by the browser: a click asks nothing; after the owner allows it in the settings, it reads it again', async () => {
    const { p, sw } = make('denied', 'granted', true);
    await sw.toggle();
    expect(p.asked).toBe(0);
    expect(sw.state).toBe('denied');
    p.now = 'granted';
    sw.recheck();
    expect(sw.state).toBe('on'); // he had turned it on before the browser blocked it
  });

  it('a click reads the browser again: allowed in its settings meanwhile, the click turns it on', async () => {
    const { p, sw } = make('denied', 'granted');
    expect(sw.state).toBe('denied');
    p.now = 'granted';
    await sw.toggle();
    expect(sw.state).toBe('on');
    expect(p.asked).toBe(0);
  });

  it('says what each click came to', async () => {
    expect(await make('default', 'granted').sw.toggle()).toBe('on');
    expect(await make('granted', 'granted', true).sw.toggle()).toBe('off');
    expect(await make('default', 'denied').sw.toggle()).toBe('denied');
    expect(await make('default', 'default').sw.toggle()).toBe('dismissed');
    expect(await make('denied').sw.toggle()).toBe('denied');
    expect(await make('default', 'granted', false, false).sw.toggle()).toBe('none');
    const held = make('default', 'wait' as NotificationPermission);
    void held.sw.toggle();
    expect(await held.sw.toggle()).toBe('busy');
  });

  it('two tabs: one turning it off or on, the other follows without saving again', async () => {
    const a = make('granted', 'granted', true);
    const b = make('granted', 'granted', true);
    expect(await a.sw.toggle()).toBe('off');
    b.sw.follow(null);
    expect(b.sw.state).toBe('off');
    b.sw.follow('on');
    expect(b.sw.state).toBe('on');
    expect(b.saved).toEqual([]);
  });

  it('a browser without notifications: the switch does nothing', async () => {
    const { p, sw } = make('default', 'granted', false, false);
    expect(sw.state).toBe('none');
    await sw.toggle();
    expect(p.asked).toBe(0);
  });
});

const made: Array<{ title: string; opts: NotificationOptions; closed: boolean; onclick: (() => void) | null }> = [];
class FakeNotification {
  onclick: (() => void) | null = null;
  constructor(title: string, opts: NotificationOptions) {
    made.push(Object.assign(this, { title, opts, closed: false }) as never);
  }
  close() {
    (this as unknown as { closed: boolean }).closed = true;
  }
}

describe('startNotifier', () => {
  it('follows the switch key from other tabs until stopped, then no longer', () => {
    const { sw } = make('granted', 'granted', false);
    const n = startNotifier(sw, window);
    window.dispatchEvent(new StorageEvent('storage', { key: 'sitboard.notify', newValue: 'on' }));
    expect(sw.state).toBe('on');
    n.stop();
    window.dispatchEvent(new StorageEvent('storage', { key: 'sitboard.notify', newValue: null }));
    expect(sw.state).toBe('on');
  });
});

describe('the browser side', () => {
  it('a browser without Notification is unsupported', () => {
    const p = browserPermission({} as Window & typeof globalThis);
    expect(p.supported()).toBe(false);
    expect(p.current()).toBe('denied');
  });

  it('one notification, titled sitboard, tagged so a later one replaces it; a click brings the tab forward and closes it', () => {
    made.length = 0;
    let focused = 0;
    const w = { Notification: FakeNotification, focus: () => focused++ } as unknown as Window & typeof globalThis;
    browserShow(w)('2 agents need you');
    expect(made).toHaveLength(1);
    expect(made[0].title).toBe('sitboard');
    expect(made[0].opts).toEqual({ body: '2 agents need you', tag: 'sitboard' });
    made[0].onclick!();
    expect(focused).toBe(1);
    expect(made[0].closed).toBe(true);
  });
});
