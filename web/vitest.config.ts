import { defineConfig } from 'vitest/config';

export default defineConfig({
  // The tests sit outside tsconfig.json's include, so the JSX settings there
  // do not reach them; the transform is told directly.
  oxc: {
    jsx: { runtime: 'automatic', importSource: 'preact' },
  },
  test: {
    include: ['test/**/*.test.{ts,tsx}'],
    // Times are shown in the viewer's zone, so tests pin one to get the same
    // results on every machine.
    env: { TZ: 'UTC' },
  },
});
