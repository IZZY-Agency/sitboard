// 0.6.0 (#171): the agents on each issue, the Agent filter, pins, the tile
// count and the card's "looked at" line.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { initial } from '../dev/scenarios';
import { ISSUES, sampleIssuesKey, sampleList, sampleRawList } from '../dev/issues';
import App from './App.svelte';
import { card, project, snapshot } from './test/fixtures';
import { feedOf, recordActions, render, settle, text } from './test/render';
import type { ActionResult } from './data/actions';
import { createActions } from './data/actions';
import type { Card, Issues, IssuesSummary, OpenIssue, Project, RepoIssues, Snapshot } from './contract.gen';
import { agentTags, knownNoAgent, pinKey, readIssues, withPins, type IssuesClient, type IssuesLoad, type ProjectNoAgent } from './data/issues';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});
beforeEach(() => localStorage.clear());

const REPO_A = 'example-org/example-repo';
const REPO_B = 'example-org/other-repo';

// The server's issues key, opaque to the page: never recomputed from the path.
const keyOf = (repo: string) => `sample-key:${repo}`;

function withRepo(p: Project, repo: string, open: number): Project {
  return { ...p, repo, repoForge: 'github', repoHost: 'github.com', issuesKey: keyOf(repo), openIssues: { known: true, open } };
}
// A project with the core's per-project count (Project.issuesNoAgent, not in the generated type yet).
const served = (p: Project, n: number): Project => ({ ...p, issuesNoAgent: n }) as ProjectNoAgent;

const PA = withRepo(project('p_a', 'alpha'), REPO_A, 12);
const PB = withRepo(project('p_b', 'beta'), REPO_B, 3);

const ON = card({ id: 'claude:on', projectId: 'p_a', agent: { name: 'sample-on' } });
const LOOK = card({ id: 'claude:look', projectId: 'p_a', agent: { name: 'sample-look' } });
const CARDS = [ON, LOOK, card({ id: 'claude:b', projectId: 'p_b' })];

function issue(n: number, milestone = '', over: Partial<OpenIssue> = {}): OpenIssue {
  return { number: n, title: `Sample issue ${n}`, url: `https://github.com/${REPO_A}/issues/${n}`, milestone, labels: [], agents: [], pinned: false, ...over };
}
function repoList(repo: string, open: number, issues: OpenIssue[], pinned: OpenIssue[] = []): RepoIssues {
  return { forge: 'github', host: 'github.com', repo, key: keyOf(repo), state: 'ok', open, issues, pinned };
}

// alpha: #48 on it (0.5.0), #45 looked at (0.5.0), #33 by a card not on the board (Later), #5 pinned (none);
// outside the listed: #97 pinned and open, #61 pinned and closed. beta: #7 nothing (0.5.0).
const LIST: Issues = {
  state: 'ready',
  unsaved: false,
  rev: 1,
  freshMs: 60_000,
  asOf: '2026-11-17T09:58:00Z',
  repos: [
    repoList(
      REPO_A,
      12,
      [
        issue(48, '0.5.0', { agents: [{ cardId: 'claude:on', kind: 'closes' }] }),
        issue(45, '0.5.0', { agents: [{ cardId: 'claude:look', kind: 'looked' }] }),
        issue(33, 'Later', { agents: [{ cardId: 'claude:gone', kind: 'closes' }] }),
        issue(5, '', { pinned: true }),
      ],
      [issue(97, '', { pinned: true, state: 'open' }), issue(61, '', { pinned: true, state: 'closed' })],
    ),
    repoList(REPO_B, 3, [{ ...issue(7, '0.5.0'), url: `https://github.com/${REPO_B}/issues/7` }]),
  ],
};
const READY: IssuesSummary = { state: 'ready', asOf: '2026-11-17T09:58:00Z', open: 15, rev: 1, noAgent: 3 };

/** The list as a server that keeps pins (number -> pinned, alpha's repository) serves it. */
function withServerPins(list: Issues, pins: ReadonlyMap<number, boolean>): Issues {
  return {
    ...list,
    repos: list.repos.map((x) =>
      x.repo !== REPO_A
        ? x
        : { ...x, issues: x.issues.map((y) => ({ ...y, pinned: pins.get(y.number) ?? y.pinned })), pinned: x.pinned.filter((y) => pins.get(y.number) !== false) },
    ),
  };
}

