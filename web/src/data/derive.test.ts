import { describe, expect, it } from 'vitest';
import {
  applySeen,
  attentionLines,
  byColumn,
  COLUMN_META,
  COLUMNS,
  inbox,
  indexOf,
  laneColumns,
  laneNote,
  lanes,
  modelFamily,
  modelLabel,
  modelUnknown,
  needsYouArrivals,
  overviewWords,
  prLine,
  prSummary,
  cardPrs,
  cardPrSummary,
  artifactHref,
  artifactTitle,
  cardArtifacts,
  closesText,
  projectTabs,
  roleLine,
  sourcesReady,
  sourceLine,
  stateWord,
  teams,
  timeInState,
} from './derive';
import { card, golden, project, snapshot } from '../test/fixtures';
import type { Artifact, PullRequest, SourceStatus } from '../contract.gen';

const NOW = Date.parse('2026-11-17T10:00:00Z');
const pr = (number: number, over: Partial<PullRequest> = {}): PullRequest => ({
  repo: 'org/repo',
  number,
  title: 't',
  url: 'https://github.com/org/repo/pull/' + number,
  state: 'open',
  checks: 'passing',
  review: '',
  draft: false,
  ...over,
});

describe('columns', () => {
  it('six columns in order: Needs you, Working, With coordinator, In review, Done · 24h, Idle', () => {
    expect(COLUMNS.map((c) => COLUMN_META[c].label)).toEqual([
      'Needs you',
      'Working',
      'With coordinator',
      'In review',
      'Done · 24h',
      'Idle',
    ]);
  });
  it('every column has a one-line explanation and an empty text', () => {
    for (const c of COLUMNS) {
      expect(COLUMN_META[c].hint.length).toBeGreaterThan(0);
      expect(COLUMN_META[c].empty.length).toBeGreaterThan(0);
    }
  });
  it('Idle cards are ordered by time in state, longest last', () => {
    const a = card({ column: 'idle', agent: { statusSince: '2026-11-14T10:00:00Z' } });
    const b = card({ column: 'idle', agent: { statusSince: '2026-11-17T09:00:00Z' } });
    const c = card({ column: 'idle', agent: { statusSince: '2026-11-16T09:00:00Z' } });
    expect(byColumn([a, b, c]).idle.map((x) => x.id)).toEqual([b.id, c.id, a.id]);
  });
});

