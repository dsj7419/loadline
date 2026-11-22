import { expect, test, type Locator, type Page } from '@playwright/test';
import { accountOf, sessionOf, viewOf } from '../test/fixtures';
import { startScriptedServer, type ScriptedServer } from './scripted';

// The narrowest width the page supports.
test.use({ viewport: { width: 360, height: 800 } });

// How far the element scrolls sideways: what overflows it, or 0.
async function sidewaysOverflow(element: Locator): Promise<number> {
  return element.evaluate((node) => Math.max(0, node.scrollWidth - node.clientWidth));
}

function page360(page: Page): Locator {
  return page.locator(':root');
}

test.describe('long text and large amounts', () => {
  let server: ScriptedServer;

  test.beforeEach(async () => {
    const oneLongWord = 'prompt_input_exit_after_the_terminal_window_was_closed_by_the_operating_system';
    server = await startScriptedServer(
      viewOf({
        accounts: [
          accountOf(),
          accountOf({
            dir: 'C:/Users/example/.claude-work',
            label: 'API',
            auth: 'api_key',
            spendTodayUSD: 1e21,
            spendWeekUSD: 1e300,
            spendMonthUSD: 1e300,
            budgetUSD: 1e21,
          }),
        ],
        sessions: [
          sessionOf({
            name: 'harbor-api',
            state: 'ended',
            costUSD: 1e21,
            timeline: [
              { at: '2026-11-21T17:00:00Z', state: 'working', reason: '' },
              { at: '2026-11-21T17:58:00Z', state: 'ended', reason: oneLongWord },
            ],
          }),
        ],
      }),
    );
  });

  test.afterEach(async () => {
    await server.close();
  });

  test('never scroll the page or the details sideways at 360 px', async ({ page }) => {
    await page.goto(server.url);
    await expect(page.getByRole('button', { name: /harbor-api/ })).toBeVisible();
    expect(await sidewaysOverflow(page360(page))).toBe(0);

    await page.getByRole('button', { name: /harbor-api/ }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    expect(await sidewaysOverflow(dialog)).toBe(0);
    expect(await sidewaysOverflow(page360(page))).toBe(0);
  });
});
