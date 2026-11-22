import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    include: ['test/**/*.test.{ts,tsx}'],
    // Times are shown in the viewer's zone, so tests pin one to get the same
    // results on every machine.
    env: { TZ: 'UTC' },
  },
});
