import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { test as base, expect, type Page } from '@playwright/test';

/** What playwright.config.ts tells the tests about the server it started. */
export type LoadlineOptions = {
  /** The server's LOADLINE_HOME, which holds its ingest token. */
  readonly loadlineHome: string;
};

type Fixtures = {
  /** A page signed in to the dashboard, showing the demo machine. */
  readonly dashboard: Page;
};

export const test = base.extend<Fixtures, LoadlineOptions>({
  loadlineHome: ['', { scope: 'worker', option: true }],
  dashboard: async ({ page, loadlineHome }, use) => {
    await signIn(page, loadlineHome);
    await use(page);
  },
});

export { expect };

/**
 * Signs page in the way `loadline open` does: mints a one-time login code
 * with the ingest token, then opens the link that trades the code for the UI
 * cookie. It returns once the dashboard shows the server's first view.
 */
export async function signIn(page: Page, loadlineHome: string): Promise<void> {
  const token = await readToken(loadlineHome);
  const minted = await page.request.post('/api/login-code', { headers: { 'x-loadline-token': token } });
  expect(minted.status(), 'POST /api/login-code').toBe(200);
  const code = loginCode(await minted.json());

  await page.goto(`/login?code=${encodeURIComponent(code)}`);
  await expect(page).toHaveURL('/');
  await expect(page.getByRole('region', { name: 'Sessions' })).toBeVisible();
}

async function readToken(loadlineHome: string): Promise<string> {
  if (loadlineHome === '') {
    throw new Error('loadlineHome is not set: run the suite through playwright.config.ts');
  }
  return (await readFile(join(loadlineHome, 'token'), 'utf8')).trim();
}

function loginCode(body: unknown): string {
  if (typeof body === 'object' && body !== null && 'code' in body && typeof body.code === 'string' && body.code !== '') {
    return body.code;
  }
  throw new Error('POST /api/login-code returned no code');
}

/**
 * Records everything that goes wrong in page from the call on: console
 * errors, uncaught exceptions, requests that fail or are answered with an
 * error status, and content security policy violations. Call it before the
 * page's first navigation.
 */
export async function watchForProblems(page: Page): Promise<readonly string[]> {
  const problems: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error') {
      problems.push(`console error: ${message.text()}`);
    }
  });
  page.on('pageerror', (error) => problems.push(`uncaught exception: ${error.message}`));
  page.on('requestfailed', (request) => {
    problems.push(`request failed: ${request.method()} ${request.url()} (${request.failure()?.errorText ?? 'no reason given'})`);
  });
  page.on('response', (response) => {
    if (response.status() >= 400) {
      problems.push(`HTTP ${response.status()}: ${response.request().method()} ${response.url()}`);
    }
  });
  // Each violation also fires an event in the page, whether or not the
  // browser logs it. Raised as a console error, it is recorded above.
  await page.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (event) => {
      console.error(`CSP violation: ${event.violatedDirective} blocked ${event.blockedURI || 'inline content'}`);
    });
  });
  return problems;
}
