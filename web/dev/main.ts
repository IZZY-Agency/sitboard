// Dev harness: the real App, fed with placeholder data. Served by `npm run dev`
// at /dev/; never part of the build.
import { mount } from 'svelte';
import App from '../src/App.svelte';
import Harness from './Harness.svelte';
import { initial, SCENARIOS, step, type Scenario } from './scenarios';
import { CATCHUPS, catchupClient, type CatchupCase } from './catchup';
import { LEFTOVERS, leftoversClient, leftoversSummary, type LeftoversCase } from './leftovers';
import { ISSUES, issuesClient, issuesSnapshot, type IssuesCase } from './issues';
import { saveSwitch, type Permission } from '../src/data/notify';
import type { Feed, FeedState } from '../src/data/client';
import type { Actions } from '../src/data/actions';
import type { Snapshot } from '../src/contract.gen';
import '../src/styles/fonts.css';
import '../src/styles/tokens.css';
import '../src/styles/base.css';

const params = new URLSearchParams(location.search);
const sc = (SCENARIOS as readonly string[]).includes(params.get('s') ?? '') ? (params.get('s') as Scenario) : 'golden';
const liveMode = params.get('live') === '1';
const cu = (CATCHUPS as readonly string[]).includes(params.get('c') ?? '') ? (params.get('c') as CatchupCase) : 'few';

const lc = (LEFTOVERS as readonly string[]).includes(params.get('l') ?? '') ? (params.get('l') as LeftoversCase) : 'few';

const ic = (ISSUES as readonly string[]).includes(params.get('i') ?? '') ? (params.get('i') as IssuesCase) : 'few';

let current = initial(sc);
if (current) current.leftovers = leftoversSummary(lc, current.projects);
if (current) issuesSnapshot(ic, current);
let push: ((s: FeedState) => void) | null = null;

const feed: Feed = {
  start(on) {
    push = on;
    const conn: FeedState['conn'] =
      sc === 'loading' ? 'connecting' : sc === 'unreachable' ? 'unreachable' : sc === 'unauthorized' ? 'unauthorized' : sc === 'outdated' ? 'outdated' : sc === 'reconnecting' ? 'reconnecting' : 'live';
    on({ conn, snapshot: current, retryAt: sc === 'unreachable' ? Date.now() + 8000 : undefined });
    let n = 0;
    const t = liveMode && current ? setInterval(() => publish(step(current as Snapshot, n++)), 2500) : undefined;
    return () => clearInterval(t);
  },
};

function publish(s: Snapshot) {
  current = s;
  push?.({ conn: 'live', snapshot: s });
}

const actions: Actions = {
  async jump(id) {
    const c = current?.cards.find((x) => x.id === id);
    if (!c || c.agent.jump === 'none') return { ok: false, kind: 'no_terminal' };
    if (params.get('fail') === '1') return { ok: false, kind: 'failed', message: 'sitboard answered 500' };
    if (params.get('fail') === 'tmux' && c.agent.jump === 'tmux') return { ok: false, kind: 'failed', message: 'no tmux client is attached' };
    return { ok: true };
  },
  // The server forgets the grouping: its parts become top-level projects, and
  // a parent with no cards of its own leaves the board.
  async forgetGrouping(id) {
    if (!current) return { ok: true };
    const next = JSON.parse(JSON.stringify(current)) as Snapshot;
    if (!next.projects.some((p) => p.parentId === id)) return { ok: false, kind: 'failed', message: 'it is no longer grouped' };
    for (const p of next.projects) if (p.parentId === id) p.parentId = '';
    if (!next.cards.some((c) => c.projectId === id)) next.projects = next.projects.filter((p) => p.id !== id);
    next.seq++;
    setTimeout(() => publish(next), 300);
    return { ok: true };
  },
  // The server sets a folder's grouping: never splits the grouping and lists
  // the folder; always lists it; auto takes it off the list (back from
  // never, the parts are not regrouped here: the server would wait for
  // agents to qualify the folder again).
  async setGrouping(id, mode) {
    if (!current) return { ok: true };
    const next = JSON.parse(JSON.stringify(current)) as Snapshot;
    const listed = next.groupingSettings.find((s) => s.projectId === id);
    const p = next.projects.find((x) => x.id === id);
    if (!listed && !p) return { ok: false, kind: 'failed', message: 'it is no longer on the board', final: true };
    next.groupingSettings = next.groupingSettings.filter((s) => s.projectId !== id);
    if (mode !== 'auto') {
      const name = listed?.name ?? p!.name;
      next.groupingSettings.push({ projectId: id, name, path: listed?.path ?? p!.path, mode });
      next.groupingSettings.sort((a, b) => a.name.localeCompare(b.name) || a.projectId.localeCompare(b.projectId));
    }
    if (mode === 'never') {
      for (const q of next.projects) if (q.parentId === id) q.parentId = '';
      if (!next.cards.some((c) => c.projectId === id)) next.projects = next.projects.filter((q) => q.id !== id);
    }
    next.seq++;
    setTimeout(() => publish(next), 300);
    return { ok: true };
  },
  async seen(id) {
    if (!current) return { ok: true };
    const next = JSON.parse(JSON.stringify(current)) as Snapshot;
    const c = next.cards.find((x) => x.id === id);
    if (c) {
      c.attention = c.attention.filter((a) => a !== 'unseen');
      c.seen = true;
      if (c.column === 'needs_you' && c.agent.status === 'finished') c.column = 'idle';
      next.seq++;
      setTimeout(() => publish(next), 300);
    }
    return { ok: true };
  },
};

mount(Harness, { target: document.getElementById('harness')!, props: { scenario: sc, scenarios: SCENARIOS, live: liveMode, catchup: cu, catchups: CATCHUPS, leftover: lc, leftovers: LEFTOVERS, issue: ic, issues: ISSUES } });
// ?n= sets the Notify switch's state for pictures: off, asking (click it), on,
// denied or none. A stand-in for the browser's permission; no notification is
// ever shown by the harness.
const NOTIFY = ['off', 'asking', 'on', 'denied', 'none'] as const;
const n = (NOTIFY as readonly string[]).includes(params.get('n') ?? '') ? params.get('n') : null;
const permission: Permission | undefined = n
  ? {
      supported: () => n !== 'none',
      current: () => (n === 'denied' ? 'denied' : n === 'on' ? 'granted' : 'default'),
      request: () => new Promise<NotificationPermission>(() => {}), // 'asking' stays asking
    }
  : undefined;
if (n === 'on') saveSwitch(true);
else if (n) saveSwitch(false);

mount(App, { target: document.getElementById('app')!, props: { feed, actions, catchup: catchupClient(cu), leftovers: leftoversClient(lc, current?.projects ?? []), issues: issuesClient(ic, current?.projects ?? []), ...(permission ? { permission } : {}) } });
