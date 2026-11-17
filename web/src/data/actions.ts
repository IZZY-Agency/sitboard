// The two actions this cut ships: jump to the card's terminal, and mark it seen.
// The page sends a card id only; the server resolves everything else.

import { Paths } from '../contract.gen';

export type ActionResult =
  | { ok: true }
  | { ok: false; kind: 'no_terminal' } // 409: the session has no terminal to focus
  | { ok: false; kind: 'unauthorized' } // 401 or 403: this tab lost its key
  | { ok: false; kind: 'failed'; message: string };

export interface Actions {
  jump(cardId: string): Promise<ActionResult>;
  seen(cardId: string): Promise<ActionResult>;
}

export function createActions(fetchFn: typeof fetch): Actions {
  async function post(path: string, cardId: string): Promise<ActionResult> {
    let res: Response;
    try {
      res = await fetchFn(path, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ cardId }),
      });
    } catch {
      return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
    }
    if (res.ok) return { ok: true };
    if (res.status === 409) return { ok: false, kind: 'no_terminal' };
    if (res.status === 401 || res.status === 403) return { ok: false, kind: 'unauthorized' };
    if (res.status === 404) return { ok: false, kind: 'failed', message: 'this card is no longer on the board' };
    return { ok: false, kind: 'failed', message: `sitboard answered ${res.status}` };
  }
  return {
    jump: (id) => post(Paths.jump, id),
    seen: (id) => post(Paths.seen, id),
  };
}
