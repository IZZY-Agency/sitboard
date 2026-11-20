import { describe, expect, it } from 'vitest';
import { initial, SCENARIOS } from '../dev/scenarios';

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
