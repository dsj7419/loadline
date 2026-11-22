import { isTimestamp } from '../api/guard';

// How the dashboard writes numbers and times. Every formatter is total: a
// value it cannot show, such as NaN or an unreadable time, comes out as a
// dash. Times are local to the viewer, with English names whatever the
// browser's language, to match the rest of the page.

const dash = '—';

const second = 1_000;
const minute = 60 * second;
const hour = 60 * minute;
const day = 24 * hour;

// A negative amount that rounds to zero reads $0.00, not -$0.00.
const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', signDisplay: 'negative' });

// Millions and up: $1.2M, $3.4B, $5.6T.
const usdCompact = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  notation: 'compact',
  maximumFractionDigits: 1,
});
const compactFrom = 1e6;
// The least amount that would round to $1,000T, past the largest unit.
const compactBeyond = 999.95e12;

// Math.round sends halves up (-2.5 becomes -2); rounding the magnitude sends
// them away from zero instead.
function roundHalfAwayFromZero(n: number): number {
  return Math.sign(n) * Math.round(Math.abs(n));
}

/** A percentage, rounded to a whole number: 93%. */
export function formatPct(n: number): string {
  if (!Number.isFinite(n)) {
    return dash;
  }
  return `${roundHalfAwayFromZero(n)}%`;
}

/** A token count: 999, 241k or 1.2M. */
export function formatTokens(n: number): string {
  if (!Number.isFinite(n) || n < 0) {
    return dash;
  }
  const whole = roundHalfAwayFromZero(n);
  if (whole < 1_000) {
    return String(whole);
  }
  const thousands = roundHalfAwayFromZero(n / 1_000);
  if (thousands < 1_000) {
    return `${thousands}k`;
  }
  return `${roundHalfAwayFromZero(n / 100_000) / 10}M`;
}

/**
 * An amount in US dollars: $12.40, <$0.01 for a fraction of a cent, $1.2M
 * from a million up, and >$999T past that, so no amount can widen the page.
 */
export function formatUSD(n: number): string {
  if (!Number.isFinite(n)) {
    return dash;
  }
  if (n > 0 && n < 0.005) {
    return '<$0.01';
  }
  const size = Math.abs(n);
  if (size >= compactBeyond) {
    return n > 0 ? '>$999T' : '<-$999T';
  }
  return size >= compactFrom ? usdCompact.format(n) : usd.format(n);
}

/** An amount as formatUSD shows it, without the cents of a whole amount: $20, but $13.60. */
export function formatUSDBrief(n: number): string {
  const full = formatUSD(n);
  return full.endsWith('.00') ? full.slice(0, -'.00'.length) : full;
}

/** An elapsed time in its two largest units: 45s, 1m 12s, 2h 14m or 3d 4h. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) {
    return dash;
  }
  const seconds = Math.floor(ms / second);
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m ${seconds % 60}s`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${hours}h ${minutes % 60}m`;
  }
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

/**
 * When a rate-limit window resets: in 42m within the hour (rounded up, as a
 * countdown is, so it never shows less time than is left), Mon 09:00 within
 * the week, and Nov 28 09:00 further out.
 */
export function formatReset(iso: string, now: Date): string {
  if (!isTimestamp(iso)) {
    return dash;
  }
  const at = new Date(iso);
  const ahead = at.getTime() - now.getTime();
  if (Number.isNaN(ahead)) {
    return dash;
  }
  if (ahead <= 0) {
    return 'now';
  }
  if (ahead < minute) {
    return 'in <1m';
  }
  if (ahead < hour) {
    return `in ${Math.ceil(ahead / minute)}m`;
  }
  if (ahead < 7 * day) {
    return `${nameOf(at, { weekday: 'short' })} ${clockTime(at)}`;
  }
  return `${nameOf(at, { month: 'short' })} ${at.getDate()} ${clockTime(at)}`;
}

/**
 * How long ago a time was: just now, 42s ago, 5m ago, 2h ago or 3d ago; a
 * future time reads in 5m.
 */
export function formatRelative(iso: string, now: Date): string {
  if (!isTimestamp(iso)) {
    return dash;
  }
  const ago = now.getTime() - Date.parse(iso);
  if (Number.isNaN(ago)) {
    return dash;
  }
  // Within a few seconds either way, which also absorbs a small difference
  // between the browser's clock and the server's.
  if (Math.abs(ago) < 10 * second) {
    return 'just now';
  }
  const span = largestUnit(Math.abs(ago));
  return ago > 0 ? `${span} ago` : `in ${span}`;
}

function largestUnit(ms: number): string {
  if (ms < minute) {
    return `${Math.floor(ms / second)}s`;
  }
  if (ms < hour) {
    return `${Math.floor(ms / minute)}m`;
  }
  if (ms < day) {
    return `${Math.floor(ms / hour)}h`;
  }
  return `${Math.floor(ms / day)}d`;
}

/**
 * When something happened, as a local clock time: 17:14 today, Fri 23:59 on
 * the six days either side, Nov 14 18:00 further off, and Dec 31 2025 23:00
 * in another year. A weekday alone never names the same weekday as today.
 */
export function formatWhen(iso: string, now: Date): string {
  if (!isTimestamp(iso) || Number.isNaN(now.getTime())) {
    return dash;
  }
  const at = new Date(iso);
  const days = Math.abs(calendarDaysBetween(at, now));
  if (days === 0) {
    return clockTime(at);
  }
  if (days < 7) {
    return `${nameOf(at, { weekday: 'short' })} ${clockTime(at)}`;
  }
  const year = at.getFullYear() === now.getFullYear() ? '' : ` ${at.getFullYear()}`;
  return `${nameOf(at, { month: 'short' })} ${at.getDate()}${year} ${clockTime(at)}`;
}

// How many local calendar days separate a from b. Rounding absorbs the
// 23- and 25-hour days that daylight saving makes.
function calendarDaysBetween(a: Date, b: Date): number {
  const midnight = (d: Date): number => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  return Math.round((midnight(b) - midnight(a)) / day);
}

// A weekday or month name in English, in the viewer's time zone.
function nameOf(date: Date, part: Pick<Intl.DateTimeFormatOptions, 'weekday' | 'month'>): string {
  return date.toLocaleDateString('en-US', part);
}

// A 24-hour local time: 09:00.
function clockTime(date: Date): string {
  return `${twoDigits(date.getHours())}:${twoDigits(date.getMinutes())}`;
}

function twoDigits(n: number): string {
  return String(n).padStart(2, '0');
}
