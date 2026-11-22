import { expect, test, type Page } from '@playwright/test';
import type { SessionView, View } from '../src/api/types';
import { sessionOf, viewOf } from '../test/fixtures';
import { startScriptedServer, type ScriptedServer } from './scripted';

// Three sessions, started an hour apart: the page lists the newest first.
const startHours: Readonly<Record<string, number>> = { s1: 10, s2: 11, s3: 12 };

function session(id: string, fields: Record<string, unknown> = {}): SessionView {
  const hour = String(startHours[id] ?? 9).padStart(2, '0');
  return sessionOf({ id, name: id, state: 'working', startedAt: `2026-11-21T${hour}:00:00Z`, ...fields });
}

// The server lists sessions in its own order, which moves with every status
// line, and a test lists them in whatever order it likes.
function viewWith(...sessions: SessionView[]): View {
  return viewOf({ sessions });
}

let server: ScriptedServer;

test.beforeEach(async () => {
  server = await startScriptedServer(viewWith(session('s1'), session('s2'), session('s3')));
});

test.afterEach(async () => {
  await server.close();
});

// Counts, in the page, every moment the document's focus rests on the body
// while the session list changes.
async function countFocusDrops(page: Page): Promise<void> {
  await page.evaluate(() => {
    const counts = { drops: 0 };
    Object.assign(window, { focusDrops: counts });
    const list = document.querySelector('main');
    if (list === null) {
      throw new Error('no main element');
    }
    new MutationObserver(() => {
      if (document.activeElement === document.body) {
        counts.drops += 1;
      }
    }).observe(list, { childList: true, subtree: true, characterData: true });
  });
}

async function focusDrops(page: Page): Promise<number> {
  return page.evaluate(() => {
    const counts: unknown = Reflect.get(window, 'focusDrops');
    return typeof counts === 'object' && counts !== null && 'drops' in counts && typeof counts.drops === 'number' ? counts.drops : -1;
  });
}

test('a focused row keeps focus, and rows keep their order, as views arrive', async ({ page }) => {
  await page.goto(server.url);
  const row = (id: string) => page.locator(`button[data-session="${id}"]`);
  const shown = (id: string) => page.locator('li', { has: row(id) });
  const order = async () =>
    page.locator('button[data-session]').evaluateAll((buttons) => buttons.map((b) => b.getAttribute('data-session')));

  await expect(row('s1')).toBeVisible();
  expect(await order()).toEqual(['s3', 's2', 's1']);
  await row('s2').focus();
  await countFocusDrops(page);

  // Status lines only: the server reorders the sessions and their figures move.
  const reorders = [
    ['s2', 's3', 's1'],
    ['s3', 's1', 's2'],
    ['s1', 's2', 's3'],
  ];
  for (const [i, ids] of reorders.entries()) {
    const cost = 1 + (i + 1) / 100;
    server.send(viewWith(...ids.map((id) => session(id, { costUSD: cost }))));
    await expect(shown('s1')).toContainText(`$${cost.toFixed(2)}`);
    await expect(row('s2')).toBeFocused();
    expect(await order()).toEqual(['s3', 's2', 's1']);
  }

  // Another session moves to another group.
  server.send(viewWith(session('s1'), session('s2'), session('s3', { state: 'idle' })));
  await expect(shown('s3')).toContainText('Idle');
  await expect(row('s2')).toBeFocused();

  // The focused session moves to another group, and back.
  server.send(viewWith(session('s1'), session('s2', { state: 'needs_you' }), session('s3')));
  await expect(shown('s2')).toContainText('Needs you');
  await expect(row('s2')).toBeFocused();
  server.send(viewWith(session('s3'), session('s2'), session('s1')));
  await expect(shown('s2')).toContainText('Working');
  await expect(row('s2')).toBeFocused();

  expect(await focusDrops(page)).toBe(0);
});
