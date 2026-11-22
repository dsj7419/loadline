import { describe, expect, it } from 'vitest';

describe('the test environment', () => {
  it('runs in UTC whatever the machine zone', () => {
    expect(Intl.DateTimeFormat().resolvedOptions().timeZone).toBe('UTC');
    expect(new Date('2026-07-01T12:00:00Z').getHours()).toBe(12);
  });
});
