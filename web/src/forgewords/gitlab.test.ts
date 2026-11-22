// The page's words for GitLab merge requests: "!12", "merge request" in the
// hidden link words, "on GitLab", "approval needed", and the general words
// that name both forges once the board has a merge request.
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import App from '../App.svelte';
import Board from '../views/Board.svelte';
import Lanes from '../views/Lanes.svelte';
import Teams from '../views/Teams.svelte';
import Card from '../components/Card.svelte';
import StatusBar from '../components/StatusBar.svelte';
import { indexOf, prLineParts, sourceLine } from '../data/derive';
import { rowWord, type CatchupClient } from '../data/catchup';
import { reviewWords } from '../data/words';
import { card, project, snapshot } from '../test/fixtures';
import { feedOf, recordActions, render, settle, text } from '../test/render';
import type { CatchupItem, PullRequest, Snapshot, SourceStatus } from '../contract.gen';

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});
beforeEach(() => localStorage.clear());
const NOW = Date.parse('2026-11-17T10:00:00Z');
const noop = () => {};

const HOST = 'gitlab.example';
const PATH = 'sample-group/sample-app';
const P = 'p_000000000001';
const MR_URL = `https://${HOST}/${PATH}/-/merge_requests/12`;
const mr = (over: Partial<PullRequest> = {}): PullRequest => ({
  forge: 'gitlab',
  host: HOST,
  repo: PATH,
  number: 12,
  title: 'Sample merge request',
  url: MR_URL,
  state: 'open',
  checks: 'passing',
  review: '',
  draft: false,
  ...over,
});
const pr = (over: Partial<PullRequest> = {}): PullRequest => ({
  forge: 'github',
  host: 'github.com',
  repo: 'sample-org/sample-repo',
  number: 7,
  title: 'Sample pull request',
  url: 'https://github.com/sample-org/sample-repo/pull/7',
  state: 'open',
  checks: 'passing',
  review: '',
  draft: false,
  ...over,
});
const glProject = { ...project(P, 'sample-app'), repo: PATH, repoForge: 'gitlab' as const, repoHost: HOST };

/** A board with one card in review on a GitLab merge request. */
function board(over: Partial<Snapshot> = {}): Snapshot {
  const c = card({ id: 'claude:mr', column: 'in_review', pullRequests: [mr()], agent: { name: 'sample-worker' } });
  return snapshot([c], [glProject], { gitlabHosts: [HOST], ...over });
}
function view(C: typeof Board | typeof Lanes | typeof Teams, s: Snapshot) {
  r = render(C, { snapshot: s, ix: indexOf(s), now: NOW, arrivals: new Map(), onjump: noop, onseen: noop });
  return r;
}
const q = (sel: string) => r!.target.querySelector(sel) ?? document.querySelector(sel);

describe('review words per forge', () => {
  it('GitLab says "approval needed" for a required approval and nothing for changes requested', () => {
    expect(prLineParts(mr({ review: 'review_required' })).rest).toBe('checks passing · approval needed');
    expect(prLineParts(mr({ review: 'changes_requested' })).rest).toBe('checks passing');
    expect(prLineParts(mr({ review: 'approved' })).rest).toBe('checks passing · approved');
    expect(reviewWords(mr({ review: '' }))).toBe('');
  });
  it('GitHub keeps its words', () => {
    expect(prLineParts(pr({ review: 'review_required' })).rest).toBe('checks passing');
    expect(prLineParts(pr({ review: 'changes_requested' })).rest).toBe('checks passing · changes requested');
    expect(prLineParts(pr({ review: 'approved' })).rest).toBe('checks passing · approved');
  });
});

