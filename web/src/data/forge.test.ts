import { describe, expect, it } from 'vitest';
import type { Issue, PullRequest } from '../contract.gen';
import { cardClosesParts, cardPrSummaryParts, closesParts, indexOf, issueHref, prHref, prKey, prLineParts, prNumber, readGitlabHosts, repoKey } from './derive';
import { groupCatchup, rowLink } from './catchup';
import { card, golden, project, snapshot } from '../test/fixtures';

const HOST = 'gitlab.example.com';
const PATH = 'example-group/sub/example-project';
const mr = (over: Partial<PullRequest> = {}): PullRequest => ({
  forge: 'gitlab',
  host: HOST,
  repo: PATH,
  number: 12,
  title: 't',
  url: `https://${HOST}/${PATH}/-/merge_requests/12`,
  state: 'open',
  checks: 'none',
  review: '',
  draft: false,
  ...over,
});
const glIssue = (n: number, over: Partial<Issue> = {}): Issue => ({
  forge: 'gitlab',
  host: HOST,
  repo: PATH,
  number: n,
  url: `https://${HOST}/${PATH}/-/issues/${n}`,
  ...over,
});

describe('GitLab links', () => {
  it('links a merge request only on a listed host, at the address built for it', () => {
    expect(prHref(mr(), [HOST])).toBe(`https://${HOST}/${PATH}/-/merge_requests/12`);
    expect(issueHref(glIssue(3), [HOST])).toBe(`https://${HOST}/${PATH}/-/issues/3`);
    // Not listed, or no list at all: no link.
    expect(prHref(mr(), [])).toBeNull();
    expect(prHref(mr())).toBeNull();
    expect(prHref(mr(), ['gitlab.com'])).toBeNull();
    expect(issueHref(glIssue(3), ['gitlab.com'])).toBeNull();
    // Rebuilt address differs: another number, path or host than the fields say.
    expect(prHref(mr({ number: 13 }), [HOST])).toBeNull();
    expect(prHref(mr({ repo: 'example-group/other' }), [HOST])).toBeNull();
    expect(prHref(mr({ url: `https://gitlab.com/${PATH}/-/merge_requests/12` }), [HOST, 'gitlab.com'])).toBeNull();
    // An address of the wrong form, even when it equals the rebuilt one.
    expect(prHref(mr({ host: 'evil.example:8443', url: `https://evil.example:8443/${PATH}/-/merge_requests/12` }), ['evil.example:8443'])).toBeNull();
    expect(prHref(mr({ repo: 'p', url: `https://${HOST}/p/-/merge_requests/12` }), [HOST])).toBeNull();
    // A GitHub address under the GitLab forge, and a GitLab one under GitHub.
    expect(prHref(mr({ url: `https://github.com/${PATH}/pull/12` }), [HOST])).toBeNull();
    expect(prHref(mr({ forge: 'github' }), [HOST])).toBeNull();
    expect(prHref(mr({ forge: 'github', host: HOST, repo: 'o/r', url: 'https://github.com/o/r/pull/12' }))).toBeNull();
    // A forge the page does not know.
    expect(prHref(mr({ forge: 'bitbucket' as never }), [HOST])).toBeNull();
  });
});