function app(s: Snapshot, list: Issues = LIST, answer: Partial<Record<'pinIssue', ActionResult>> = {}) {
  const loads: IssuesLoad[] = [];
  const pins = new Map<number, boolean>(); // what the server keeps
  const c: IssuesClient = {
    async load() {
      const l: IssuesLoad = { ok: true, issues: withServerPins(list, pins) };
      loads.push(l);
      return l;
    },
  };
  const a = recordActions(answer);
  const send = a.actions.pinIssue;
  a.actions.pinIssue = async (req) => {
    const res = await send(req);
    if (res.ok) pins.set(req.number, req.pinned);
    return res;
  };
  const f = feedOf({ conn: 'live', snapshot: s });
  r = render(App, { feed: f.feed, actions: a.actions, issues: c });
  return { calls: a.calls, loads, push: (next: Snapshot) => f.push({ conn: 'live', snapshot: next }), setList: (l: Issues) => (list = l) };
}
const snap = (over: Partial<Snapshot> = {}, cards: Card[] = CARDS) => snapshot(cards, [PA, PB], { issues: READY, ...over });
const panel = () => document.getElementById('issues-panel');
async function open() {
  document.querySelector<HTMLButtonElement>('header button.issues')!.click();
  await settle();
}
const chip = (label: string) =>
  [...(panel()?.querySelectorAll<HTMLButtonElement>('.filter button') ?? [])].find((b) => (b.querySelector('.u')?.textContent ?? b.firstChild?.textContent) === label) ?? null;
const rows = () => [...(panel()?.querySelectorAll<HTMLElement>('li.it') ?? [])];
const num = (li: Element) => text(li.querySelector('.num')).split(' ')[0];
const row = (n: number) => rows().find((li) => num(li) === `#${n}`) ?? null;
const numbers = () => rows().map(num);
const agentGroup = () => panel()?.querySelector('[role="group"][aria-label^="Agent"]') ?? null;

describe('reading the list', () => {
  it('reads agents, pins, pinned rows outside the list and unsaved; drops what it cannot use', () => {
    const l = readIssues({
      state: 'ready',
      rev: 2,
      freshMs: 0,
      unsaved: true,
      repos: [
        {
          forge: 'github',
          host: 'github.com',
          repo: REPO_A,
          key: keyOf(REPO_A),
          state: 'ok',
          open: 3,
          issues: [
            {
              number: 4,
              title: 't',
              url: '',
              milestone: '',
              labels: [],
              pinned: true,
              agents: [
                { cardId: 'claude:x', kind: 'looked' },
                { cardId: 'claude:x', kind: 'closes' }, // the same card twice: "closes" wins
                { cardId: '', kind: 'closes' },
                { cardId: 'claude:y', kind: 'claimed' },
                'nonsense',
              ],
            },
          ],
          pinned: [
            { number: 4, title: 'listed already', url: '', milestone: '', labels: [], state: 'open' },
            { number: 9, title: 'closed', url: '', milestone: '', labels: [], state: 'closed' },
            { number: 9, title: 'twice', url: '', milestone: '', labels: [], state: 'closed' },
            { number: 10, title: 'no state', url: '', milestone: '', labels: [], state: 'merged' },
          ],
        },
      ],
    })!;
    expect(l.unsaved).toBe(true);
    const repo = l.repos[0];
    expect(repo.issues[0].pinned).toBe(true);
    expect(repo.issues[0].agents).toEqual([{ cardId: 'claude:x', kind: 'closes' }]);
    expect(repo.pinned.map((i) => [i.number, i.pinned, i.state])).toEqual([
      [9, true, 'closed'],
      [10, true, undefined],
    ]);
  });

  it('names an agent from its card only, and leaves out one whose card is not on the board', () => {
    const cards = new Map([ON, LOOK].map((c) => [c.id, c]));
    const tags = agentTags(
      issue(1, '', {
        agents: [
          { cardId: 'claude:look', kind: 'looked' },
          { cardId: 'claude:gone', kind: 'closes' },
          { cardId: 'claude:on', kind: 'closes' },
        ],
      }),
      cards,
    );
    expect(tags).toEqual([
      { cardId: 'claude:on', name: 'sample-on', kind: 'closes' },
      { cardId: 'claude:look', name: 'sample-look', kind: 'looked' },
    ]);
  });

  it('lays a pressed pin over the list, and an unpin takes a pinned row outside it away', () => {
    const a = LIST.repos[0];
    const l = withPins(
      LIST,
      new Map([
        [pinKey(a, 33), true],
        [pinKey(a, 5), false],
        [pinKey(a, 61), false],
      ]),
    );
    expect(l.repos[0].issues.filter((i) => i.pinned).map((i) => i.number)).toEqual([33]);
    expect(l.repos[0].pinned.map((i) => i.number)).toEqual([97]);
  });
});