describe('Board with a merge request', () => {
  it('the column hints name pull or merge requests', () => {
    view(Board, board());
    expect(text(q('section[data-column="in_review"] .hint'))).toBe('Stopped with an open pull or merge request.');
    expect(text(q('section[data-column="done"] .hint'))).toBe('Every pull or merge request it opened is merged or closed.');
  });
  it('an empty In review column says the same', () => {
    const s = board();
    view(Board, { ...s, cards: [] });
    expect(text(q('section[data-column="in_review"] .empty'))).toBe(
      'Empty right now. An agent lands here when it stops while one of its pull or merge requests is still open.',
    );
  });
});

describe('a card with a merge request', () => {
  it('links !12 as a merge request, and a pull request beside it as a pull request', () => {
    const c = card({ column: 'in_review', pullRequests: [mr({ review: 'review_required' }), pr()] });
    const s = snapshot([c], [glProject], { gitlabHosts: [HOST] });
    r = render(Card, { card: c, ix: indexOf(s), now: NOW, agtermPresent: true, onjump: noop, onseen: noop });
    const links = [...r.target.querySelectorAll<HTMLAnchorElement>('.prs a')];
    expect(links.map((a) => a.getAttribute('href'))).toEqual([MR_URL, 'https://github.com/sample-org/sample-repo/pull/7']);
    expect(text(links[0])).toBe('!12 checks passing · approval needed (merge request, opens in a new tab)');
    expect(text(links[1])).toBe('#7 checks passing (pull request, opens in a new tab)');
    expect(q('.prs')!.getAttribute('aria-label')).toBe('Pull and merge requests');
  });
});

describe('rows with a merge request', () => {
  it('Teams names the column "Pull and merge requests" and links !12 as a merge request', () => {
    view(Teams, board());
    expect(text(q('.thead'))).toContain('Pull and merge requests');
    const a = q('.trow[data-card="claude:mr"] a') as HTMLAnchorElement;
    expect(a.getAttribute('href')).toBe(MR_URL);
    expect(text(a)).toBe('!12 (merge request, opens in a new tab)');
  });
  // One row with a GitHub pull request and a GitLab merge request: each link
  // names its own kind, in Teams and in Lanes.
  for (const [name, V, row] of [['Teams', Teams, '.trow'], ['Lanes', Lanes, '.it']] as const) {
    it(`${name}: a mixed row names each link by its own forge`, () => {
      for (const prs of [[pr(), mr()], [mr(), pr()]]) {
        r?.destroy();
        const c = card({ id: 'claude:mixed', column: 'in_review', pullRequests: prs, agent: { name: 'sample-worker' } });
        view(V as typeof Teams, snapshot([c], [glProject], { gitlabHosts: [HOST] }));
        const got = [...r!.target.querySelectorAll<HTMLAnchorElement>(`${row}[data-card="claude:mixed"] a`)].map((a) => [a.getAttribute('href'), a.querySelector('.visually-hidden')!.textContent]);
        const want: Record<string, string> = { [MR_URL]: ' (merge request, opens in a new tab)', 'https://github.com/sample-org/sample-repo/pull/7': ' (pull request, opens in a new tab)' };
        expect(got).toEqual(prs.map((p) => [p.url, want[p.url]]));
      }
    });
  }
  it('Lanes links !12 as a merge request', () => {
    view(Lanes, board());
    const a = q('.it[data-card="claude:mr"] a') as HTMLAnchorElement;
    expect(a.getAttribute('href')).toBe(MR_URL);
    expect(text(a)).toBe('!12 (merge request, opens in a new tab)');
  });
});

