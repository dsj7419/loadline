import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  formatDuration,
  formatPct,
  formatRelative,
  formatReset,
  formatTokens,
  formatUSD,
  formatWhen,
} from '../src/model/format';

const notNumbers = [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY];

// A Saturday. Tests run in UTC, so local times read as written.
const now = new Date('2026-11-21T18:00:00Z');

describe('formatPct', () => {
  it.each<[number, string]>([
    [0, '0%'],
    [93, '93%'],
    [92.5, '93%'],
    [92.49, '92%'],
    [0.5, '1%'],
    [0.4, '0%'],
    [100, '100%'],
    [101.2, '101%'],
    [-0.4, '0%'],
    [-2.5, '-3%'],
  ])('shows %s as %s', (n, want) => {
    expect(formatPct(n)).toBe(want);
  });

  it.each(notNumbers)('shows %s as a dash', (n) => {
    expect(formatPct(n)).toBe('—');
  });
});

describe('formatTokens', () => {
  it.each<[number, string]>([
    [0, '0'],
    [999, '999'],
    [999.4, '999'],
    [999.5, '1k'],
    [1_000, '1k'],
    [1_499, '1k'],
    [1_500, '2k'],
    [241_000, '241k'],
    [241_499, '241k'],
    [999_499, '999k'],
    [999_500, '1M'],
    [1_000_000, '1M'],
    [1_200_000, '1.2M'],
    [1_249_999, '1.2M'],
    [1_250_000, '1.3M'],
    [1_960_000, '2M'],
    [2_000_000, '2M'],
    [12_345_678, '12.3M'],
  ])('shows %s as %s', (n, want) => {
    expect(formatTokens(n)).toBe(want);
  });

  it.each([-1, -0.5, ...notNumbers])('shows %s as a dash', (n) => {
    expect(formatTokens(n)).toBe('—');
  });
});

describe('formatUSD', () => {
  it.each<[number, string]>([
    [0, '$0.00'],
    [-0, '$0.00'],
    [0.001, '<$0.01'],
    [0.0049, '<$0.01'],
    [0.005, '$0.01'],
    [0.125, '$0.13'],
    [12.4, '$12.40'],
    [1_240.5, '$1,240.50'],
    [1_234_567.891, '$1,234,567.89'],
    [-3.5, '-$3.50'],
    [-0.001, '$0.00'],
  ])('shows %s as %s', (n, want) => {
    expect(formatUSD(n)).toBe(want);
  });

  it.each(notNumbers)('shows %s as a dash', (n) => {
    expect(formatUSD(n)).toBe('—');
  });
});

describe('formatDuration', () => {
  const second = 1_000;
  const minute = 60 * second;
  const hour = 60 * minute;
  const day = 24 * hour;

  it.each<[number, string]>([
    [0, '0s'],
    [999, '0s'],
    [45 * second, '45s'],
    [minute - 1, '59s'],
    [minute, '1m 0s'],
    [minute + 12 * second, '1m 12s'],
    [hour - 1, '59m 59s'],
    [hour, '1h 0m'],
    [2 * hour + 14 * minute, '2h 14m'],
    [day - 1, '23h 59m'],
    [day, '1d 0h'],
    [3 * day + 4 * hour, '3d 4h'],
  ])('shows %s ms as %s', (ms, want) => {
    expect(formatDuration(ms)).toBe(want);
  });

  it.each([-1, ...notNumbers])('shows %s ms as a dash', (ms) => {
    expect(formatDuration(ms)).toBe('—');
  });
});