describe('the Issues panel with agents and pins', () => {
  it('tags each row with its agents from the cards: solid "on it", dashed "looked at", "no agent seen" otherwise', async () => {
    app(snap());
    await open();
    const on = row(48)!.querySelector('.ag')!;
    expect(text(on)).toBe('sample-on · on it');
    expect(on.classList.contains('weak')).toBe(false);
    const look = row(45)!.querySelector('.ag')!;
    expect(text(look)).toBe('sample-look · looked at');
    expect(look.classList.contains('weak')).toBe(true);
    // A card that is not on the board: no tag, and so no agent seen.
    expect(row(33)!.querySelector('.ag')).toBeNull();
    expect(text(row(33)!.querySelector('.free'))).toBe('no agent seen');
    expect(panel()!.textContent).not.toMatch(/unclaimed|nobody/i);
  });

  it('pinned rows lead their repository under "Pinned", the ones outside the list after them', async () => {
    app(snap());
    await open();
    const a = panel()!.querySelector('section.repo')!;
    expect(text(a.querySelector('.msh'))).toBe('Pinned');
    expect(numbers().slice(0, 3)).toEqual(['#5', '#97', '#61']);
    expect(text(row(97)!.querySelector('.meta'))).toBe('pinned · not in the listed 30');
    expect(text(row(61)!.querySelector('.meta'))).toBe('closed since you pinned it · unpin issue #61');
    // A closed pin has the unpin button and no star.
    expect(row(61)!.querySelector('.pin')).toBeNull();
    expect(row(5)!.querySelector('.pin')!.getAttribute('aria-pressed')).toBe('true');
    expect(row(48)!.querySelector('.pin')!.getAttribute('aria-pressed')).toBe('false');
    expect(row(48)!.querySelector('.pin')!.getAttribute('aria-label')).toBe('Pin issue #48');
  });

  it('the Agent filter counts follow the milestone filter, and the two combine', async () => {
    app(snap());
    await open();
    const counts = () => [...agentGroup()!.querySelectorAll('button')].map((b) => [`${b.firstChild?.textContent} ${text(b.querySelector('.cn'))}`, b.getAttribute('aria-pressed')]);
    expect(counts()).toEqual([
      ['Any 5', 'true'],
      ['No agent seen 3', 'false'],
      ['With an agent 2', 'false'],
    ]);
    chip('0.5.0')!.click();
    await settle();
    expect(counts()).toEqual([
      ['Any 3', 'true'],
      ['No agent seen 1', 'false'],
      ['With an agent 2', 'false'],
    ]);
    expect(agentGroup()!.getAttribute('aria-label')).toBe('Agent, counting the issues listed here in 0.5.0');
    chip('No agent seen')!.click();
    await settle();
    // 0.5.0 and no agent seen: only beta's #7; pinned rows outside the list are not judged, so not shown.
    expect(numbers()).toEqual(['#7']);
    expect(text(panel()!.querySelector('section.repo .note'))).toBe('Every listed issue in 0.5.0 has been seen by an agent.');
    chip('With an agent')!.click();
    await settle();
    expect(numbers()).toEqual(['#48', '#45']);
    chip('All')!.click();
    chip('Any')!.click();
    await settle();
    expect(numbers()).toContain('#97');
  });

  it('a pinned row passes the filters like any listed row', async () => {
    app(snap());
    await open();
    chip('With an agent')!.click();
    await settle();
    expect(numbers()).not.toContain('#5');
    chip('No agent seen')!.click();
    await settle();
    expect(numbers()).toEqual(['#5', '#33', '#7']);
  });

  it('says the pins are not saved, as Catch-up does, when the list is unsaved', async () => {
    app(snap(), { ...LIST, unsaved: true });
    await open();
    expect(text(panel()!.querySelector('.msg.note'))).toBe(
      'Nothing here is saved: sitboard runs with --no-state, or could not write its state file when it started, so what you pin lasts until it stops.',
    );
  });
});