describe('forge-aware words and keys', () => {
  it('writes !12 for a merge request and #12 for a pull request', () => {
    expect(prNumber(mr())).toBe('!12');
    expect(prNumber({ forge: 'github', number: 12 })).toBe('#12');
    expect(prLineParts(mr()).num).toBe('!12');
  });

  it('keys GitHub as before forges and never lets a path collide across forges or hosts', () => {
    const gh = { forge: 'github' as const, host: 'github.com', repo: 'Example-Org/Example-Repo', number: 12 };
    expect(prKey(gh)).toBe('Example-Org/Example-Repo#12');
    expect(prKey(mr())).toBe(`gitlab:${HOST}/${PATH}!12`);
    const same = { forge: 'gitlab' as const, host: HOST, repo: 'example-org/example-repo', number: 12 };
    expect(prKey(same)).not.toBe(prKey(gh));
    expect(prKey({ ...same, host: 'gitlab.com' })).not.toBe(prKey(same));
    expect(repoKey({ forge: '', host: '', repo: '' })).toBe('');
  });

  it('counts the same issue number on two forges as two issues', () => {
    const gh: Issue = { forge: 'github', host: 'github.com', repo: 'example-group/example-project', number: 3, url: '' };
    const gl = glIssue(3, { repo: 'example-group/example-project', url: '' });
    expect(closesParts([gh, gl], '')?.items).toHaveLength(2);
  });

  it('tells two issues with the same path and number on different hosts apart', () => {
    const OTHER = 'gitlab.other.example';
    const a = glIssue(3);
    const b = glIssue(3, { host: OTHER, url: `https://${OTHER}/${PATH}/-/issues/3` });
    const SHORT = 'example-group/example-project';
    const gh: Issue = { forge: 'github', host: 'github.com', repo: SHORT, number: 3, url: `https://github.com/${SHORT}/issues/3` };
    const glShort = glIssue(3, { repo: SHORT, url: `https://${HOST}/${SHORT}/-/issues/3` });
    const hosts = [HOST, OTHER];
    // Neither is the card's own: each label carries its host, and each link is its own.
    expect(closesParts([a, b], '', hosts)?.items).toEqual([
      { label: `${HOST}/${PATH}#3`, href: a.url },
      { label: `${OTHER}/${PATH}#3`, href: b.url },
    ]);
    expect(closesParts([gh, glShort], '', hosts)?.items).toEqual([
      { label: `github.com/${SHORT}#3`, href: gh.url },
      { label: `${HOST}/${SHORT}#3`, href: glShort.url },
    ]);
    // The card's own repository stays a bare number; the other host's is named.
    expect(closesParts([a, b], repoKey({ forge: 'gitlab', host: HOST, repo: PATH }), hosts)?.items).toEqual([
      { label: '#3', href: a.url },
      { label: `${OTHER}/${PATH}#3`, href: b.url },
    ]);
    // The card's own repository counts even when none of its issues is on the line.
    expect(closesParts([b], repoKey({ forge: 'gitlab', host: HOST, repo: PATH }), hosts)?.items).toEqual([{ label: `${OTHER}/${PATH}#3`, href: b.url }]);
    // A path that no other repository shares keeps its short label.
    expect(closesParts([a, glIssue(4, { repo: 'example-group/other', url: '' })], '', hosts)?.items.map((i) => i.label)).toEqual([`example-project#3`, `other#4`]);
  });

  it('measures a GitLab card against its own project and links with the snapshot hosts', () => {
    const p = { ...project('p_000000000001', 'beta'), repo: PATH, repoForge: 'gitlab' as const, repoHost: HOST };
    const c = card({ pullRequests: [mr()], issues: [glIssue(3)] });
    const ix = indexOf(snapshot([c], [p], { gitlabHosts: [HOST] }));
    expect(cardClosesParts(c, ix)?.items).toEqual([{ label: '#3', href: `https://${HOST}/${PATH}/-/issues/3` }]);
    const bits = cardPrSummaryParts(c, ix)?.parts[0].bits ?? [];
    expect(bits[0]).toMatchObject({ text: '!12', href: `https://${HOST}/${PATH}/-/merge_requests/12` });
    // Without the host listed nothing links.
    const bare = indexOf(snapshot([c], [p]));
    expect(cardPrSummaryParts(c, bare)?.parts[0].bits[0]).toMatchObject({ text: '!12', href: null });
  });

  it('reads gitlabHosts as an array of strings, else none, and matches a host exactly', () => {
    const p = { ...project('p_000000000001', 'beta'), repo: PATH, repoForge: 'gitlab' as const, repoHost: HOST };
    const c = card({ pullRequests: [mr()], issues: [glIssue(3)] });
    const hrefOf = (hosts: unknown) => {
      const ix = indexOf(snapshot([c], [p], { gitlabHosts: hosts as string[] }));
      return [cardPrSummaryParts(c, ix)?.parts[0].bits[0], cardClosesParts(c, ix)?.items[0].href];
    };
    // A string that contains the host, an object, a number and null list no host.
    for (const bad of [HOST, `x${HOST}x`, { [HOST]: true }, 1, null]) {
      expect(readGitlabHosts(bad)).toEqual([]);
      expect(hrefOf(bad)[0]).toMatchObject({ text: '!12', href: null });
      expect(hrefOf(bad)[1]).toBeNull();
    }
    // Entries that are not strings are dropped; a longer or shorter host does not match.
    expect(readGitlabHosts([HOST, 3, null, { host: HOST }])).toEqual([HOST]);
    expect(hrefOf([`sub.${HOST}`, 'example.com'])[1]).toBeNull();
    expect(hrefOf([5, HOST])[1]).toBe(`https://${HOST}/${PATH}/-/issues/3`);
  });

  it('reads the golden snapshot: its merge request links, its pull requests unchanged', () => {
    const s = golden();
    const ix = indexOf(s);
    expect(ix.gitlabHosts).toEqual([HOST]);
    const all = s.cards.flatMap((c) => c.pullRequests);
    for (const p of all) expect(prHref(p, ix.gitlabHosts)).toBe(p.url);
    expect(all.filter((p) => p.forge === 'github').map(prNumber)).toEqual(all.filter((p) => p.forge === 'github').map((p) => `#${p.number}`));
  });
});

