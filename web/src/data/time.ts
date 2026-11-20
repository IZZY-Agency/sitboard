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

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/**
 * A past moment in local time, as short as is still clear: "14:02" today,
 * "Sun 21:47" within the last six days, "12 Nov 09:00" before that. "" when
 * the instant is unreadable.
 */
export function when(at: string, now: number): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return '';
  const hm = clock(at);
  const today = new Date(now);
  today.setHours(0, 0, 0, 0);
  const day = new Date(d);
  day.setHours(0, 0, 0, 0);
  const daysAgo = Math.round((today.getTime() - day.getTime()) / DAY);
  if (daysAgo <= 0) return hm;
  if (daysAgo < 7) return `${DAYS[d.getDay()]} ${hm}`;
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${hm}`;
}