describe('pinning', () => {
  it('pins at once and posts the issue', async () => {
    const a = app(snap());
    await open();
    row(48)!.querySelector<HTMLButtonElement>('.pin')!.click();
    await settle();
    expect(a.calls).toContainEqual(['pinIssue', `github|github.com|${REPO_A}#48`, 'true']);
    expect(row(48)!.querySelector('.pin')!.getAttribute('aria-pressed')).toBe('true');
    expect(numbers().slice(0, 2)).toEqual(['#48', '#5']);
  });

  it('takes the pin back and says why when the server refuses it', async () => {
    const a = app(snap(), LIST, { pinIssue: { ok: false, kind: 'failed', message: 'too many issues are pinned already', final: true } });
    await open();
    row(48)!.querySelector<HTMLButtonElement>('.pin')!.click();
    await settle();
    expect(a.calls).toContainEqual(['pinIssue', `github|github.com|${REPO_A}#48`, 'true']);
    expect(row(48)!.querySelector('.pin')!.getAttribute('aria-pressed')).toBe('false');
    // The notice is inside the dialog (its tab order), once, and closing it leads back to the issue.
    expect(document.querySelectorAll('[data-action-notice]')).toHaveLength(1);
    const n = panel()!.querySelector('[data-action-notice]')!;
    expect(text(n)).toContain('Pin issue #48 failed: too many issues are pinned already.');
    expect(n.querySelector('.primary')).toBeNull(); // final: no Retry
    n.querySelector<HTMLButtonElement>('.close')!.click();
    await settle();
    expect(panel()).not.toBeNull();
    expect(document.activeElement).toBe(row(48)!.querySelector('.pin'));
  });

  it('keeps the pin over a list read before the server accepted it, and hands it to the first list read after', async () => {
    const pins = new Map<number, boolean>();
    let answer: (r: ActionResult) => void = () => {};
    const held: Array<() => void> = [];
    const actions = { ...recordActions().actions, pinIssue: () => new Promise<ActionResult>((res) => (answer = res)) };
    // Each read takes the server's state when it is sent, and answers when released.
    const load = () => {
      const l: IssuesLoad = { ok: true, issues: withServerPins(LIST, pins) };
      return new Promise<IssuesLoad>((res) => held.push(() => res(l)));
    };
    const f = feedOf({ conn: 'live', snapshot: snap() });
    r = render(App, { feed: f.feed, actions, issues: { load } satisfies IssuesClient });
    document.querySelector<HTMLButtonElement>('header button.issues')!.click();
    await settle();
    held.shift()!();
    await settle();
    const pressed = () => row(48)!.querySelector('.pin')!.getAttribute('aria-pressed');
    row(48)!.querySelector<HTMLButtonElement>('.pin')!.click();
    await settle();
    expect(pressed()).toBe('true'); // before the server answers
    // A read sent before the server accepts the pin (a new rev) ...
    f.push({ conn: 'live', snapshot: snap({ issues: { ...READY, rev: 2 } }) });
    await settle();
    expect(held).toHaveLength(1);
    pins.set(48, true);
    answer({ ok: true });
    await settle();
    // ... answers the list without the pin: the pin stays shown.
    held.shift()!();
    await settle();
    expect(pressed()).toBe('true');
    // The read sent after the acceptance shows it, and from then on the list alone decides.
    expect(held).toHaveLength(1);
    held.shift()!();
    await settle();
    expect(pressed()).toBe('true');
    pins.set(48, false); // another tab unpinned it
    f.push({ conn: 'live', snapshot: snap({ issues: { ...READY, rev: 3 } }) });
    await settle();
    held.shift()!();
    await settle();
    expect(pressed()).toBe('false');
  });

  it('unpins a closed pin with its unpin button', async () => {
    const a = app(snap());
    await open();
    row(61)!.querySelector<HTMLButtonElement>('.unpin')!.click();
    await settle();
    expect(a.calls).toContainEqual(['pinIssue', `github|github.com|${REPO_A}#61`, 'false']);
    expect(row(61)).toBeNull();
  });
});

