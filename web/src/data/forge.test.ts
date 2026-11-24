import { describe, expect, it } from 'vitest';
import type { Issue, PullRequest } from '../contract.gen';
import { cardClosesParts, cardPrSummaryParts, closesParts, gitlabMount, indexOf, issueHref, prHref, prKey, prLineParts, prNumber, readGitlabHosts, repoKey } from './derive';
import { groupCatchup, rowLink, visibleCatchupItems } from './catchup';
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
    expect(prHref(mr({ host: 'evil.example:443', url: `https://evil.example:443/${PATH}/-/merge_requests/12` }), ['evil.example:443'])).toBeNull();
    expect(prHref(mr({ host: 'evil.example:08443', url: `https://evil.example:08443/${PATH}/-/merge_requests/12` }), ['evil.example:08443'])).toBeNull();
    expect(prHref(mr({ host: 'evil.example:', url: `https://evil.example:/${PATH}/-/merge_requests/12` }), ['evil.example:'])).toBeNull();
    expect(prHref(mr({ repo: 'p', url: `https://${HOST}/p/-/merge_requests/12` }), [HOST])).toBeNull();
    // A GitHub address under the GitLab forge, and a GitLab one under GitHub.
    expect(prHref(mr({ url: `https://github.com/${PATH}/pull/12` }), [HOST])).toBeNull();
    expect(prHref(mr({ forge: 'github' }), [HOST])).toBeNull();
    expect(prHref(mr({ forge: 'github', host: HOST, repo: 'o/r', url: 'https://github.com/o/r/pull/12' }))).toBeNull();
    // A forge the page does not know.
    expect(prHref(mr({ forge: 'bitbucket' as never }), [HOST])).toBeNull();
  });

  it('links a merge request on a host with a web port only when that host:port is listed', () => {
    const site = `${HOST}:8443`;
    const url = `https://${site}/${PATH}/-/merge_requests/12`;
    expect(prHref(mr({ host: site, url }), [site])).toBe(url);
    expect(issueHref(glIssue(3, { host: site, url: `https://${site}/${PATH}/-/issues/3` }), [site])).toBe(`https://${site}/${PATH}/-/issues/3`);
    // The host name alone, or another port, is not that host.
    expect(prHref(mr({ host: site, url }), [HOST])).toBeNull();
    expect(prHref(mr({ host: site, url }), [`${HOST}:9443`])).toBeNull();
    // The port in the address must be the one in the fields.
    expect(prHref(mr({ host: HOST, url }), [HOST, site])).toBeNull();
    expect(prHref(mr({ host: site, url: `https://${HOST}/${PATH}/-/merge_requests/12` }), [HOST, site])).toBeNull();
  });

  it('links a merge request under a subfolder at its full web path, nothing shorter or longer', () => {
    const web = `gitlab/${PATH}`;
    const url = `https://${HOST}/${web}/-/merge_requests/12`;
    // The listed entry carries the subfolder: the row must sit under it.
    expect(prHref(mr({ repo: web, url }), [`${HOST}/gitlab`])).toBe(url);
    expect(prHref(mr({ repo: PATH, url }), [`${HOST}/gitlab`])).toBeNull();
    expect(prHref(mr({ repo: web, url: `https://${HOST}/${PATH}/-/merge_requests/12` }), [`${HOST}/gitlab`])).toBeNull();
    // On a host listed without a subfolder, "gitlab" is just a group name.
    expect(prHref(mr({ repo: web, url }), [HOST])).toBe(url);
  });

  it('binds a link to the mount of its host: repo and url changed together still need the mount', () => {
    const mounted = [`${HOST}/gitlab`];
    const mrUrl = (repo: string, n = 12) => `https://${HOST}/${repo}/-/merge_requests/${n}`;
    const isUrl = (repo: string, n = 3) => `https://${HOST}/${repo}/-/issues/${n}`;
    const good = `gitlab/${PATH}`;
    expect(prHref(mr({ repo: good, url: mrUrl(good) }), mounted)).toBe(mrUrl(good));
    expect(issueHref(glIssue(3, { repo: good, url: isUrl(good) }), mounted)).toBe(isUrl(good));
    // Both fields omit the mount: a path outside it.
    expect(prHref(mr({ repo: PATH, url: mrUrl(PATH) }), mounted)).toBeNull();
    expect(issueHref(glIssue(3, { repo: PATH, url: isUrl(PATH) }), mounted)).toBeNull();
    // Another folder than the mount.
    const other = `other/${PATH}`;
    expect(prHref(mr({ repo: other, url: mrUrl(other) }), mounted)).toBeNull();
    expect(issueHref(glIssue(3, { repo: other, url: isUrl(other) }), mounted)).toBeNull();
    // A mount that only starts like the listed one.
    const near = `gitlab2/${PATH}`;
    expect(prHref(mr({ repo: near, url: mrUrl(near) }), mounted)).toBeNull();
    // The character after the mount's name is not a slash: another folder.
    const glued = `gitlabx${PATH}`;
    expect(prHref(mr({ repo: glued, url: mrUrl(glued) }), mounted)).toBeNull();
    // Nothing after the mount but one segment: not a project.
    expect(prHref(mr({ repo: 'gitlab/solo', url: mrUrl('gitlab/solo') }), mounted)).toBeNull();
    // A host with a web port and a mount, both in the entry.
    const site = `${HOST}:8443`;
    const portUrl = `https://${site}/${good}/-/merge_requests/12`;
    expect(prHref(mr({ host: site, repo: good, url: portUrl }), [`${site}/gitlab`])).toBe(portUrl);
    expect(prHref(mr({ host: site, repo: PATH, url: `https://${site}/${PATH}/-/merge_requests/12` }), [`${site}/gitlab`])).toBeNull();
    expect(prHref(mr({ host: site, repo: good, url: portUrl }), [`${HOST}/gitlab`])).toBeNull();
  });

  it('refuses a path or number the server never emits', () => {
    // A project segment ending in .git or .atom.
    const dotGit = `${PATH}.git`;
    expect(prHref(mr({ repo: dotGit, url: `https://${HOST}/${dotGit}/-/merge_requests/12` }), [HOST])).toBeNull();
    const dotAtom = `example-group/app.atom`;
    expect(issueHref(glIssue(3, { repo: dotAtom, url: `https://${HOST}/${dotAtom}/-/issues/3` }), [HOST])).toBeNull();
    // A number above a signed 32-bit integer (ten digits pass a loose pattern).
    expect(prHref(mr({ number: 2147483647, url: `https://${HOST}/${PATH}/-/merge_requests/2147483647` }), [HOST])).toBe(`https://${HOST}/${PATH}/-/merge_requests/2147483647`);
    expect(prHref(mr({ number: 2147483648, url: `https://${HOST}/${PATH}/-/merge_requests/2147483648` }), [HOST])).toBeNull();
    expect(issueHref(glIssue(2147483648), [HOST])).toBeNull();
    // 22 segments are the most; 23 are not.
    const seg = (n: number) => Array.from({ length: n }, (_, i) => `g${i}`).join('/');
    expect(prHref(mr({ repo: seg(22), url: `https://${HOST}/${seg(22)}/-/merge_requests/12` }), [HOST])).not.toBeNull();
    expect(prHref(mr({ repo: seg(23), url: `https://${HOST}/${seg(23)}/-/merge_requests/12` }), [HOST])).toBeNull();
    // Under a mount, the mount's segment counts toward the 22.
    const under = `gitlab/${seg(21)}`;
    expect(prHref(mr({ repo: under, url: `https://${HOST}/${under}/-/merge_requests/12` }), [`${HOST}/gitlab`])).not.toBeNull();
    const tooDeep = `gitlab/${seg(22)}`;
    expect(prHref(mr({ repo: tooDeep, url: `https://${HOST}/${tooDeep}/-/merge_requests/12` }), [`${HOST}/gitlab`])).toBeNull();
  });

  it('links no path longer than the server gives the page (300 characters), for merge requests and issues alike', () => {
    const path = (n: number) => `${'a'.repeat(100)}/${'b'.repeat(100)}/${'c'.repeat(n - 202)}`;
    for (const [hosts, mount] of [[[HOST], ''], [[`${HOST}/gitlab`], 'gitlab/']] as const) {
      const at = (n: number) => `${mount}${path(n - mount.length)}`;
      expect(at(300).length).toBe(300);
      const mrAt = (n: number) => prHref(mr({ repo: at(n), url: `https://${HOST}/${at(n)}/-/merge_requests/12` }), hosts);
      const isAt = (n: number) => issueHref(glIssue(3, { repo: at(n), url: `https://${HOST}/${at(n)}/-/issues/3` }), hosts);
      expect(mrAt(300)).toBe(`https://${HOST}/${at(300)}/-/merge_requests/12`);
      expect(isAt(300)).toBe(`https://${HOST}/${at(300)}/-/issues/3`);
      expect(mrAt(301)).toBeNull();
      expect(isAt(301)).toBeNull();
    }
  });

  it('a row of the wrong shape, a whole entry as its host, or github.com never gets a GitLab link', () => {
    const mounted = [`${HOST}/gitlab`];
    const good = `gitlab/${PATH}`;
    const url = `https://${HOST}/${good}/-/merge_requests/12`;
    expect(prHref(mr({ repo: good, url }), mounted)).toBe(url);
    // The whole entry as the row's host: not a site.
    expect(prHref(mr({ host: `${HOST}/gitlab`, repo: PATH, url: `https://${HOST}/gitlab/${PATH}/-/merge_requests/12` }), mounted)).toBeNull();
    // Fields of another type must not throw.
    expect(prHref(mr({ repo: 5 as unknown as string, url }), mounted)).toBeNull();
    expect(prHref(mr({ host: null as unknown as string, url }), mounted)).toBeNull();
    // github.com is never a GitLab host, even when listed.
    const gh = `https://github.com/${PATH}/-/merge_requests/12`;
    expect(prHref(mr({ host: 'github.com', url: gh }), ['github.com'])).toBeNull();
    expect(prHref(mr({ host: 'github.com:8443', url: `https://github.com:8443/${PATH}/-/merge_requests/12` }), ['github.com:8443'])).toBeNull();
  });

  it('gitlabMount reads the subfolder from the listed entry, matching the host exactly', () => {
    expect(gitlabMount([HOST], HOST)).toBe('');
    expect(gitlabMount([`${HOST}/gitlab`], HOST)).toBe('gitlab');
    expect(gitlabMount([`${HOST}:8443/apps/gitlab`], `${HOST}:8443`)).toBe('apps/gitlab');
    expect(gitlabMount([`${HOST}:8443/gitlab`], HOST)).toBeNull();
    expect(gitlabMount([`${HOST}/gitlab`], 'example.com')).toBeNull();
    expect(gitlabMount([`x${HOST}`], HOST)).toBeNull();
    expect(gitlabMount([`${HOST}/gitlab`], `${HOST}/gitlab`)).toBeNull(); // a whole entry is no host
    expect(gitlabMount([5 as unknown as string, `${HOST}/gitlab`], HOST)).toBe('gitlab'); // a malformed entry is skipped, not thrown on
    expect(gitlabMount([], HOST)).toBeNull();
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

  it('keeps only entries the server could list: its host, port and subfolder bounds, one entry per site', () => {
    const ok = [HOST, `${HOST}:8443`, `${HOST}:1`, `${HOST}:65535`, `other.example.com/gitlab`, `third.example.com/${'a/'.repeat(7)}b`];
    expect(readGitlabHosts(ok)).toEqual(ok);
    for (const bad of [
      'localhost', // one label
      'GitLab.example.com', // not lowercase
      '-gitlab.example.com', // a label starting with a hyphen
      `${'a'.repeat(64)}.example.com`, // a label of 64
      `${'a'.repeat(63)}.${'b'.repeat(63)}.${'c'.repeat(63)}.${'d'.repeat(62)}`, // 254 characters
      `${HOST}:443`, // GitLab never writes it
      `${HOST}:0`,
      `${HOST}:08443`,
      `${HOST}:65536`,
      `${HOST}:`,
      `${HOST}:84a3`,
      `${HOST}/`, // an empty subfolder
      `${HOST}//gitlab`,
      `${HOST}/gitlab/`,
      `${HOST}/../gitlab`,
      `${HOST}/apps/git..lab`, // ".." inside a segment
      `${HOST}/.gitlab`,
      `${HOST}/${'a/'.repeat(8)}b`, // nine levels
      `user@${HOST}`,
    ]) {
      expect(readGitlabHosts([bad]), bad).toEqual([]);
    }
    // The longest host name and the deepest subfolder the server allows pass.
    const longest = `${'a'.repeat(63)}.${'b'.repeat(63)}.${'c'.repeat(63)}.${'d'.repeat(61)}`;
    expect(readGitlabHosts([longest])).toEqual([longest]);
    // Two entries for one site contradict each other: neither is kept; the same entry twice is one.
    expect(readGitlabHosts([HOST, `${HOST}/gitlab`, 'other.example.com'])).toEqual(['other.example.com']);
    expect(readGitlabHosts([`${HOST}/a`, `${HOST}/b`])).toEqual([]);
    expect(readGitlabHosts([HOST, HOST])).toEqual([HOST]);
    // Another port is another site.
    expect(readGitlabHosts([HOST, `${HOST}:8443/gitlab`])).toEqual([HOST, `${HOST}:8443/gitlab`]);
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

  it('visibleCatchupItems keeps a merge request only of a host in the list, mounted or not', () => {
    const gl = item(mr());
    const gh = item(mr({ forge: 'github', host: 'github.com', repo: 'o/r', url: 'https://github.com/o/r/pull/12' }));
    expect(visibleCatchupItems([gl, gh], [HOST])).toEqual([gl, gh]);
    expect(visibleCatchupItems([gl, gh], [`${HOST}/gitlab`])).toEqual([gl, gh]);
    expect(visibleCatchupItems([gl, gh], [`${HOST}:8443`])).toEqual([gh]);
    expect(visibleCatchupItems([gl, gh], [])).toEqual([gh]);
    expect(visibleCatchupItems([gl, gh])).toEqual([gh]);
  });

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
