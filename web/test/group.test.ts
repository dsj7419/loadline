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

  it('keeps the server order within a group', () => {
    const sessions = [
      sessionOf({ id: 'w1', state: 'working' }),
      sessionOf({ id: 'c', state: 'compacting' }),
      sessionOf({ id: 'n', state: 'needs_you' }),
      sessionOf({ id: 'w2', state: 'working' }),
    ];
    expect(summary(groupSessions(sessions))).toEqual([
      ['needs_you', 'Needs you', ['n']],
      ['working', 'Working', ['w1', 'c', 'w2']],
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