describe('review round 1', () => {
  it('sends one pin write per issue at a time, and only the latest press after it; a replaced press says nothing', async () => {
    const sent: boolean[] = [];
    const kept = new Map<number, boolean>(); // what the server keeps
    const answers: Array<(r: ActionResult) => void> = [];
    const actions = {
      ...recordActions().actions,
      pinIssue: (req: { pinned: boolean }) => {
        sent.push(req.pinned);
        return new Promise<ActionResult>((res) => answers.push(res));
      },
    };
    const f = feedOf({ conn: 'live', snapshot: snap() });
    r = render(App, { feed: f.feed, actions, issues: { load: async () => ({ ok: true, issues: withServerPins(LIST, kept) }) } satisfies IssuesClient });
    await open();
    const star = () => panel()!.querySelector<HTMLButtonElement>(`[data-pin="${keyOf(REPO_A)}#48"]`)!;
    star().click(); // pin
    await settle();
    star().click(); // unpin
    await settle();
    star().click(); // pin again
    await settle();
    expect(sent).toEqual([true]); // one write out
    answers.shift()!({ ok: false, kind: 'failed', message: 'sitboard could not save it' });
    await settle();
    // The refused write was replaced by newer presses: no notice; the middle press is never sent.
    expect(document.querySelector('[data-action-notice]')).toBeNull();
    expect(sent).toEqual([true, true]);
    kept.set(48, true);
    answers.shift()!({ ok: true });
    await settle();
    expect(star().getAttribute('aria-pressed')).toBe('true');
  });

  it('a filter whose row is gone shows all again, so an outside pin can be reached', async () => {
    const one: Issues = { ...LIST, repos: [repoList(REPO_A, 1, [issue(5, '', { pinned: true })], [])] };
    const a = app(snapshot(CARDS, [PA], { issues: READY }), one);
    await open();
    chip('No agent seen')!.click();
    await settle();
    expect(agentGroup()!.querySelector('[aria-pressed="true"]')!.textContent).toContain('No agent seen');
    // The issue closes: no listed row is left, it is a closed pin outside the list now.
    a.setList({ ...one, rev: 2, repos: [repoList(REPO_A, 0, [], [issue(5, '', { pinned: true, state: 'closed' })])] });
    a.push(snapshot(CARDS, [PA], { issues: { ...READY, rev: 2 } }));
    await settle();
    expect(agentGroup()).toBeNull();
    expect(row(5)!.querySelector('.unpin')).not.toBeNull();
  });

  it('a milestone filter whose chips shrink to one shows all again', async () => {
    const a = app(snap());
    await open();
    chip('Later')!.click();
    await settle();
    expect(numbers()).toEqual(['#33']);
    a.setList({ ...LIST, rev: 2, repos: [repoList(REPO_A, 12, [issue(33, 'Later'), issue(30, 'Later')], [issue(97, '', { pinned: true, state: 'open' })])] });
    a.push(snap({ issues: { ...READY, rev: 2 } }));
    await settle();
    expect(panel()!.querySelector('[aria-label^="Milestone"]')).toBeNull();
    expect(numbers()).toEqual(['#97', '#33', '#30']);
  });

  it('focus follows the star to its new place, and back when the pin is refused', async () => {
    app(snap(), LIST, { pinIssue: { ok: false, kind: 'failed', message: 'sitboard could not save it' } });
    await open();
    const key = `${keyOf(REPO_A)}#48`;
    const first = panel()!.querySelector<HTMLButtonElement>(`[data-pin="${key}"]`)!;
    first.focus();
    first.click();
    await settle();
    const now = document.activeElement as HTMLElement;
    expect(now.dataset.pin).toBe(key);
    expect(now.closest('li')).toBe(row(48));
    // Refused: the row went back to its milestone, the focus with it; the notice is the next stop in the dialog.
    expect(numbers()[0]).toBe('#5');
    expect(panel()!.contains(document.querySelector('[data-action-notice]'))).toBe(true);
    // Retry leads back to the star too, even after focus went elsewhere first (a chip, then Tab to the notice).
    chip('Any')!.focus();
    const retry = panel()!.querySelector<HTMLButtonElement>('[data-action-notice] .primary')!;
    retry.focus();
    retry.click();
    await settle();
    expect((document.activeElement as HTMLElement).dataset.pin).toBe(key);
  });

  it('with a milestone that has no listed issue in a repository, says so before anything about agents', async () => {
    app(snap());
    await open();
    chip('Later')!.click();
    chip('No agent seen')!.click();
    await settle();
    const beta = [...panel()!.querySelectorAll('section.repo')][1];
    expect(text(beta.querySelector('.note'))).toBe('None of the listed issues is in Later.');
  });

  it('an outside pin shows the agents the server names, never "no agent seen", and is not counted; a reference-only pin shows its number and note', async () => {
    const l: Issues = {
      ...LIST,
      repos: [
        repoList(REPO_A, 12, LIST.repos[0].issues, [
          issue(97, '', { pinned: true, state: 'open', agents: [{ cardId: 'claude:on', kind: 'closes' }] }),
          issue(60, '', { pinned: true, state: 'open' }),
          issue(59, '', { pinned: true, title: '' }),
        ]),
        LIST.repos[1],
      ],
    };
    app(snap(), l);
    await open();
    expect(text(row(97)!.querySelector('.ag'))).toBe('sample-on · on it');
    expect(text(row(60)!.querySelector('.meta'))).toBe('pinned · not in the listed 30');
    expect(text(row(59)!.querySelector('.ttl'))).toBe('');
    expect(panel()!.textContent).not.toContain('untitled issue');
    const counts = [...agentGroup()!.querySelectorAll('.cn')].map((c) => text(c));
    expect(counts).toEqual(['5', '3', '2']);
  });

  it('an issue not judged is in neither Agent count and passes only Any, beside judged ones', async () => {
    const raw = JSON.parse(JSON.stringify(LIST));
    raw.repos[0].issues[3].agents = null; // #5: not judged
    raw.repos[1].issues[0].agents = null; // #7: not judged
    app(snap(), readIssues(raw)!);
    await open();
    const counts = [...agentGroup()!.querySelectorAll('.cn')].map((c) => text(c));
    expect(counts).toEqual(['5', '1', '2']); // #33 alone has no agent seen
    chip('No agent seen')!.click();
    await settle();
    expect(numbers()).toEqual(['#33']);
    expect(row(5)).toBeNull();
  });

  it('an absent IssuesSummary.noAgent is not known, never 0', () => {
    const { noAgent: _gone, ...rest } = READY;
    expect(knownNoAgent(rest as IssuesSummary)).toBeNull();
    expect(knownNoAgent({ ...READY, noAgent: 0 })).toBe(0);
    expect(knownNoAgent({ ...READY, noAgent: -1 })).toBeNull();
  });

  it('the looked-at line wraps a long label inside the card', () => {
    const css = readFileSync(join(__dirname, 'components/Card.svelte'), 'utf8');
    expect(css).toMatch(/\.looked \{[^}]*overflow-wrap: anywhere;/);
  });

  it('the harness pins outside the list only numbers no listed row has, in every case', () => {
    for (const c of ISSUES) {
      const s = initial('site')!;
      for (const p of s.projects) if (p.repo) p.issuesKey = sampleIssuesKey(p);
      const raw = sampleRawList(sampleList(c, s), s.cards);
      for (const r of raw.repos) {
        const listed = new Set(r.issues.map((i) => i.number));
        for (const p of r.pinned) expect(listed.has(p.number), `${c} ${r.repo} #${p.number}`).toBe(false);
      }
    }
  });
});

