import { describe, expect, it } from 'vitest';
import { initial, SCENARIOS } from '../dev/scenarios';
import { catchupClient } from '../dev/catchup';
import { prKey } from './data/derive';

// The dev harness's scenarios stand in for the server, so they keep its rules
// (internal/core/rules.go): an unseen turn is rule 5, which only comes after
// rule 4, so a card with an open pull request is never unseen. A scenario
// that broke this would make the page play moves the real server never causes.
describe('dev scenarios keep the server rules', () => {
  it('no unseen card has an open pull request', () => {
    const wrong: string[] = [];
    for (const sc of SCENARIOS) {
      for (const c of initial(sc)?.cards ?? []) {
        if (c.attention.includes('unseen') && c.pullRequests.some((p) => p.state === 'open')) wrong.push(`${sc}: ${c.id}`);
      }
    }
    expect(wrong).toEqual([]);
  });
});

describe('the GitLab sample scenario', () => {
  it('links every merge request and issue on its sample host, and keeps "site" GitHub only', async () => {
    const { prHref, issueHref } = await import('./data/derive');
    const s = initial('gitlab')!;
    expect(s.gitlabHosts).toEqual(['gitlab.example']);
    const mrs = s.cards.flatMap((c) => c.pullRequests).filter((p) => p.forge === 'gitlab');
    const issues = s.cards.flatMap((c) => c.issues).filter((i) => i.forge === 'gitlab');
    expect(mrs.length).toBeGreaterThan(0);
    expect(mrs.some((p) => p.state === 'open' && p.review === 'review_required')).toBe(true);
    for (const p of mrs) expect(prHref(p, s.gitlabHosts)).not.toBeNull();
    for (const i of issues) expect(issueHref(i, s.gitlabHosts)).not.toBeNull();
    expect(s.sources.find((x) => x.kind === 'gitlab')?.state).toBe('ok');
    const site = initial('site')!;
    expect(site.gitlabHosts).toEqual([]);
    expect(site.cards.flatMap((c) => c.pullRequests).every((p) => p.forge === 'github')).toBe(true);
  });
});

describe('the GitLab catch-up list', () => {
  it('credits each merge request to the session and title the board shows', async () => {
    const board = initial('gitlab')!;
    const res = await catchupClient('gitlab').load();
    if (!res.ok) throw new Error('no list');
    const mrs = res.catchup.items.filter((i) => i.pullRequest?.forge === 'gitlab');
    expect(mrs.length).toBeGreaterThan(1);
    for (const it of mrs) {
      const owner = board.cards.find((c) => c.pullRequests.some((p) => prKey(p) === prKey(it.pullRequest!)));
      const onBoard = owner?.pullRequests.find((p) => prKey(p) === prKey(it.pullRequest!));
      expect({ card: it.cardId, agent: it.agentName, title: it.title, prTitle: it.pullRequest!.title }).toEqual({
        card: owner?.id,
        agent: owner?.agent.name,
        title: onBoard?.title,
        prTitle: onBoard?.title,
      });
    }
  });
});
