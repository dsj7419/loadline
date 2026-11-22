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
 * Sorts sessions into groups by state, and leaves out empty groups. Within a
 * group the newest session comes first, then the lowest ID. The server's own
 * order moves with every status line; this one moves only when a session
 * changes group, so rows hold still under the reader's eyes and keyboard.
 */
export function groupSessions(sessions: readonly SessionView[]): readonly Group[] {
  const ordered = [...sessions].sort(newestFirst);
  return groups
    .map(({ key, label }) => ({
      key,
      label,
      sessions: ordered.filter(({ state }) => groupOf[state] === key),
    }))
    .filter((group) => group.sessions.length > 0);
}

function newestFirst(a: SessionView, b: SessionView): number {
  const byStart = Date.parse(b.startedAt) - Date.parse(a.startedAt);
  if (byStart !== 0) {
    return byStart;
  }
  if (a.id === b.id) {
    return 0;
  }
  return a.id < b.id ? -1 : 1;
}