describe('words', () => {
  it('unknown status shows the words "status unknown"', () => {
    const g = golden();
    const unknown = g.cards.find((c) => c.agent.status === 'unknown');
    expect(unknown).toBeDefined();
    expect(stateWord(unknown!)).toBe('Status unknown');
    expect(laneNote(unknown!, indexOf(g)).head).toContain('status unknown');
  });
  it('state word follows the column otherwise', () => {
    expect(stateWord(card({ column: 'with_coordinator' }))).toBe('With coordinator');
  });
  it('unknown model shows "model unknown"; a Claude id reads as a name', () => {
    expect(modelLabel('')).toBe('model unknown');
    expect(modelLabel('claude-opus-5-5')).toBe('Opus 5.5');
    expect(modelLabel('claude-fable-5-2')).toBe('Fable 5.2');
    expect(modelLabel('claude-haiku-4-5-20251001')).toBe('Haiku 4.5');
    expect(modelLabel('gpt-5.4-codex')).toBe('gpt-5.4-codex');
  });
  it.each([
    ['claude-opus-5-5', 'opus', 'today\'s form'],
    ['claude-fable-5-2', 'fable', 'today\'s form'],
    ['claude-sonnet-5-5', 'sonnet', 'today\'s form'],
    ['claude-haiku-5-5', 'haiku', 'today\'s form'],
    ['claude-opus-9-1', 'opus', 'a version the page has never seen'],
    ['claude-haiku-4-5-20251001', 'haiku', 'a dated id'],
    ['claude-3-5-sonnet-20241022', 'sonnet', 'the old form, version before family'],
    ['claude-3-opus-20240229', 'opus', 'the old form, one version number'],
    ['claude-opus-4-6[1m]', 'opus', 'a context-size suffix'],
    ['claude-sonnet-4-5@20250929', 'sonnet', 'Vertex AI'],
    ['us.anthropic.claude-sonnet-4-5-20250929-v1:0', 'sonnet', 'Bedrock'],
    ['anthropic.claude-3-5-sonnet-20240620-v1:0', 'sonnet', 'Bedrock, old form'],
    ['arn:aws:bedrock:us-east-1:000000000000:inference-profile/us.anthropic.claude-opus-4-1-20250805-v1:0', 'opus', 'a Bedrock profile ARN'],
    ['anthropic/claude-opus-4.1', 'opus', 'a router prefix'],
    ['Claude-Fable-5-2', 'fable', 'upper case'],
    [' claude-opus-5-5\u200b', 'opus', 'invisible characters around it'],
  ])('model family: %s is %s (%s)', (id, family) => {
    expect(modelFamily(id)).toBe(family);
  });
  it.each([
    ['', 'empty'],
    ['   ', 'blank'],
    ['gpt-5.4-codex', 'not a Claude model'],
    ['opus-mt-en-de', 'a family word without "claude"'],
    ['claude-2', 'a Claude model with no family'],
    ['claude-instant-1', 'a family the page does not know'],
    ['claude-newfamily-6-0', 'a new family'],
    ['claudeopus', 'no word boundary'],
    ['claude-opus4', 'family and version run together: not an Anthropic id, and the family must be a whole word'],
    ['Qwen3-14B-Claude-Opus-4.5-Distill', 'another maker\'s model trained on Claude output: "claude" follows "14b", not a provider prefix'],
    ['deepseek-claude-sonnet-distill', 'another maker\'s distilled model: "claude" follows "deepseek"'],
    ['gemini-claude-opus', 'another maker\'s name in front of "claude"'],
    ['claude-latest-opus', 'a word that is not a version between "claude" and the family'],
  ])('model family: %s has none (%s), so it keeps the neutral chip', (id) => {
    expect(modelFamily(id)).toBe('');
  });
  it('a model is unknown only when there is no id at all', () => {
    expect(modelUnknown('')).toBe(true);
    expect(modelUnknown(' \u200b ')).toBe(true);
    expect(modelUnknown('gpt-5.4-codex')).toBe(false);
    expect(modelUnknown('claude-opus-5-5')).toBe(false);
  });
  it('the lane note carries the model apart from head and tail, with its hue in a muted note', () => {
    const boss = card({ title: 'plan', agent: { name: 'boss', model: 'claude-fable-5-2', status: 'unknown' } });
    const w = card({ title: 'do it', agent: { parent: 'known', parentCardId: boss.id, model: 'claude-sonnet-5-5' } });
    const ix = indexOf(snapshot([boss, w], [project(boss.projectId, 'one')]));
    expect(laneNote(boss, ix)).toMatchObject({ tone: 'muted', head: 'coordinator · status unknown', model: 'Fable 5.2', hue: 'fable', tail: '1 worker · plan' });
    expect(laneNote(w, ix)).toMatchObject({ tone: 'muted', head: '', model: 'Sonnet 5.5', hue: 'sonnet', tail: 'for boss · do it' });
    const lone = card({ title: '', agent: { name: 'x', model: '' } });
    expect(laneNote(lone, indexOf(snapshot([lone], [project(lone.projectId, 'one')])))).toMatchObject({
      head: '',
      model: 'model unknown',
      hue: '',
      tail: '',
    });
  });
  it('in a needs-you or failing lane note the model takes the note\'s tone, not its hue', () => {
    const needs = card({ column: 'needs_you', agent: { model: 'claude-opus-5-5' } });
    const failing = card({ column: 'in_review', attention: ['failing_checks'], agent: { model: 'claude-haiku-5-5' } });
    const ix = indexOf(snapshot([needs, failing], [project(needs.projectId, 'one')]));
    expect(laneNote(needs, ix)).toMatchObject({ tone: 'needs', model: 'Opus 5.5', hue: '' });
    expect(laneNote(failing, ix)).toMatchObject({ tone: 'failing', model: 'Haiku 5.5', hue: '' });
  });
  it('time in state falls back to the last activity and is empty when unknown', () => {
    expect(timeInState(card({ agent: { statusSince: '2026-11-17T09:47:00Z' } }), NOW)).toBe('13m');
    expect(timeInState(card({ agent: { statusSince: undefined, lastActivity: '2026-11-17T08:00:00Z' } }), NOW)).toBe('2h 0m');
    expect(timeInState(card({ agent: { statusSince: undefined, lastActivity: undefined } }), NOW)).toBe('');
  });
  it('coordinator line: coordinator, worker, unknown, none', () => {
    const boss = card({ agent: { name: 'boss' } });
    const w = card({ agent: { parent: 'known', parentCardId: boss.id } });
    const lost = card({ agent: { parent: 'known', parentCardId: '' } });
    const odd = card({ agent: { parent: 'unknown' } });
    const solo = card();
    const ix = indexOf(snapshot([boss, w, lost, odd, solo], []));
    expect(roleLine(boss, ix)).toBe('coordinator · 1 worker');
    expect(roleLine(w, ix)).toBe('for boss');
    expect(roleLine(lost, ix)).toBe('for a coordinator not on the board');
    expect(roleLine(odd, ix)).toBe('coordinator: unknown');
    expect(roleLine(solo, ix)).toBe('');
  });
  it('stale and failing checks are shown as words', () => {
    const c = card({
      attention: ['stale', 'failing_checks'],
      agent: { lastActivity: '2026-11-17T09:35:00Z' },
      pullRequests: [pr(733, { checks: 'failing' }), pr(5)],
    });
    expect(attentionLines(c, NOW).map((l) => l.text)).toEqual(['Checks failing on #733', 'Stale · no activity for 25m']);
  });
  it('pull request lines', () => {
    expect(prLine(pr(1, { checks: 'running' }))).toEqual({ text: '#1 checks running', tone: 'coordinator' });
    expect(prLine(pr(2, { checks: 'failing' })).tone).toBe('failing');
    expect(prLine(pr(3, { state: 'merged' }))).toEqual({ text: '#3 merged', tone: 'done' });
    expect(prLine(pr(4, { checks: 'none', draft: true })).text).toBe('#4 draft open');
    expect(prSummary([])).toBeNull();
  });

  const merged = (from: number, n: number) => Array.from({ length: n }, (_, i) => pr(from - i, { state: 'merged' as const }));

  it('a card shows at most three pull request lines, in the order sent, then "+N more merged"', () => {
    const list = [pr(50, { checks: 'failing' }), ...merged(49, 30)];
    const { shown, more } = cardPrs(list);
    expect(shown.map((p) => p.number)).toEqual([50, 49]);
    expect(more).toEqual({ text: '+29 more merged', tone: 'muted' });
    expect(cardPrs(list.slice(0, 3))).toEqual({ shown: list.slice(0, 3), more: null });
    expect(cardPrs([pr(1), pr(2), pr(3), ...merged(9, 2), pr(4, { state: 'closed' })]).more?.text).toBe('+4 more: 1 open · 2 merged · 1 closed');
  });

  // Every count of pull requests, both forms: [case, list, card lines, card "more", row line].
  const cases: Array<[string, PullRequest[], string[], { text: string; tone: string } | null, { text: string; tone: string } | null]> = [
    ['none', [], [], null, null],
    ['one open', [pr(1)], ['#1 checks passing'], null, { text: '#1 open · checks passing', tone: 'body' }],
    ['two open', [pr(2), pr(1, { checks: 'running' })], ['#2 checks passing', '#1 checks running'], null, { text: '#2 #1 open · checks running', tone: 'coordinator' }],
    ['three merged', merged(3, 3), ['#3 merged', '#2 merged', '#1 merged'], null, { text: '#3 #2 #1 merged', tone: 'done' }],
    ['four merged', merged(4, 4), ['#4 merged', '#3 merged'], { text: '+2 more merged', tone: 'muted' }, { text: '#4 #3 +2 merged', tone: 'done' }],
    ['many merged', merged(50, 30), ['#50 merged', '#49 merged'], { text: '+28 more merged', tone: 'muted' }, { text: '#50 #49 +28 merged', tone: 'done' }],
    ['four open', [pr(4), pr(3), pr(2), pr(1)], ['#4 checks passing', '#3 checks passing'], { text: '+2 more open', tone: 'muted' }, { text: '#4 #3 +2 open · checks passing', tone: 'body' }],
    [
      'hidden open with failing checks',
      [pr(4), pr(3), pr(2), pr(1, { checks: 'failing' })],
      ['#4 checks passing', '#3 checks passing'],
      { text: '+2 more open · checks failing', tone: 'failing' },
      { text: '#4 #3 +2 open · checks failing', tone: 'failing' },
    ],
    [
      'mixed with draft and closed',
      [pr(9, { draft: true, checks: 'none' }), ...merged(8, 3), pr(4, { state: 'closed' })],
      ['#9 draft open', '#8 merged'],
      { text: '+3 more: 2 merged · 1 closed', tone: 'muted' },
      { text: '#9 open · +3 merged · +1 closed', tone: 'body' },
    ],
  ];

  const issue = (number: number, repo = 'org/repo') => ({ repo, number, url: `https://github.com/${repo}/issues/${number}` });

  it('closes: numbers only, at most two, then the count; duplicates counted once', () => {
    expect(closesText([], 'org/repo')).toBe('');
    expect(closesText([issue(7)], 'org/repo')).toBe('closes #7');
    expect(closesText([issue(7), issue(8)], 'org/repo')).toBe('closes #7 #8');
    expect(closesText([issue(7), issue(8), issue(9), issue(10)], 'org/repo')).toBe('closes #7 #8 +2');
    expect(closesText([issue(7), issue(7)], 'org/repo')).toBe('closes #7');
  });

  it('closes: an issue outside the card\'s own repository carries that repository\'s short name', () => {
    expect(closesText([issue(7), issue(7), issue(7, 'org/other')], 'org/repo')).toBe('closes #7 other#7');
    expect(closesText([issue(7, 'org/other')], 'org/repo')).toBe('closes other#7');
    // Two other repositories with the same short name keep their owners apart.
    expect(closesText([issue(7, 'a/lib'), issue(8, 'b/lib')], 'org/repo')).toBe('closes a/lib#7 b/lib#8');
  });

  it('closes: another repository sharing the home short name (a fork) is named in full', () => {
    expect(closesText([issue(7, 'upstream/lib')], 'me/lib')).toBe('closes upstream/lib#7');
    expect(closesText([issue(7, 'me/lib'), issue(8, 'upstream/lib')], 'me/lib')).toBe('closes #7 upstream/lib#8');
  });

  it('closes: repository names compare without case, and print as first seen', () => {
    expect(closesText([issue(7, 'Owner/Name')], 'owner/name')).toBe('closes #7');
    expect(closesText([issue(7, 'Owner/Name'), issue(7, 'owner/name')], 'org/repo')).toBe('closes Name#7');
    expect(closesText([issue(7, 'a/Lib'), issue(8, 'b/lib')], 'org/repo')).toBe('closes a/Lib#7 b/lib#8');
    expect(closesText([issue(7, 'Org/Other'), issue(8, 'org/other')], '')).toBe('closes #7 #8');
  });

  it('closes: with no repository of its own, names are shown only when the issues span repositories', () => {
    expect(closesText([issue(7, 'org/other'), issue(8, 'org/other')], '')).toBe('closes #7 #8');
    expect(closesText([issue(7), issue(7, 'org/other')], '')).toBe('closes repo#7 other#7');
  });

  const homeIx = (c: ReturnType<typeof card>) => indexOf({ cards: [c], projects: [{ ...project(c.projectId, 'one'), repo: 'org/repo' }] });

  it('the one-line form appends the closed issues after the pull request part, and only with a pull request', () => {
    const a = card({ pullRequests: [pr(5, { checks: 'failing' })], issues: [issue(7), issue(8), issue(9)] });
    expect(cardPrSummary(a, homeIx(a))).toEqual({ text: '#5 open · checks failing · closes #7 #8 +1', tone: 'failing' });
    const b = card({ pullRequests: [], issues: [issue(7)] });
    expect(cardPrSummary(b, homeIx(b))).toBeNull();
    const c = card({ pullRequests: [pr(5)], issues: [] });
    expect(cardPrSummary(c, homeIx(c))!.text).toBe('#5 open · checks passing');
    const d = card({ pullRequests: [pr(5)], issues: [issue(7), issue(3, 'org/other')] });
    expect(cardPrSummary(d, homeIx(d))!.text).toBe('#5 open · checks passing · closes #7 other#3');
  });

  it('the lane note keeps the closed issues apart, so they never take the note\'s tone', () => {
    const a = card({ attention: ['failing_checks'], pullRequests: [pr(5, { checks: 'failing' })], issues: [issue(7)] });
    const n = laneNote(a, homeIx(a));
    expect(n.tone).toBe('failing');
    expect(n.tail).toMatch(/#5 open · checks failing$/);
    expect(n.closes).toBe('closes #7');
    const b = card({ pullRequests: [], issues: [issue(7)] });
    expect(laneNote(b, homeIx(b)).closes).toBe('');
  });

  it.each(cases)('pull requests, %s: card lines, card "more" line, row line', (_name, list, lines, more, row) => {
    const c = cardPrs(list);
    expect(c.shown.map((p) => prLine(p).text)).toEqual(lines);
    expect(c.more).toEqual(more);
    expect(prSummary(list)).toEqual(row);
  });

  it('a row line lists the open ones with their checks, then "+N merged", never a long run of numbers', () => {
    expect(prSummary([pr(9, { checks: 'failing' }), pr(8), ...merged(7, 1)])).toEqual({
      text: '#9 #8 open · checks failing · +1 merged',
      tone: 'failing',
    });
    expect(prSummary([pr(60), ...merged(59, 40)])!.text).toBe('#60 open · checks passing · +40 merged');
    expect(prSummary(merged(194, 2))).toEqual({ text: '#194 #193 merged', tone: 'done' });
    expect(prSummary(merged(194, 3))!.text).toBe('#194 #193 #192 merged');
    expect(prSummary(merged(194, 30))!.text).toBe('#194 #193 +28 merged');
    expect(prSummary([pr(1), pr(2), pr(3), pr(4), pr(5)])!.text).toBe('#1 #2 +3 open · checks passing');
    expect(prSummary([...merged(9, 1), pr(3, { state: 'closed' })])!.text).toBe('#9 merged · +1 closed');
  });
});

describe('inbox and seen', () => {
  it('lists every card with an attention reason, blocked first', () => {
    const quiet = card();
    const stale = card({ attention: ['stale'] });
    const blocked = card({ attention: ['blocked'], column: 'needs_you' });
    expect(inbox([quiet, stale, blocked]).map((c) => c.id)).toEqual([blocked.id, stale.id]);
  });
  it('marked seen hides "unseen" until the agent finishes another turn', () => {
    const c = card({ column: 'needs_you', attention: ['unseen'], seen: false, agent: { statusSince: '2026-11-17T09:00:00Z' } });
    const marked = new Map([[c.id, '2026-11-17T09:00:00Z']]);
    expect(applySeen([c], marked)[0].attention).toEqual([]);
    const later = { ...c, agent: { ...c.agent, statusSince: '2026-11-17T09:30:00Z' } };
    expect(applySeen([later], marked)[0].attention).toEqual(['unseen']);
  });
  it('arrivals in Needs you: none on the first snapshot, then only new arrivals', () => {
    const a = card({ column: 'working' });
    const b = card({ column: 'needs_you' });
    expect(needsYouArrivals(null, [a, b])).toEqual([]);
    const a2 = { ...a, column: 'needs_you' as const };
    const fresh = card({ column: 'needs_you' });
    expect(needsYouArrivals([a, b], [a2, b, fresh])).toEqual([a2.id, fresh.id]);
  });
});

describe('project filter', () => {
  it('lists projects with counts, most cards first, and skips projects with no card', () => {
    const s = snapshot(
      [card({ projectId: 'p_b' }), card({ projectId: 'p_a' }), card({ projectId: 'p_a' })],
      [project('p_b', 'bravo'), project('p_a', 'alpha'), project('p_c', 'empty')],
    );
    expect(projectTabs(s)).toEqual([
      { id: 'p_a', name: 'alpha', count: 2 },
      { id: 'p_b', name: 'bravo', count: 1 },
    ]);
  });
});

describe('lanes', () => {
  it('one row per project with its path and agent count', () => {
    const g = golden();
    const rows = lanes(g);
    expect(rows.map((r) => r.project.path)).toEqual(['~/projects/alpha', '~/projects/beta']);
    expect(rows[0].meta).toBe('1 coordinator · 5 agents');
  });
  it('overview: each project row shows its four counts with their words', () => {
    const g = golden();
    expect(overviewWords(g.projects[0].counts)).toEqual(['1 working', '1 needs you', '1 in review', '1 stale']);
  });
  it('a column with no card in any row is hidden, except Needs you and Working', () => {
    expect(laneColumns([card({ column: 'idle' })])).toEqual(['needs_you', 'working', 'idle']);
  });
});

describe('teams', () => {
  it('one panel per coordinator, its workers indented under it; unknown coordinator last, not indented', () => {
    const boss = card({ projectId: 'p_a', agent: { name: 'boss' } });
    const w1 = card({ projectId: 'p_a', agent: { parent: 'known', parentCardId: boss.id } });
    const lost = card({ projectId: 'p_a', agent: { parent: 'unknown' } });
    const solo = card({ projectId: 'p_b' });
    const t = teams(snapshot([lost, solo, w1, boss], [project('p_a', 'alpha'), project('p_b', 'bravo')]));
    expect(t.map((x) => x.label)).toEqual(['boss', 'Solo agents']);
    expect(t[0].rows.map((r) => [r.card.id, r.depth])).toEqual([
      [boss.id, 0],
      [w1.id, 1],
      [lost.id, 0],
    ]);
    expect(t[0].meta).toBe('1 coordinator · 1 worker · 1 agent with no known coordinator');
    expect(t[1].rows.map((r) => r.card.id)).toEqual([solo.id]);
  });
  it('a team working across repos is named after its coordinator and lists its projects', () => {
    const boss = card({ projectId: 'p_a', agent: { name: 'boss' } });
    const w1 = card({ projectId: 'p_b', agent: { parent: 'known', parentCardId: boss.id } });
    const w2 = card({ projectId: 'p_a', agent: { parent: 'known', parentCardId: boss.id } });
    const [team] = teams(snapshot([boss, w1, w2], [project('p_a', 'alpha'), project('p_b', 'bravo')]));
    expect(team.label).toBe('boss');
    expect(team.path).toBe('~/projects/alpha');
    expect(team.projects).toEqual(['alpha', 'bravo']);
  });
  it('an agent with an unknown coordinator in a project without a team is solo', () => {
    const lost = card({ agent: { parent: 'unknown' } });
    const t = teams(snapshot([lost], [project('p_000000000001', 'one')]));
    expect(t).toHaveLength(1);
    expect(t[0].solo).toBe(true);
  });
  it('the golden snapshot places every card exactly once', () => {
    const g = golden();
    const ids = teams(g).flatMap((t) => t.rows.map((r) => r.card.id));
    expect(ids.sort()).toEqual(g.cards.map((c) => c.id).sort());
  });
});

describe('sources', () => {
  it('a missing or failing source shows its notice', () => {
    const s = snapshot([], []);
    expect(sourceLine({ kind: 'agterm', state: 'missing', version: '', detail: '', tested: false, ready: true }, s).text).toBe('not found');
    expect(sourceLine({ kind: 'github', state: 'off', version: '', detail: '', tested: false, ready: true }, s).text).toBe('off');
    expect(sourceLine({ kind: 'github', state: 'failing', version: '', detail: 'not logged in', tested: true, ready: true }, s)).toEqual({
      text: 'failing · not logged in',
      tone: 'failing',
    });
  });
  it('a source on its first pass is not ready, and says it is reading', () => {
    const reading: SourceStatus = { kind: 'claude', state: 'ok', version: '2', detail: '', tested: true, ready: false };
    const done: SourceStatus = { ...reading, kind: 'git', ready: true };
    expect(sourcesReady({ sources: [reading, done] })).toBe(false);
    expect(sourcesReady({ sources: [{ ...reading, kind: 'agterm' }, done] })).toBe(false);
    expect(sourceLine(reading, snapshot([], [])).text).toBe('reading…');
    expect(sourcesReady({ sources: [{ ...reading, ready: true }, done] })).toBe(true);
  });
  it('a missing, off or failing source never holds the board in loading, whatever its ready value', () => {
    for (const state of ['missing', 'off', 'failing'] as const) {
      const src: SourceStatus = { kind: 'claude', state, version: '', detail: '', tested: false, ready: false };
      expect(sourcesReady({ sources: [src] }), state).toBe(true);
    }
  });
  it('claude code counts its live sessions', () => {
    const s = snapshot([card(), card(), card({ agent: { live: 'no' } })], []);
    expect(sourceLine({ kind: 'claude', state: 'ok', version: '2', detail: '', tested: true, ready: true }, s).text).toBe('2 live');
  });
});

describe('artifacts', () => {
  const art = (n: number, title = `Placeholder ${n}`): Artifact => ({ title, url: `https://claude.ai/artifact/exampleArtifact${String(n).padStart(4, '0')}` });

  it('links only a claude.ai artifact address', () => {
    expect(artifactHref('https://claude.ai/artifact/exampleArtifact0001')).toBe('https://claude.ai/artifact/exampleArtifact0001');
    expect(artifactHref('https://claude.ai/code/artifact/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')).not.toBeNull();
    for (const bad of [
      'javascript:alert(1)',
      'http://claude.ai/artifact/exampleArtifact0001',
      'https://claude.ai.example/artifact/exampleArtifact0001',
      'https://claude.ai/artifact/exampleArtifact0001?x',
      'https://claude.ai/artifact/exampleArtifact0001\n',
      '//claude.ai/artifact/exampleArtifact0001',
      '',
    ])
      expect(artifactHref(bad)).toBeNull();
  });

  it('a blank title says so; a title is cleaned', () => {
    expect(artifactTitle(art(1, ''))).toBe('Untitled artifact');
    expect(artifactTitle(art(1, ' \u202e '))).toBe('Untitled artifact');
    expect(artifactTitle(art(1, 'Placeholder\u0007 plan'))).toBe('Placeholder plan');
  });

  it('three lines at most, the last one "+N more", like pull requests', () => {
    const list = [1, 2, 3, 4, 5, 6].map((n) => art(n));
    expect(cardArtifacts(list.slice(0, 3))).toEqual({ shown: list.slice(0, 3), more: null });
    expect(cardArtifacts(list)).toEqual({ shown: list.slice(0, 2), more: '+4 more' });
  });

  it('leaves out a link that is not an artifact address, and counts only the rest', () => {
    const list = [art(1), { title: 'x', url: 'javascript:alert(1)' }, art(2), art(3)];
    expect(cardArtifacts(list)).toEqual({ shown: [art(1), art(2), art(3)], more: null });
  });
});