describe('the pin request', () => {
  function recorder(code: number, body: string | null = null) {
    const calls: Array<{ url: string; init: RequestInit }> = [];
    const fetchFn = ((url: string, init: RequestInit) => {
      calls.push({ url, init });
      return Promise.resolve(new Response(body, { status: code }));
    }) as unknown as typeof fetch;
    return { calls, actions: createActions(fetchFn) };
  }
  const req = { forge: 'github', host: 'github.com', repo: REPO_A, number: 48, pinned: true } as const;

  it('posts exactly the issue and the pin', async () => {
    const r = recorder(204);
    expect(await r.actions.pinIssue(req)).toEqual({ ok: true });
    expect(r.calls[0].url).toBe('/api/v1/issues/pin');
    expect(r.calls[0].init.method).toBe('POST');
    expect(r.calls[0].init.credentials).toBe('same-origin');
    expect(JSON.parse(r.calls[0].init.body as string)).toEqual(req);
  });

  it('words the refusals as the other actions do', async () => {
    expect(await recorder(400, 'bad repository\n').actions.pinIssue(req)).toEqual({ ok: false, kind: 'failed', message: 'sitboard did not accept this issue', final: true });
    expect(await recorder(409, 'too many issues are pinned\n').actions.pinIssue(req)).toEqual({ ok: false, kind: 'failed', message: 'too many issues are pinned already', final: true });
    expect(await recorder(404).actions.pinIssue(req)).toEqual({ ok: false, kind: 'failed', message: 'its repository is no longer on the board', final: true });
    expect(await recorder(500).actions.pinIssue(req)).toEqual({ ok: false, kind: 'failed', message: 'sitboard could not save it' });
    expect(await recorder(403).actions.pinIssue(req)).toEqual({ ok: false, kind: 'unauthorized' });
  });
});

