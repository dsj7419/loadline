import { describe, expect, it } from 'vitest';
import { sessionStates, type SessionState } from '../src/api/types';
import { groupSessions, type GroupKey } from '../src/model/group';
import { sessionOf } from './fixtures';

function summary(groups: ReturnType<typeof groupSessions>): [GroupKey, string, string[]][] {
  return groups.map(({ key, label, sessions }) => [key, label, sessions.map(({ id }) => id)]);
}

describe('groupSessions', () => {
  it('orders the groups needs you, working, idle, parked, ended', () => {
    const sessions = [
      sessionOf({ id: 'e', state: 'ended' }),
      sessionOf({ id: 'i', state: 'idle' }),
      sessionOf({ id: 'p', state: 'parked' }),
      sessionOf({ id: 'w', state: 'working' }),
      sessionOf({ id: 'n', state: 'needs_you' }),
    ];
    expect(summary(groupSessions(sessions))).toEqual([
      ['needs_you', 'Needs you', ['n']],
      ['working', 'Working', ['w']],
      ['idle', 'Idle', ['i']],
      ['parked', 'Parked', ['p']],
      ['ended', 'Ended', ['e']],
    ]);
  });

  it.each<[SessionState, GroupKey]>([
    ['needs_you', 'needs_you'],
    ['error', 'needs_you'],
    ['working', 'working'],
    ['compacting', 'working'],
    ['idle', 'idle'],
    ['starting', 'idle'],
    ['parked', 'parked'],
    ['ended', 'ended'],
  ])('puts a %s session under %s', (state, key) => {
    expect(groupSessions([sessionOf({ state })]).map((group) => group.key)).toEqual([key]);
  });

  it('keeps every session, whatever its state', () => {
    const sessions = sessionStates.map((state) => sessionOf({ id: state, state }));
    const grouped = groupSessions(sessions).flatMap((group) => group.sessions.map(({ id }) => id));
    expect(grouped.sort()).toEqual([...sessionStates].sort());
  });

  it('orders each group by when its sessions started, newest first, then by ID', () => {
    const sessions = [
      sessionOf({ id: 'old', state: 'working', startedAt: '2026-11-21T15:00:00Z' }),
      sessionOf({ id: 'b-new', state: 'compacting', startedAt: '2026-11-21T17:00:00Z' }),
      sessionOf({ id: 'n', state: 'needs_you' }),
      sessionOf({ id: 'a-new', state: 'working', startedAt: '2026-11-21T17:00:00Z' }),
      sessionOf({ id: 'mid', state: 'working', startedAt: '2026-11-21T16:00:00Z' }),
    ];
    expect(summary(groupSessions(sessions))).toEqual([
      ['needs_you', 'Needs you', ['n']],
      ['working', 'Working', ['a-new', 'b-new', 'mid', 'old']],
    ]);
  });

  it('ignores the order the server lists sessions in, which moves with every status line', () => {
    const first = sessionOf({ id: 'first', state: 'idle', startedAt: '2026-11-21T17:00:00Z', lastSeenAt: '2026-11-21T17:59:00Z' });
    const second = sessionOf({ id: 'second', state: 'idle', startedAt: '2026-11-21T16:00:00Z', lastSeenAt: '2026-11-21T18:00:00Z' });
    const seenLater = sessionOf({ id: 'first', state: 'idle', startedAt: '2026-11-21T17:00:00Z', lastSeenAt: '2026-11-21T18:00:05Z' });

    expect(summary(groupSessions([second, first]))).toEqual([['idle', 'Idle', ['first', 'second']]]);
    expect(summary(groupSessions([seenLater, second]))).toEqual([['idle', 'Idle', ['first', 'second']]]);
  });

  it('moves a session that changes state to its new group, and keeps the order inside each group', () => {
    const at = (hour: number) => `2026-11-21T${String(hour).padStart(2, '0')}:00:00Z`;
    const before = [
      sessionOf({ id: 'a', state: 'working', startedAt: at(10) }),
      sessionOf({ id: 'b', state: 'working', startedAt: at(12) }),
      sessionOf({ id: 'c', state: 'working', startedAt: at(11) }),
      sessionOf({ id: 'd', state: 'idle', startedAt: at(9) }),
    ];
    const after = before.map((s) => (s.id === 'c' ? sessionOf({ id: 'c', state: 'idle', startedAt: at(11) }) : s));

    expect(summary(groupSessions(before))).toEqual([
      ['working', 'Working', ['b', 'c', 'a']],
      ['idle', 'Idle', ['d']],
    ]);
    expect(summary(groupSessions(after))).toEqual([
      ['working', 'Working', ['b', 'a']],
      ['idle', 'Idle', ['c', 'd']],
    ]);
  });

  it('omits empty groups', () => {
    const sessions = [sessionOf({ id: 'a', state: 'idle' }), sessionOf({ id: 'b', state: 'idle' })];
    expect(summary(groupSessions(sessions))).toEqual([['idle', 'Idle', ['a', 'b']]]);
  });

  it('gives no groups for no sessions', () => {
    expect(groupSessions([])).toEqual([]);
  });
});
