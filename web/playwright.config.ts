import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig, devices } from '@playwright/test';
import type { LoadlineOptions } from './e2e/fixtures';

const port = 18289;
const baseURL = `http://127.0.0.1:${port}`;

// The server runs in a home of its own, made fresh for each run, so that it
// never touches the user's Loadline files. Playwright evaluates this file
// again in every worker; the workers inherit the runner's environment, and
// with it the home the runner made.
const homeVariable = 'LOADLINE_E2E_HOME';
const inheritedHome = process.env[homeVariable];
const loadlineHome = inheritedHome !== undefined && inheritedHome !== '' ? inheritedHome : makeHome();

// Only the process that makes the home removes it, and only as it exits:
// Playwright has stopped the server by then, so nothing holds its files open.
function makeHome(): string {
  const home = mkdtempSync(join(tmpdir(), 'loadline-e2e-'));
  process.env[homeVariable] = home;
  process.on('exit', () => {
    try {
      rmSync(home, { recursive: true, force: true, maxRetries: 5 });
    } catch (error) {
      console.warn(`could not remove the test server's home ${home}: ${String(error)}`);
    }
  });
  return home;
}

// CI tests the binary it built; locally the server is compiled from source.
const serve = `serve --demo --no-open --port ${port}`;
const binary = process.env['LOADLINE_BIN'];
const command = binary !== undefined && binary !== '' ? `"${binary}" ${serve}` : `go run ../cmd/loadline ${serve}`;

export default defineConfig<LoadlineOptions>({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: process.env['CI'] !== undefined,
  reporter: 'list',
  use: {
    baseURL,
    loadlineHome,
    trace: 'retain-on-failure',
  },
  // Full Chromium rather than the headless shell, which denies notifications
  // and so shows the page as no user sees it.
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'], channel: 'chromium' } }],
  webServer: {
    command,
    url: `${baseURL}/healthz`,
    env: { LOADLINE_HOME: loadlineHome },
    reuseExistingServer: false,
    // The first `go run` compiles the whole binary.
    timeout: 180_000,
  },
});