describe('tiles and cards', () => {
  const tile = (name: string) =>
    [...document.querySelectorAll<HTMLElement>('.tiles .tile')].find((t) => text(t.querySelector('.name')) === name) ?? null;

  it('all projects says how many listed issues have no agent seen, from the summary, only when there are some', async () => {
    const a = app(snap());
    await settle();
    expect(text(tile('all projects')!.querySelector('.note.free'))).toBe(', 3 with no agent seen'); // the comma for screen readers only
    // A project: only once the panel has the list (judged there).
    expect(tile('alpha')?.querySelector('.note.free') ?? null).toBeNull();
    await open();
    expect(text(tile('alpha')!.querySelector('.note.free'))).toBe(', 2 with no agent seen');
    a.push(snap({ issues: { ...READY, noAgent: 0 } }));
    await settle();
    expect(tile('all projects')!.querySelector('.note.free')).toBeNull();
    // Absent (the core serves it optional): not known, so no line and never "0".
    const { noAgent: _gone, ...unknown } = READY;
    a.push(snap({ issues: unknown as IssuesSummary }));
    await settle();
    expect(tile('all projects')!.querySelector('.note.free')).toBeNull();
    a.push(snap({ issues: { ...READY, noAgent: 5 } }));
    await settle();
    expect(text(tile('all projects')!.querySelector('.note.free'))).toBe(', 5 with no agent seen');
  });

  it('a project tile shows the served Project.issuesNoAgent from the start, each repository once, and nothing for 0', async () => {
    const part = served({ ...withRepo(project('p_a2', 'alpha-part'), REPO_A, 12), parentId: 'p_a' }, 4);
    const projects = [
      served(PA, 4),
      part, // the same repository as its parent: counted once
      served(PB, 0),
    ];
    app(snapshot([...CARDS, card({ id: 'claude:part', projectId: 'p_a2' })], projects, { issues: READY }));
    await settle();
    expect(text(tile('alpha')!.querySelector('.note.free'))).toBe(', 4 with no agent seen');
    expect(tile('beta')!.querySelector('.note.free')).toBeNull();
  });

  it('a project with no served count falls back to the list, and a repository with one but no answer counts nothing', async () => {
    // beta serves one. alpha serves one, but its part (another repository) does not: the
    // tile never adds a served count to an unknown one, so alpha waits for the list.
    const part = { ...withRepo(project('p_a3', 'alpha-docs'), 'example-org/docs-repo', 2), parentId: 'p_a' };
    app(snapshot([...CARDS, card({ id: 'claude:docs', projectId: 'p_a3' })], [served(PA, 4), part, served(PB, 1)], { issues: READY }));
    await settle();
    expect(tile('alpha')?.querySelector('.note.free') ?? null).toBeNull();
    expect(text(tile('beta')!.querySelector('.note.free'))).toBe(', 1 with no agent seen');
    // Opened: the list answers for alpha's repository but not its part's, so still no line, never a part of it.
    await open();
    expect(tile('alpha')?.querySelector('.note.free') ?? null).toBeNull();
  });

  it('the list fallback counts only for a list of the current rev, and nothing for an issue not judged', async () => {
    const a = app(snap());
    await open();
    expect(text(tile('alpha')!.querySelector('.note.free'))).toBe(', 2 with no agent seen');
    // The snapshot moves on (a forge refresh) while the panel is closed: the list held is of an older rev.
    document.querySelector<HTMLButtonElement>('header button.issues')!.click();
    await settle();
    a.push(snap({ issues: { ...READY, rev: 2 } }));
    await settle();
    expect(tile('alpha')?.querySelector('.note.free') ?? null).toBeNull();
  });

  it('says nothing for an issue whose agents the server did not judge', async () => {
    const unjudged = readIssues(JSON.parse(JSON.stringify({ ...LIST, repos: LIST.repos.map((x) => ({ ...x, issues: x.issues.map((y) => ({ ...y, agents: null })) })) })))!;
    app(snap(), unjudged);
    await open();
    expect(panel()!.querySelectorAll('.ag')).toHaveLength(0);
    expect(row(48)!.querySelector('.free')).toBeNull();
    expect(text(row(97)!.querySelector('.free'))).toBe('pinned · not in the listed 30');
    expect(agentGroup()).toBeNull();
    expect(panel()!.textContent).not.toContain('no agent seen');
    expect(tile('alpha')?.querySelector('.note.free') ?? null).toBeNull();
  });

  it('a card shows "looked at #N" on its own line, linked, and keeps "closes" for the issues its pull request closes', async () => {
    const issueOf = (n: number, kind: 'closes' | 'looked') => ({ forge: 'github' as const, host: 'github.com', repo: REPO_A, number: n, url: `https://github.com/${REPO_A}/issues/${n}`, kind });
    const pr = { forge: 'github' as const, host: 'github.com', repo: REPO_A, number: 9, title: 'x', url: `https://github.com/${REPO_A}/pull/9`, state: 'open' as const, checks: 'passing' as const, review: '' as const, draft: false };
    const withPr = card({ id: 'claude:pr', projectId: 'p_a', pullRequests: [pr], issues: [issueOf(48, 'closes'), issueOf(45, 'looked')] });
    const noPr = card({ id: 'claude:nopr', projectId: 'p_a', issues: [issueOf(45, 'looked')] });
    app(snap({}, [withPr, noPr]));
    await settle();
    const one = document.querySelector(`[data-card="claude:pr"]`)!;
    expect(text(one.querySelector('.closes'))).toBe('closes #48 (issue, opens in a new tab)');
    expect(text(one.querySelector('.looked'))).toBe('looked at #45 (issue, opens in a new tab)');
    expect(one.querySelector('.looked a')!.getAttribute('href')).toBe(`https://github.com/${REPO_A}/issues/45`);
    const two = document.querySelector(`[data-card="claude:nopr"]`)!;
    expect(two.querySelector('.closes')).toBeNull();
    expect(text(two.querySelector('.looked'))).toBe('looked at #45 (issue, opens in a new tab)');
  });
});
