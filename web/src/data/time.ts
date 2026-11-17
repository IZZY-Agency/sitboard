// Durations for times in state. Times and counters update without easing.

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** A duration as the board shows it: "now", "13m", "11h 57m", "3d 17h". Never negative. */
export function duration(ms: number): string {
  if (!Number.isFinite(ms) || ms < MINUTE) return 'now';
  if (ms < HOUR) return `${Math.floor(ms / MINUTE)}m`;
  if (ms < DAY) return `${Math.floor(ms / HOUR)}h ${Math.floor((ms % HOUR) / MINUTE)}m`;
  return `${Math.floor(ms / DAY)}d ${Math.floor((ms % DAY) / HOUR)}h`;
}

/** Time from an RFC 3339 instant to now. "" when the instant is absent or unreadable (unknown). */
export function since(at: string | undefined, now: number): string {
  if (!at) return '';
  const t = Date.parse(at);
  if (Number.isNaN(t)) return '';
  return duration(now - t);
}

/** Local wall-clock time, "HH:MM". */
export function clock(at: string): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return '';
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}
