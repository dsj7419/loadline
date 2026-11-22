import type { Page } from '@playwright/test';
import { viewOf } from '../test/fixtures';
import { expect, test } from './fixtures';
import { startScriptedServer } from './scripted';

type Pixel = readonly [number, number, number];

// The colour of the tab icon at the point where the alert dot sits.
async function dotPixel(page: Page): Promise<Pixel> {
  return page.evaluate(async () => {
    const href = document.querySelector('link[rel="icon"]')?.getAttribute('href') ?? '';
    const image = new Image();
    image.src = href;
    await image.decode();
    const canvas = document.createElement('canvas');
    canvas.width = 64;
    canvas.height = 64;
    const context = canvas.getContext('2d');
    if (context === null) {
      throw new Error('no 2d canvas');
    }
    context.drawImage(image, 0, 0);
    const [r = 0, g = 0, b = 0] = context.getImageData(50, 14, 1, 1).data;
    return [r, g, b] as const;
  });
}

test('the tab icon carries the accent dot while alerts are active', async ({ dashboard: page }) => {
  // The demo always has alerts. The accent is #fb6a1c.
  await expect.poll(async () => dotPixel(page)).toEqual([251, 106, 28]);
});

test('the tab icon has no dot without alerts', async ({ page }) => {
  const server = await startScriptedServer(viewOf());
  try {
    await page.goto(server.url);
    await expect(page.getByRole('region', { name: 'Sessions' })).toBeVisible();
    // The icon's tile is #13161b.
    await expect.poll(async () => dotPixel(page)).toEqual([19, 22, 27]);
  } finally {
    await server.close();
  }
});
