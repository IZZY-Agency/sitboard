// The page's actions: jump to the card's terminal, mark it seen, forget a
// project's grouping, and set a folder's grouping. The page sends a card or
// project id (and a mode) only; the server resolves everything else.

import { Paths, type GroupingMode } from '../contract.gen';

export type ActionResult =
  | { ok: true }
  | { ok: false; kind: 'no_terminal' } // 409: the session has no terminal to focus
  | { ok: false; kind: 'unauthorized' } // 401 or 403: this tab lost its key
  | { ok: false; kind: 'failed'; message: string; final?: boolean }; // final: trying again would fail the same way

export interface Actions {
  jump(cardId: string): Promise<ActionResult>;
  seen(cardId: string): Promise<ActionResult>;
  forgetGrouping(projectId: string): Promise<ActionResult>;
  setGrouping(projectId: string, mode: GroupingMode): Promise<ActionResult>;
}

/** The jump failures the server reports with a 503, shown as they are. */
const JUMP_FAILURES = new Set(['agterm not found', 'agterm did not answer', 'tmux not found', 'tmux did not answer', 'no tmux client is attached']);

export function createActions(fetchFn: typeof fetch): Actions {
  async function post(path: string, body: object): Promise<ActionResult> {
    let res: Response;
    try {
      res = await fetchFn(path, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
    } catch {
      return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
    }
    if (res.ok) return { ok: true };
    if (res.status === 401 || res.status === 403) return { ok: false, kind: 'unauthorized' };
    if (path === Paths.forgetGrouping) {
      // The server's own reasons: 404 and 409 stay so, a 500 is a state file it could not write.
      if (res.status === 404) return { ok: false, kind: 'failed', message: 'this project is no longer on the board', final: true };
      if (res.status === 409) return { ok: false, kind: 'failed', message: 'it is no longer grouped', final: true };
      if (res.status === 500) return { ok: false, kind: 'failed', message: 'sitboard could not save it' };
    }
    if (path === Paths.groupingSetting) {
      if (res.status === 404) return { ok: false, kind: 'failed', message: 'it is no longer on the board', final: true };
      if (res.status === 409) {
        // The server says which: too many settings, or no longer a grouping.
        const why = await res.text().catch(() => '');
        if (why.includes('too many')) return { ok: false, kind: 'failed', message: 'too many folders have a setting already', final: true };
        return { ok: false, kind: 'failed', message: 'it is no longer grouped', final: true };
      }
      if (res.status === 500) return { ok: false, kind: 'failed', message: 'sitboard could not save it' };
    }
    if (path === Paths.jump && res.status === 503) {
      // The server names what failed (internal/server/actions.go); only those known texts are shown.
      const why = (await res.text().catch(() => '')).trim();
      if (JUMP_FAILURES.has(why)) return { ok: false, kind: 'failed', message: why };
    }
    if (res.status === 409) return { ok: false, kind: 'no_terminal' };
    if (res.status === 404) return { ok: false, kind: 'failed', message: 'this card is no longer on the board' };
    return { ok: false, kind: 'failed', message: `sitboard answered ${res.status}` };
  }
  return {
    jump: (cardId) => post(Paths.jump, { cardId }),
    seen: (cardId) => post(Paths.seen, { cardId }),
    forgetGrouping: (projectId) => post(Paths.forgetGrouping, { projectId }),
    setGrouping: (projectId, mode) => post(Paths.groupingSetting, { projectId, mode }),
  };
}
