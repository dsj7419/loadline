import type { SessionState, SessionView } from '../api/types';

// The groups in the order the page shows them, most urgent first.
const groups = [
  { key: 'needs_you', label: 'Needs you' },
  { key: 'working', label: 'Working' },
  { key: 'idle', label: 'Idle' },
  { key: 'parked', label: 'Parked' },
  { key: 'ended', label: 'Ended' },
] as const;

export type GroupKey = (typeof groups)[number]['key'];

/** Sessions that the page shows under one heading. */
export type Group = {
  readonly key: GroupKey;
  readonly label: string;
  readonly sessions: readonly SessionView[];
};

// A failed turn waits on the user as a prompt does, compacting is part of
// working, and a session that is starting has no work yet.
const groupOf: Readonly<Record<SessionState, GroupKey>> = {
  needs_you: 'needs_you',
  error: 'needs_you',
  working: 'working',
  compacting: 'working',
  starting: 'idle',
  idle: 'idle',
  parked: 'parked',
  ended: 'ended',
};

/**
 * Sorts sessions into groups by state, keeping the server's order within
 * each, and leaves out empty groups.
 */
export function groupSessions(sessions: readonly SessionView[]): readonly Group[] {
  return groups
    .map(({ key, label }) => ({
      key,
      label,
      sessions: sessions.filter(({ state }) => groupOf[state] === key),
    }))
    .filter((group) => group.sessions.length > 0);
}
