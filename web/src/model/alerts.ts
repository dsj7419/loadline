import { isTimestamp } from '../api/guard';
import type { SessionView } from '../api/types';

/** The page title, led by the number of current alerts when there are any: (2) Loadline. */
export function alertTitle(count: number): string {
  return Number.isInteger(count) && count > 0 ? `(${count}) Loadline` : 'Loadline';
}

/**
 * How many milliseconds the session has waited on the user, or null when it
 * is not waiting. A browser clock that is behind the server's gives 0.
 */
export function needsYouSince(session: SessionView, now: Date): number | null {
  if (session.state !== 'needs_you' || !isTimestamp(session.stateAt)) {
    return null;
  }
  const waited = now.getTime() - Date.parse(session.stateAt);
  return Number.isNaN(waited) ? null : Math.max(0, waited);
}