describe('formatReset', () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it.each<[string, string]>([
    ['2026-11-21T17:00:00Z', 'now'],
    ['2026-11-21T18:00:00Z', 'now'],
    ['2026-11-21T18:00:30Z', 'in <1m'],
    ['2026-11-21T18:00:59.999Z', 'in <1m'],
    ['2026-11-21T18:01:00Z', 'in 1m'],
    ['2026-11-21T18:41:01Z', 'in 42m'],
    ['2026-11-21T18:42:00Z', 'in 42m'],
    ['2026-11-21T18:59:59Z', 'in 60m'],
    ['2026-11-21T19:00:00Z', 'Sat 19:00'],
    ['2026-11-23T09:00:00Z', 'Mon 09:00'],
    ['2026-11-28T17:59:00Z', 'Sat 17:59'],
    ['2026-11-28T18:00:00Z', 'Nov 28 18:00'],
    ['2026-12-05T09:05:00Z', 'Dec 5 09:05'],
  ])('shows a reset at %s as %s', (iso, want) => {
    expect(formatReset(iso, now)).toBe(want);
  });

  it('shows the time in the time zone of the viewer', () => {
    vi.stubEnv('TZ', 'Asia/Tokyo');
    // Each time falls on the next day in Tokyo: one a new weekday, the other
    // a new month.
    expect(formatReset('2026-11-22T20:00:00Z', now)).toBe('Mon 05:00');
    expect(formatReset('2026-11-30T15:30:00Z', now)).toBe('Dec 1 00:30');
  });

  // Date.parse reads '42' as the year 2042, so only a strict check refuses it.
  it.each(['soon', '', '42', '2026-13-45T00:00:00Z'])('shows %j as a dash', (iso) => {
    expect(formatReset(iso, now)).toBe('—');
  });

  it('shows a dash when now is not a time', () => {
    expect(formatReset('2026-11-23T09:00:00Z', new Date(Number.NaN))).toBe('—');
  });
});

describe('formatRelative', () => {
  it.each<[string, string]>([
    ['2026-11-21T18:00:00Z', 'just now'],
    ['2026-11-21T17:59:50.001Z', 'just now'],
    ['2026-11-21T17:59:50Z', '10s ago'],
    ['2026-11-21T17:59:18Z', '42s ago'],
    ['2026-11-21T17:59:00.001Z', '59s ago'],
    ['2026-11-21T17:59:00Z', '1m ago'],
    ['2026-11-21T17:55:00Z', '5m ago'],
    ['2026-11-21T17:00:00.001Z', '59m ago'],
    ['2026-11-21T17:00:00Z', '1h ago'],
    ['2026-11-21T16:00:00Z', '2h ago'],
    ['2026-11-20T18:00:00.001Z', '23h ago'],
    ['2026-11-20T18:00:00Z', '1d ago'],
    ['2026-11-18T18:00:00Z', '3d ago'],
    ['2026-11-21T18:00:05Z', 'just now'],
    ['2026-11-21T18:00:42Z', 'in 42s'],
    ['2026-11-21T18:05:00Z', 'in 5m'],
    ['2026-11-21T20:00:00Z', 'in 2h'],
    ['2026-11-24T18:00:00Z', 'in 3d'],
  ])('shows %s as %s', (iso, want) => {
    expect(formatRelative(iso, now)).toBe(want);
  });

  it.each(['yesterday', '', '42', '2026-11-21T25:00:00Z'])('shows %j as a dash', (iso) => {
    expect(formatRelative(iso, now)).toBe('—');
  });

  it('shows a dash when now is not a time', () => {
    expect(formatRelative('2026-11-21T17:55:00Z', new Date(Number.NaN))).toBe('—');
  });
});

describe('formatWhen', () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it.each<[string, string]>([
    ['2026-11-21T17:14:00Z', '17:14'],
    ['2026-11-21T00:00:00Z', '00:00'],
    ['2026-11-21T23:59:00Z', '23:59'],
    ['2026-11-20T23:59:00Z', 'Fri 23:59'],
    ['2026-11-15T09:05:00Z', 'Sun 09:05'],
    ['2026-11-14T18:00:00Z', 'Nov 14 18:00'],
    ['2026-11-02T08:30:00Z', 'Nov 2 08:30'],
    ['2025-12-31T23:00:00Z', 'Dec 31 2025 23:00'],
  ])('shows %s as %s', (iso, want) => {
    expect(formatWhen(iso, now)).toBe(want);
  });

  it('shows the time in the time zone of the viewer', () => {
    vi.stubEnv('TZ', 'Asia/Tokyo');
    // 02:30 on Sunday in Tokyo, which is today there.
    expect(formatWhen('2026-11-21T17:30:00Z', new Date('2026-11-21T18:00:00Z'))).toBe('02:30');
    expect(formatWhen('2026-11-21T14:30:00Z', new Date('2026-11-21T18:00:00Z'))).toBe('Sat 23:30');
  });

  it.each(['soon', '', '42'])('shows %j as a dash', (iso) => {
    expect(formatWhen(iso, now)).toBe('—');
  });

  it('shows a dash when now is not a time', () => {
    expect(formatWhen('2026-11-21T17:14:00Z', new Date(Number.NaN))).toBe('—');
  });
});