describe('catch-up rows', () => {
  const item = (pullRequest: PullRequest) =>
    ({ at: '2026-11-17T09:00:00Z', kind: 'pr_opened', cardId: '', projectId: 'p1', projectName: 'beta', agentName: 'a', title: 't', pullRequest }) as const;

  it('links a merge request only on a listed host and names it !12', () => {
    const [g] = groupCatchup([item(mr())]);
    const row = g.rows[0];
    expect(rowLink(row, new Set(), [HOST])).toEqual({ kind: 'url', href: mr().url, text: '!12' });
    expect(rowLink(row, new Set())).toEqual({ kind: 'none' });
  });

  it('links a GitHub row only at the address built for its own path and number', () => {
    const gh = (over: Partial<PullRequest> = {}) =>
      mr({ forge: 'github', host: 'github.com', repo: 'example-org/example-repo', url: 'https://github.com/example-org/example-repo/pull/12', ...over });
    const link = (p: PullRequest, hosts: readonly string[] = [HOST]) => rowLink(groupCatchup([item(p)])[0].rows[0], new Set(), hosts);
    expect(link(gh())).toEqual({ kind: 'url', href: 'https://github.com/example-org/example-repo/pull/12', text: '#12' });
    // Another host, a number or path the fields do not say, or an address that is not github.com's.
    expect(link(gh({ url: 'https://evil.example/example-org/example-repo/pull/12' }))).toEqual({ kind: 'none' });
    expect(link(gh({ url: 'https://github.com/example-org/example-repo/pull/13' }))).toEqual({ kind: 'none' });
    expect(link(gh({ url: 'https://github.com/other-org/other-repo/pull/12' }))).toEqual({ kind: 'none' });
    expect(link(gh({ host: HOST }))).toEqual({ kind: 'none' });
    // A forge this page does not know: no link, whatever the address.
    expect(link(gh({ forge: 'bitbucket' as never }))).toEqual({ kind: 'none' });
  });

  it('keeps a GitHub and a GitLab change with the same path and number apart', () => {
    const gh = mr({ forge: 'github', host: 'github.com', repo: 'example-group/example-project', url: '' });
    const gl = mr({ repo: 'example-group/example-project', url: '' });
    const [g] = groupCatchup([item(gh), item(gl)]);
    expect(g.rows).toHaveLength(2);
  });
});