describe('catch-up with a merge request', () => {
  const opened: CatchupItem = {
    at: '2026-11-17T09:40:00Z',
    kind: 'pr_opened',
    cardId: '',
    projectId: P,
    projectName: 'sample-app',
    agentName: 'sample-worker',
    title: 'Sample merge request',
    pullRequest: mr(),
  };
  it('words the row "MR opened" and links it on GitLab with the snapshot hosts', async () => {
    const f = feedOf({ conn: 'live', snapshot: board() });
    const client: CatchupClient = {
      load: async () => ({ ok: true, catchup: { asOf: '2026-11-17T10:00:00Z', total: 1, items: [opened] } }),
      markRead: async () => ({ ok: true }),
    };
    r = render(App, { feed: f.feed, actions: recordActions().actions, catchup: client });
    await settle();
    document.querySelector<HTMLButtonElement>('header button.catchup')!.click();
    await settle();
    const panel = document.querySelector('#catchup-panel')!;
    expect(text(panel.querySelector('li .k'))).toBe('MR opened');
    const a = panel.querySelector<HTMLAnchorElement>('li a')!;
    expect(a.getAttribute('href')).toBe(MR_URL);
    expect(text(a)).toBe('!12 on GitLab, opens in a new tab');
  });
  it('a merge request row on a host not listed is not shown at all, even with an artifact on it', async () => {
    const f = feedOf({ conn: 'live', snapshot: board({ gitlabHosts: ['gitlab.other.example'] }) });
    const odd: CatchupItem = { ...opened, artifact: { title: 'Sample', url: 'https://claude.ai/artifact/exampleArtifact0001' } };
    const client: CatchupClient = {
      load: async () => ({ ok: true, catchup: { asOf: '2026-11-17T10:00:00Z', total: 1, items: [odd] } }),
      markRead: async () => ({ ok: true }),
    };
    r = render(App, { feed: f.feed, actions: recordActions().actions, catchup: client });
    await settle();
    document.querySelector<HTMLButtonElement>('header button.catchup')!.click();
    await settle();
    const panel = document.querySelector('#catchup-panel')!;
    expect(panel.querySelector('li')).toBeNull();
    expect(text(panel)).not.toContain('Sample merge request');
    expect(panel.querySelector('a[href^="https://claude.ai/"]')).toBeNull();
  });
  it('keeps "PR opened" for a pull request', () => {
    expect(rowWord({ key: 'k', kind: 'pr_opened', at: '', count: 1, pullRequest: pr() } as never)).toBe('PR opened');
    expect(rowWord({ key: 'k', kind: 'pr_opened', at: '', count: 1, pullRequest: mr() } as never)).toBe('MR opened');
  });
});

describe('sources and About', () => {
  const gitlab = (state: SourceStatus['state']): SourceStatus => ({ kind: 'gitlab', state, version: '1.0.0', detail: '', tested: true, ready: true });
  it('the GitLab source reads "through glab"', () => {
    expect(sourceLine(gitlab('ok'), { cards: [] }).text).toBe('through glab');
  });
  const github = (state: SourceStatus['state']): SourceStatus => ({ ...gitlab(state), kind: 'github' });
  const privacy = (sources: SourceStatus[]) => {
    r?.destroy();
    r = render(StatusBar, { snapshot: snapshot([], [], { sources }) });
    r.target.querySelector<HTMLButtonElement>('button.me')!.click();
    flushSync();
    return q('.privacy')!.textContent;
  };
  it('About names only the forges the server reads', () => {
    const both = 'Runs only on this machine. Nothing is sent anywhere; GitHub and GitLab are read through your own gh and glab.';
    const glab = 'Runs only on this machine. Nothing is sent anywhere; GitLab is read through your own glab.';
    const gh = 'Runs only on this machine. Nothing is sent anywhere; GitHub is read through your own gh.';
    expect(privacy([github('ok'), gitlab('ok')])).toBe(both);
    expect(privacy([github('failing'), gitlab('failing')])).toBe(both); // switched on, not answering yet
    expect(privacy([gitlab('ok')])).toBe(glab);
    expect(privacy([github('off'), gitlab('ok')])).toBe(glab);
    expect(privacy([github('missing'), gitlab('ok')])).toBe(glab);
    // Without GitLab read: the line the page always had, whatever GitHub does.
    expect(privacy([github('ok'), gitlab('off')])).toBe(gh);
    expect(privacy([github('ok'), gitlab('missing')])).toBe(gh);
    expect(privacy([])).toBe(gh);
  });
});
