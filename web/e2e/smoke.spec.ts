import AxeBuilder from '@axe-core/playwright';
import type { Locator, Page } from '@playwright/test';
import { expect, signIn, test, watchForProblems } from './fixtures';

// The demo machine's session that waits on a permission prompt.
const waitingSession = /copper-lantern/;

test('dashboard renders demo state', async ({ dashboard: page }) => {
  const sessions = page.getByRole('region', { name: 'Sessions' });
  await expect(sessions.getByRole('listitem')).toHaveCount(5);
  await expect(sessions.getByRole('heading', { level: 3 }).first()).toHaveText('Needs you');
  await expect(page.getByRole('article', { name: 'Main', exact: true })).toContainText('93%');
});

test('keyboard can open and close detail', async ({ dashboard: page }) => {
  const row = page.getByRole('button', { name: waitingSession });
  const dialog = page.getByRole('dialog');
  const close = dialog.getByRole('button', { name: 'Close' });

  await row.focus();
  await page.keyboard.press('Enter');
  await expect(dialog).toBeVisible();
  await expect(close).toBeFocused();

  // Tab and Shift+Tab go round the dialog's controls instead of leaving it.
  await page.keyboard.press('Shift+Tab');
  await expect(dialog.getByRole('region', { name: 'Usage by model' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(close).toBeFocused();

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(row).toBeFocused();

  // An Escape pressed the moment the dialog opens closes it too. The dialog
  // must have opened, or its being hidden would prove nothing.
  await noteWhenDialogOpens(page);
  await page.keyboard.press('Enter');
  await page.keyboard.press('Escape');
  await expect(page.locator(':root')).toHaveAttribute('data-dialog-opened');
  await expect(dialog).toBeHidden();
  await expect(row).toBeFocused();
});

test('no console errors', async ({ page, loadlineHome }) => {
  const problems = await watchForProblems(page);
  await signIn(page, loadlineHome);

  // The details render more of the page than the list does.
  await page.getByRole('button', { name: waitingSession }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();
  await page.evaluate(async () => {
    await document.fonts.ready;
  });

  expect(problems).toEqual([]);
});

for (const colorScheme of ['dark', 'light'] as const) {
  test.describe(`${colorScheme} theme`, () => {
    test.use({ colorScheme });

    test('axe finds no serious violations', async ({ dashboard: page }) => {
      await expect(page.locator(':root')).toHaveCSS('color-scheme', colorScheme);
      expect(await seriousViolations(page)).toEqual([]);

      await page.getByRole('button', { name: waitingSession }).click();
      await expect(page.getByRole('dialog')).toBeVisible();
      await animationsFinished(page.getByRole('dialog'));
      expect(await seriousViolations(page)).toEqual([]);
    });
  });
}

test('UI routes reject requests without cookie', async ({ page, loadlineHome }) => {
  // A fresh context holds no cookie; the page itself is public.
  await page.goto('/');
  expect(await statusOf(page, '/api/snapshot')).toBe(401);
  expect(await statusOf(page, '/api/events')).toBe(401);

  await signIn(page, loadlineHome);
  expect(await statusOf(page, '/api/snapshot')).toBe(200);
});

// Marks the page's root element once a dialog appears in it.
async function noteWhenDialogOpens(page: Page): Promise<void> {
  await page.evaluate(() => {
    const root = document.documentElement;
    delete root.dataset['dialogOpened'];
    new MutationObserver((_, observer) => {
      if (document.querySelector('[role="dialog"]') !== null) {
        root.dataset['dialogOpened'] = '';
        observer.disconnect();
      }
    }).observe(document.body, { childList: true, subtree: true });
  });
}

// The dialog fades in, and axe must judge the colours it comes to rest at.
// An endless animation never finishes, and a cancelled one has nothing left
// to wait for.
async function animationsFinished(element: Locator): Promise<void> {
  await element.evaluate(async (node) => {
    const finite = node
      .getAnimations({ subtree: true })
      .filter((animation) => animation.effect?.getComputedTiming().endTime !== Infinity);
    await Promise.allSettled(finite.map(async (animation) => animation.finished));
  });
}

type Violation = {
  readonly rule: string;
  readonly impact: string;
  readonly targets: readonly string[];
};

async function seriousViolations(page: Page): Promise<readonly Violation[]> {
  const { violations } = await new AxeBuilder({ page }).analyze();
  return violations
    .filter(({ impact }) => impact === 'serious' || impact === 'critical')
    .map(({ id, impact, nodes }) => ({
      rule: id,
      impact: impact ?? '',
      targets: nodes.map(({ target }) => target.join(' ')),
    }));
}

// The status of a request the page makes itself, cookies and all.
async function statusOf(page: Page, path: string): Promise<number> {
  return page.evaluate(async (url) => (await fetch(url)).status, path);
}
