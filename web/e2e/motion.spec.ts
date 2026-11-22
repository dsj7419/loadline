import type { Page } from '@playwright/test';
import { expect, test } from './fixtures';

// What moves on the page: the details panel slides in, and gauges grow.
async function motion(page: Page): Promise<{ panel: string; gauge: string }> {
  await page.getByRole('button', { name: /copper-lantern/ }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  return {
    panel: await dialog.evaluate((node) => getComputedStyle(node).animationName),
    gauge: await page
      .getByRole('meter')
      .first()
      .locator('span')
      .first()
      .evaluate((node) => getComputedStyle(node).transitionDuration),
  };
}

test.describe('with reduced motion', () => {
  test.use({ reducedMotion: 'reduce' });

  test('nothing animates or transitions', async ({ dashboard: page }) => {
    expect(await motion(page)).toEqual({ panel: 'none', gauge: '0s' });
  });
});

test.describe('without a motion preference', () => {
  test.use({ reducedMotion: 'no-preference' });

  // The control: the motion the reduced setting turns off is there to begin with.
  test('the panel slides in and gauges grow', async ({ dashboard: page }) => {
    expect(await motion(page)).toEqual({ panel: 'sheet-in', gauge: '0.4s' });
  });
});
