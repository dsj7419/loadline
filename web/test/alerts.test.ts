import { describe, expect, it } from 'vitest';
import { sessionStates } from '../src/api/types';
import { alertTitle, needsYouSince } from '../src/model/alerts';
import { sessionOf } from './fixtures';

describe('alertTitle', () => {
  it.each<[number, string]>([
    [0, 'Loadline'],
    [1, '(1) Loadline'],
    [2, '(2) Loadline'],
    [120, '(120) Loadline'],
    [-1, 'Loadline'],
    [1.5, 'Loadline'],
    [Number.NaN, 'Loadline'],
  ])('titles %s alerts %s', (count, want) => {
    expect(alertTitle(count)).toBe(want);
  });
});

describe('needsYouSince', () => {
  const now = new Date('2026-11-21T18:00:00Z');

  it('gives how long the session has waited on the user, in milliseconds', () => {
    const waiting = sessionOf({ state: 'needs_you', stateAt: '2026-11-21T17:58:48Z' });
    expect(needsYouSince(waiting, now)).toBe(72_000);
  });

  it('gives zero when the browser clock is behind the server', () => {
    const waiting = sessionOf({ state: 'needs_you', stateAt: '2026-11-21T18:00:02Z' });
    expect(needsYouSince(waiting, now)).toBe(0);
  });

  it.each(sessionStates.filter((state) => state !== 'needs_you'))('gives null for a %s session', (state) => {
    expect(needsYouSince(sessionOf({ state, stateAt: '2026-11-21T17:58:48Z' }), now)).toBeNull();
  });

  it('gives null when now is not a time', () => {
    const waiting = sessionOf({ state: 'needs_you', stateAt: '2026-11-21T17:58:48Z' });
    expect(needsYouSince(waiting, new Date(Number.NaN))).toBeNull();
  });
});
