import { isTimestamp } from '../api/guard';
import type { SessionState, SessionView, WindowView } from '../api/types';
import { needsYouSince } from './alerts';
import { formatDuration, formatPct, formatRelative, formatReset } from './format';

// The words the page puts to sessions and rate-limit windows.

/** Each state's name, as the page labels it beside its icon. */
export const stateLabels: Readonly<Record<SessionState, string>> = {
  starting: 'Starting',
  working: 'Working',
  needs_you: 'Needs you',
  idle: 'Idle',
  compacting: 'Compacting',
  error: 'Error',
  parked: 'Parked',
  ended: 'Ended',
};

// How a session's time in its state reads: a live state counts how long, a
// finished one says how long ago.
const timeVerbs: Readonly<Record<SessionState, { readonly verb: string; readonly finished: boolean }>> = {
  starting: { verb: 'starting', finished: false },
  working: { verb: 'working', finished: false },
  needs_you: { verb: 'waiting', finished: false },
  idle: { verb: 'idle', finished: false },
  compacting: { verb: 'compacting', finished: false },
  error: { verb: 'failed', finished: false },
  parked: { verb: 'parked', finished: true },
  ended: { verb: 'ended', finished: true },
};

const idLength = 8;

/** What the page calls a session: its name, or the start of its ID. */
export function sessionTitle(session: SessionView): string {
  return session.name !== '' ? session.name : session.id.slice(0, idLength);
}

/**
 * How long the session has been in its state: waiting 1m 12s (permission
 * prompt), working 4m 2s, or parked 20m ago. A browser clock behind the
 * server's counts no time.
 */
export function timeInState(session: SessionView, now: Date): string {
  const { verb, finished } = timeVerbs[session.state];
  if (finished) {
    return `${verb} ${formatRelative(session.stateAt, now)}`;
  }
  const elapsed = formatDuration(elapsedSince(session, now));
  const reason = session.state === 'needs_you' && session.waitReason !== '' ? ` (${session.waitReason})` : '';
  return `${verb} ${elapsed}${reason}`;
}

function elapsedSince(session: SessionView, now: Date): number {
  const waited = needsYouSince(session, now);
  if (waited !== null) {
    return waited;
  }
  if (!isTimestamp(session.stateAt)) {
    return Number.NaN;
  }
  return Math.max(0, now.getTime() - Date.parse(session.stateAt));
}

/**
 * The session's estimated part of its account's spend over the last hour:
 * ~41% of Main's last hour. Null when it has spent nothing lately.
 */
export function shareOfAccount(session: SessionView): string | null {
  if (!Number.isFinite(session.share) || session.share <= 0) {
    return null;
  }
  const pct = session.share < 0.005 ? '<1%' : `~${formatPct(session.share * 100)}`;
  return `${pct} of ${session.account}'s last hour`;
}

/** What a window gauge says under its bar. */
export type WindowNote = {
  readonly text: string;
  /** The window has reached its limit, or is on pace to before it resets. */
  readonly runsOut: boolean;
};

/** The usage, in percent, at which a window's limit is reached. */
const limitPct = 100;

/**
 * When a window resets and where its usage is heading: +2.1%/h · runs out
 * Sun 14:00 · resets Mon 09:00 for one on pace to hit its limit first, and
 * resets Fri 14:00 · +0.3%/h · on pace for 22% at reset otherwise. Without a
 * trend it gives the reset alone, and without a reset time nothing. A window
 * already at its limit reads at limit · resets Mon 09:00: it has run out, and
 * its burn rate no longer says anything.
 */
export function windowNote(window: WindowView, now: Date): WindowNote | null {
  const resets = window.resetsAt === null ? null : `resets ${formatReset(window.resetsAt, now)}`;
  if (window.usedPct >= limitPct) {
    return { text: joined(['at limit', resets]), runsOut: true };
  }
  const projection = window.projection;
  if (projection === null) {
    return resets === null ? null : { text: resets, runsOut: false };
  }
  const rate = projection.ratePerHour > 0 ? `+${projection.ratePerHour.toFixed(1)}%/h` : null;
  if (projection.runsOutBeforeReset && projection.eta !== null) {
    return { text: joined([rate, `runs out ${formatReset(projection.eta, now)}`, resets]), runsOut: true };
  }
  return { text: joined([resets, rate, `on pace for ${formatPct(projection.atReset)} at reset`]), runsOut: false };
}

function joined(parts: readonly (string | null)[]): string {
  return parts.filter((part) => part !== null).join(' · ');
}

/**
 * Reports whether a window still holds at now. Once its reset time passes,
 * the usage it shows is gone, even before the server says so.
 */
export function isCurrent(window: WindowView, now: Date): boolean {
  return window.resetsAt === null || Date.parse(window.resetsAt) > now.getTime();
}
