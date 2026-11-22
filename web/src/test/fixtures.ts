// Test helpers only. Never imported by the app, so never in the build.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { CONTRACT_VERSION, type Agent, type Card, type Project, type Snapshot } from '../contract.gen';

export function golden(): Snapshot {
  // Tests run from web/.
  const path = join(process.cwd(), '../internal/contract/golden/snapshot.v1.json');
  return JSON.parse(readFileSync(path, 'utf8')) as Snapshot;
}

let n = 0;

export function card(over: Partial<Omit<Card, 'agent'>> & { agent?: Partial<Agent> } = {}): Card {
  n++;
  const { agent, ...rest } = over;
  return {
    id: `claude:${n}`,
    projectId: 'p_000000000001',
    title: `task ${n}`,
    titleSource: 'session',
    column: 'working',
    attention: [],
    seen: true,
    pullRequests: [],
    issues: [],
    artifacts: [],
    subagents: 0,
    spend: { tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, usd: 0, priced: false, recorded: false },
    ...rest,
    agent: {
      tool: 'claude',
      name: `agent-${n}`,
      model: 'claude-opus-5-5',
      effort: '',
      effortSource: 'none',
      effortWord: '',
      status: 'busy',
      statusSince: '2026-11-17T09:00:00Z',
      live: 'yes',
      folder: '~/projects/one',
      parent: 'none',
      parentCardId: '',
      terminalId: 't1',
      workspace: 'one',
      jump: 'agterm',
      ...agent,
    },
  };
}

export function project(id: string, name: string, counts: Partial<Project['counts']> = {}): Project {
  return {
    id,
    name,
    path: `~/projects/${name}`,
    repo: '',
    repoForge: '',
    repoHost: '',
    parentId: '',
    area: 'projects',
    counts: { working: 0, needsYou: 0, inReview: 0, stale: 0, ...counts },
  };
}

export function snapshot(cards: Card[], projects: Project[], over: Partial<Snapshot> = {}): Snapshot {
  return {
    version: CONTRACT_VERSION,
    seq: 1,
    generatedAt: '2026-11-17T10:00:00Z',
    sources: [],
    projects,
    cards,
    plainShells: 0,
    counts: { working: 0, needsYou: 0, inReview: 0, stale: 0 },
    agtermPresent: true,
    build: { version: 'dev', commit: 'unknown', os: 'darwin', arch: 'arm64' },
    groupingSettings: [],
    leftovers: { items: 0, held: 0, partial: false },
    spend: { days: [], pending: false },
    gitlabHosts: [],
    ...over,
  };
}
