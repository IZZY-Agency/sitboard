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
    expect(s.gitlabHosts).toEqual(['gitlab.example', 'code.example:8443/tools']);
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

describe('the GitLab sample issues', () => {
  it('list issues of both forges, every one linked on its own host', async () => {
    const { issuesClient, issuesSnapshot } = await import('../dev/issues');
    const { openIssueHref, repoIssuesHref } = await import('./data/issues');
    const s = initial('gitlab')!;
    issuesSnapshot('many', s);
    const res = await issuesClient('many', s.projects).load();
    if (!res.ok) throw new Error('no list');
    const gl = res.issues.repos.filter((r) => r.forge === 'gitlab');
    expect(gl.map((r) => `${r.host}/${r.repo}`)).toEqual(['code.example:8443/tools/design/pixel-kit', 'gitlab.example/example-group/harbor-api']);
    expect(res.issues.repos.some((r) => r.forge === 'github')).toBe(true);
    for (const r of gl) {
      expect(r.issues.length).toBeGreaterThan(0);
      for (const i of r.issues) expect(openIssueHref(r, i, s.gitlabHosts)).toBe(`https://${r.host}/${r.repo}/-/issues/${i.number}`);
      expect(repoIssuesHref(r, s.gitlabHosts)).toBe(`https://${r.host}/${r.repo}/-/issues`);
    }
    expect(s.projects.filter((p) => p.repoForge === 'gitlab').every((p) => p.openIssues.known)).toBe(true);
  });

  it('show what could not be read as such, never as no open issues', async () => {
    const { issuesClient, issuesSnapshot } = await import('../dev/issues');
    const s = initial('gitlab')!;
    issuesSnapshot('unread', s);
    const res = await issuesClient('unread', s.projects).load();
    if (!res.ok) throw new Error('no list');
    expect(res.issues.repos.map((r) => r.state)).toEqual(['ok', 'disabled', 'pending', 'notFound']);
    expect(res.issues.repos.filter((r) => r.forge === 'gitlab').map((r) => r.state)).toEqual(['pending', 'notFound']);
    for (const p of s.projects) {
      const r = res.issues.repos.find((x) => x.forge === p.repoForge && x.host === p.repoHost && x.repo === p.repo);
      if (r && r.state !== 'ok') expect(p.openIssues.known).toBe(false);
    }
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
