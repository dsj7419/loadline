import { describe, expect, it } from 'vitest';
import { sessionStates, type SessionState } from '../src/api/types';
import { isCurrent, sessionTitle, shareOfAccount, stateLabels, timeInState, windowNote } from '../src/model/describe';
import { accountOf, sessionOf } from './fixtures';

// A Saturday. Tests run in UTC, so local times read as written.
const now = new Date('2026-11-21T18:00:00Z');

function windowOf(fields: Record<string, unknown>) {
  const week = accountOf({
    sevenDay: { usedPct: 93, resetsAt: '2026-11-23T09:00:00Z', observedAt: '2026-11-21T17:59:00Z', projection: null, ...fields },
  }).sevenDay;
  if (week === null) {
    throw new Error('the fixture lost its window');
  }
  return week;
}

describe('stateLabels', () => {
  it('names every state', () => {
    for (const state of sessionStates) {
      expect(stateLabels[state]).toMatch(/^[A-Z][a-z]+( [a-z]+)?$/);
    }
    expect(stateLabels.needs_you).toBe('Needs you');
  });
});

describe('sessionTitle', () => {
  it('is the name, or the start of the ID for a session without one', () => {
    expect(sessionTitle(sessionOf({ name: 'harbor-api' }))).toBe('harbor-api');
    expect(sessionTitle(sessionOf({ name: '', id: '6d1f0c2a-41b7-4e5d' }))).toBe('6d1f0c2a');
  });
});

describe('timeInState', () => {
  it.each<[SessionState, string, string]>([
    ['needs_you', 'permission prompt', 'waiting 1m 12s (permission prompt)'],
    ['needs_you', '', 'waiting 1m 12s'],
    ['working', '', 'working 1m 12s'],
    ['compacting', '', 'compacting 1m 12s'],
    ['idle', '', 'idle 1m 12s'],
    ['starting', '', 'starting 1m 12s'],
    ['error', '', 'failed 1m 12s'],
    ['parked', '', 'parked 1m ago'],
    ['ended', '', 'ended 1m ago'],
  ])('describes a %s session waiting for %j as %s', (state, waitReason, want) => {
    expect(timeInState(sessionOf({ state, waitReason, stateAt: '2026-11-21T17:58:48Z' }), now)).toBe(want);
  });

  it('counts no time when the browser clock is behind the server', () => {
    expect(timeInState(sessionOf({ state: 'working', stateAt: '2026-11-21T18:00:05Z' }), now)).toBe('working 0s');
  });

  it('shows a dash for the time when now is not a time', () => {
    expect(timeInState(sessionOf({ state: 'idle' }), new Date(Number.NaN))).toBe('idle —');
  });
});

describe('shareOfAccount', () => {
  it.each<[number, string | null]>([
    [0.41, "~41% of Main's last hour"],
    [1, "~100% of Main's last hour"],
    [0.004, "<1% of Main's last hour"],
    [0, null],
    [-0.2, null],
  ])('describes a share of %s as %j', (share, want) => {
    expect(shareOfAccount(sessionOf({ share, account: 'Main' }))).toBe(want);
  });
});

describe('windowNote', () => {
  it('gives the burn rate, when it runs out and when it resets for a window that runs out first', () => {
    const w = windowOf({
      projection: { ratePerHour: 2.13, atReset: 100, eta: '2026-11-22T14:00:00Z', runsOutBeforeReset: true },
    });
    expect(windowNote(w, now)).toEqual({ text: '+2.1%/h · runs out Sun 14:00 · resets Mon 09:00', runsOut: true });
  });

  // A window at or past its limit has run out already rather than being on
  // pace to, and its burn rate no longer says anything.
  it.each([100, 120])('says a window at %d percent is at its limit, and when it resets', (usedPct) => {
    const w = windowOf({
      usedPct,
      projection: { ratePerHour: 2.13, atReset: 100, eta: '2026-11-21T18:00:00Z', runsOutBeforeReset: true },
    });
    expect(windowNote(w, now)).toEqual({ text: 'at limit · resets Mon 09:00', runsOut: true });
  });

  it('says a window is at its limit while its trend is unknown, or its reset', () => {
    expect(windowNote(windowOf({ usedPct: 100 }), now)).toEqual({ text: 'at limit · resets Mon 09:00', runsOut: true });
    expect(windowNote(windowOf({ usedPct: 100, resetsAt: null }), now)).toEqual({ text: 'at limit', runsOut: true });
  });

  it('gives a window just short of its limit when it runs out', () => {
    const w = windowOf({
      usedPct: 99.9,
      projection: { ratePerHour: 2.13, atReset: 100, eta: '2026-11-21T18:03:00Z', runsOutBeforeReset: true },
    });
    expect(windowNote(w, now)?.text).toBe('+2.1%/h · runs out in 3m · resets Mon 09:00');
  });

  it('gives when it resets and where it is heading for a window on pace', () => {
    const w = windowOf({
      resetsAt: '2026-11-27T14:00:00Z',
      projection: { ratePerHour: 0.3, atReset: 22.4, eta: '2026-12-04T00:00:00Z', runsOutBeforeReset: false },
    });
    expect(windowNote(w, now)).toEqual({ text: 'resets Fri 14:00 · +0.3%/h · on pace for 22% at reset', runsOut: false });
  });

  it('leaves the rate out when usage is not rising', () => {
    const w = windowOf({ projection: { ratePerHour: 0, atReset: 93, eta: null, runsOutBeforeReset: false } });
    expect(windowNote(w, now)).toEqual({ text: 'resets Mon 09:00 · on pace for 93% at reset', runsOut: false });
  });

  it('gives only the reset while the trend is unknown', () => {
    expect(windowNote(windowOf({}), now)).toEqual({ text: 'resets Mon 09:00', runsOut: false });
  });

  it('gives nothing without a reset time or a trend', () => {
    expect(windowNote(windowOf({ resetsAt: null }), now)).toBeNull();
  });

  it('counts down a reset within the hour', () => {
    expect(windowNote(windowOf({ resetsAt: '2026-11-21T18:42:00Z' }), now)?.text).toBe('resets in 42m');
  });
});

describe('isCurrent', () => {
  it.each<[string | null, boolean]>([
    ['2026-11-21T18:00:01Z', true],
    ['2026-11-21T18:00:00Z', false],
    ['2026-11-21T12:00:00Z', false],
    [null, true],
  ])('holds a window resetting at %s current: %s', (resetsAt, want) => {
    expect(isCurrent(windowOf({ resetsAt }), now)).toBe(want);
  });
});
