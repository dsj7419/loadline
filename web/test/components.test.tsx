// @vitest-environment jsdom
import { signal } from '@preact/signals';
import { act, cleanup, render, screen, within } from '@testing-library/preact';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { sessionStates, type View } from '../src/api/types';
import { AccountCard } from '../src/components/AccountCard';
import { App } from '../src/components/App';
import { ModelsBar } from '../src/components/ModelsBar';
import { browserOptIn, createNotifier, type NotificationApi, type OptInStore } from '../src/components/Notifier';
import { SessionRow } from '../src/components/SessionRow';
import { stateLabels } from '../src/model/describe';
import type { StreamStatus } from '../src/model/stream';
import { accountOf, alertOf, modelOf, sessionOf, viewOf } from './fixtures';

// A Saturday. Tests run in UTC, so local times read as written.
const now = new Date('2026-11-21T18:00:00Z');

beforeEach(() => {
  // Only the date is frozen, so timers and user events run as usual.
  vi.useFakeTimers({ toFake: ['Date'], now });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  document.title = '';
  localStorage.clear();
});

// Lets Preact render what a plain DOM event changed, without flushing the
// effects it runs after paint, as a real browser would not have yet.
async function rendered(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

function pressKey(key: string, init: KeyboardEventInit = {}): void {
  document.activeElement?.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }));
}

function memoryOptIn(initial = false): { store: OptInStore; saved: boolean[] } {
  let on = initial;
  const saved: boolean[] = [];
  return {
    store: {
      load: () => on,
      save: (value: boolean) => {
        on = value;
        saved.push(value);
      },
    },
    saved,
  };
}

function fakeNotifications(permission: NotificationPermission = 'default', answer: NotificationPermission = 'granted') {
  let current = permission;
  const shown: string[] = [];
  const requestPermission = vi.fn(async () => {
    current = answer;
    return answer;
  });
  const api: NotificationApi = {
    permission: () => current,
    requestPermission,
    show: (_title, body) => {
      shown.push(body);
    },
  };
  return { api, requestPermission, shown };
}

// Renders the page with the stream, notifications and favicon replaced, and
// returns what plays the server.
function renderApp({ notifications = null }: { notifications?: NotificationApi | null } = {}) {
  let onView: (view: View) => void = () => {};
  let onStatus: (status: StreamStatus) => void = () => {};
  const drawFavicon = vi.fn<(alerting: boolean) => void>();
  render(
    <App
      connect={(view, status) => {
        onView = view;
        onStatus = status;
        return () => {};
      }}
      notifications={notifications}
      drawFavicon={drawFavicon}
    />,
  );
  return {
    drawFavicon,
    send: (view: View) => {
      act(() => {
        onView(view);
        onStatus('live');
      });
    },
    lose: () => {
      act(() => onStatus('reconnecting'));
    },
  };
}

function sessionsRegion() {
  return screen.getByRole('region', { name: 'Sessions' });
}

// The row a session's button sits in, which holds the session's figures.
function rowOf(button: HTMLElement): HTMLElement {
  const row = button.closest('li');
  if (row === null) {
    throw new Error('the button is not in a row');
  }
  return row;
}

const waiting = {
  id: 'waiting-1',
  name: 'copper-lantern',
  state: 'needs_you',
  waitReason: 'permission prompt',
  stateAt: '2026-11-21T17:58:48Z',
  effort: 'xhigh',
};

describe('App', () => {
  it('renders a group per non-empty state, most urgent first', () => {
    const { send } = renderApp();
    send(
      viewOf({
        sessions: [
          sessionOf({ id: 'i', name: 'ledger', state: 'idle' }),
          sessionOf({ id: 'c', name: 'tidewater', state: 'compacting' }),
          sessionOf({ ...waiting }),
          sessionOf({ id: 'w', name: 'harbor-api', state: 'working' }),
        ],
      }),
    );

    const headings = within(sessionsRegion()).getAllByRole('heading', { level: 3 });
    expect(headings.map((h) => h.textContent)).toEqual(['Needs you', 'Working', 'Idle']);
  });

  it('exposes the state of every row as text', () => {
    const { send } = renderApp();
    send(viewOf({ sessions: sessionStates.map((state) => ({ ...sessionOf({ id: state, name: `row-${state}`, state }) })) }));

    for (const state of sessionStates) {
      const button = within(sessionsRegion()).getByRole('button', { name: `${stateLabels[state]} row-${state} Main` });
      expect(rowOf(button).textContent).toContain(stateLabels[state]);
    }
  });

  it('opens the detail panel on Enter and closes it on Escape, returning focus to the row', async () => {
    const user = userEvent.setup();
    const { send } = renderApp();
    send(viewOf({ sessions: [sessionOf({ ...waiting })] }));

    const row = screen.getByRole('button', { name: /copper-lantern/ });
    row.focus();
    await user.keyboard('{Enter}');

    const dialog = screen.getByRole('dialog', { name: 'copper-lantern' });
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(dialog.contains(document.activeElement)).toBe(true);

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: /copper-lantern/ }));
  });

  it('keeps focus inside the detail panel while it is open', async () => {
    const user = userEvent.setup();
    const { send } = renderApp();
    send(viewOf({ sessions: [sessionOf({ ...waiting })] }));

    await user.click(screen.getByRole('button', { name: /copper-lantern/ }));
    const dialog = screen.getByRole('dialog', { name: 'copper-lantern' });
    for (let i = 0; i < 5; i++) {
      await user.tab();
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
    for (let i = 0; i < 5; i++) {
      await user.tab({ shift: true });
      expect(dialog.contains(document.activeElement)).toBe(true);
    }

    await user.click(within(dialog).getByRole('button', { name: /close/i }));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('handles Escape and Tab from the moment the panel opens', async () => {
    const { send } = renderApp();
    send(viewOf({ sessions: [sessionOf({ ...waiting, models: [modelOf({ model: 'claude-opus-5-5', costUSD: 1 })] })] }));

    screen.getByRole('button', { name: /copper-lantern/ }).click();
    await rendered();
    const dialog = screen.getByRole('dialog', { name: 'copper-lantern' });
    const close = within(dialog).getByRole('button', { name: /close/i });
    expect(document.activeElement).toBe(close);

    pressKey('Tab', { shiftKey: true });
    expect(document.activeElement).not.toBe(close);
    expect(dialog.contains(document.activeElement)).toBe(true);

    pressKey('Escape');
    await rendered();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('gives each row a name that holds still while its figures tick', () => {
    vi.useFakeTimers({ now });
    const { send } = renderApp();
    send(viewOf({ sessions: [sessionOf({ ...waiting, account: 'Main' })] }));
    const button = screen.getByRole('button', { name: 'Needs you copper-lantern Main' });

    act(() => {
      vi.advanceTimersByTime(3_000);
    });
    expect(rowOf(button).textContent).toContain('waiting 1m 15s');
    expect(screen.getByRole('button', { name: 'Needs you copper-lantern Main' })).toBe(button);
  });

  it('moves focus to the Sessions heading when the open session leaves the view', async () => {
    const user = userEvent.setup();
    const { send } = renderApp();
    const other = { id: 'other', name: 'harbor-api' };
    send(viewOf({ sessions: [sessionOf({ ...waiting }), sessionOf(other)] }));
    await user.click(screen.getByRole('button', { name: /copper-lantern/ }));
    expect(screen.getByRole('dialog')).toBeTruthy();

    send(viewOf({ sessions: [sessionOf(other)] }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Sessions' }));
  });

  it('shows model names and when the session started in the detail panel', async () => {
    const user = userEvent.setup();
    const { send } = renderApp();
    send(
      viewOf({
        sessions: [
          sessionOf({
            ...waiting,
            startedAt: '2026-11-21T17:14:00Z',
            models: [modelOf({ model: 'claude-opus-5-5-20261001', costUSD: 2 })],
          }),
        ],
      }),
    );
    await user.click(screen.getByRole('button', { name: /copper-lantern/ }));

    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByRole('rowheader', { name: 'Opus 5.5' })).toBeTruthy();
    expect(dialog.textContent).toContain('17:14');
    expect(dialog.textContent).toContain('46m ago');
  });

  it('puts the alert count in the title and the favicon', () => {
    const { send, drawFavicon } = renderApp();
    send(
      viewOf({
        alerts: [
          alertOf(),
          alertOf({ kind: 'runs_out', session: '', message: 'Main is on pace to run out of its weekly limit before it resets' }),
        ],
      }),
    );
    expect(document.title).toBe('(2) Loadline');
    expect(drawFavicon).toHaveBeenLastCalledWith(true);

    send(viewOf());
    expect(document.title).toBe('Loadline');
    expect(drawFavicon).toHaveBeenLastCalledWith(false);
  });

  it('lists the alerts in the order the server gives', () => {
    const { send } = renderApp();
    send(
      viewOf({
        alerts: [
          alertOf({ message: 'copper-lantern needs you: permission prompt' }),
          alertOf({ kind: 'weekly_threshold', session: '', message: 'Main is at 93% of its weekly limit' }),
        ],
      }),
    );

    const items = within(screen.getByRole('region', { name: /alerts/i })).getAllByRole('listitem');
    expect(items.map((item) => item.textContent)).toEqual([
      expect.stringContaining('copper-lantern needs you: permission prompt'),
      expect.stringContaining('Main is at 93% of its weekly limit'),
    ]);
  });

  it('says whether it is live', () => {
    const { send, lose } = renderApp();
    expect(screen.getByRole('status').textContent).toMatch(/connecting/i);
    send(viewOf());
    expect(screen.getByRole('status').textContent).toMatch(/live/i);
    lose();
    expect(screen.getByRole('status').textContent).toMatch(/reconnecting/i);
  });

  it('counts the time in state up each second without a new view', () => {
    vi.useFakeTimers({ now });
    const { send } = renderApp();
    send(viewOf({ sessions: [sessionOf({ ...waiting })] }));
    const row = rowOf(screen.getByRole('button', { name: /copper-lantern/ }));
    expect(row.textContent).toContain('waiting 1m 12s (permission prompt)');

    act(() => {
      vi.advanceTimersByTime(3_000);
    });
    expect(row.textContent).toContain('waiting 1m 15s (permission prompt)');
  });

  it('shows names, paths and messages as text, never as markup', () => {
    const hostile = '<img src=x onerror="document.title=1">';
    const { send } = renderApp();
    send(
      viewOf({
        sessions: [sessionOf({ name: hostile, project: hostile, model: hostile })],
        alerts: [alertOf({ message: hostile })],
        accounts: [accountOf({ label: hostile, email: hostile, plan: hostile })],
      }),
    );

    expect(screen.getAllByText(hostile, { exact: false }).length).toBeGreaterThan(0);
    expect(document.querySelector('img')).toBeNull();
  });

  it('does not ask for notification permission until the toggle is clicked', async () => {
    const user = userEvent.setup();
    const notifications = fakeNotifications();
    const { send } = renderApp({ notifications: notifications.api });
    send(viewOf({ alerts: [alertOf()] }));
    expect(notifications.requestPermission).not.toHaveBeenCalled();

    const toggle = screen.getByRole('button', { name: /notifications/i });
    expect(toggle.getAttribute('aria-pressed')).toBe('false');
    await user.click(toggle);

    expect(notifications.requestPermission).toHaveBeenCalledTimes(1);
    expect(toggle.getAttribute('aria-pressed')).toBe('true');
  });

  it('hides the notifications toggle where the browser has none', () => {
    renderApp({ notifications: null });
    expect(screen.queryByRole('button', { name: /notifications/i })).toBeNull();
  });
});

describe('SessionRow', () => {
  function renderRow(fields: Parameters<typeof sessionOf>[0]) {
    const onOpen = vi.fn();
    render(
      <ul>
        <SessionRow session={sessionOf(fields)} now={signal(now)} onOpen={onOpen} />
      </ul>,
    );
    return { row: screen.getByRole('listitem'), button: screen.getByRole('button'), onOpen };
  }

  it('shows the figures of a session', () => {
    const { row } = renderRow({
      ...waiting,
      account: 'Main',
      model: 'Opus 5.5',
      contextPct: 31,
      contextLeft: 138_000,
      share: 0.41,
      costUSD: 9.3,
      subagents: 2,
    });

    for (const text of [
      'Needs you',
      'copper-lantern',
      'Main',
      'Opus 5.5',
      'xhigh',
      '31%',
      '138k left',
      "~41% of Main's last hour",
      '$9.30',
      'waiting 1m 12s (permission prompt)',
      '2 subagents',
    ]) {
      expect(row.textContent).toContain(text);
    }
  });

  it('describes the row by its model', () => {
    renderRow({ model: 'Opus 5.5', effort: 'xhigh' });
    expect(screen.getByRole('button', { description: 'Opus 5.5 · xhigh' })).toBeTruthy();
  });

  it('gives no description when the model is unknown', () => {
    renderRow({ model: '', effort: '' });
    expect(screen.getByRole('button', { description: '' })).toBeTruthy();
  });

  it('leaves out subagents and share when there are none', () => {
    const { row } = renderRow({ subagents: 0, share: 0 });
    expect(row.textContent).not.toMatch(/subagent/);
    expect(row.textContent).not.toMatch(/last hour/);
  });

  it('marks a session that was lost', () => {
    const { row } = renderRow({ state: 'ended', lost: true });
    expect(row.textContent).toContain('lost');
  });

  it('opens on Space as well as on click', async () => {
    const user = userEvent.setup();
    const { button, onOpen } = renderRow({});
    button.focus();
    await user.keyboard(' ');
    await user.click(button);
    expect(onOpen).toHaveBeenCalledTimes(2);
  });
});

describe('AccountCard', () => {
  const week = { usedPct: 93, resetsAt: '2026-11-23T09:00:00Z', observedAt: '2026-11-21T17:59:00Z', projection: null };
  const fiveHour = { usedPct: 38, resetsAt: '2026-11-21T20:10:00Z', observedAt: '2026-11-21T17:59:00Z', projection: null };

  it('hides the 5-hour gauge when the account has no 5-hour window', () => {
    render(<AccountCard account={accountOf({ fiveHour: null, sevenDay: week })} now={signal(now)} />);
    expect(screen.getByRole('meter', { name: /weekly/i }).getAttribute('aria-valuenow')).toBe('93');
    expect(screen.queryByRole('meter', { name: /5-hour/i })).toBeNull();
  });

  it('shows a gauge for each window the account has', () => {
    render(<AccountCard account={accountOf({ fiveHour, sevenDay: week })} now={signal(now)} />);
    expect(screen.getByRole('meter', { name: /weekly/i })).toBeTruthy();
    expect(screen.getByRole('meter', { name: /5-hour/i }).getAttribute('aria-valuenow')).toBe('38');
  });

  it('hides a window once its reset time has passed', () => {
    const later = signal(new Date('2026-11-21T20:10:00Z'));
    render(<AccountCard account={accountOf({ fiveHour, sevenDay: week })} now={later} />);
    expect(screen.queryByRole('meter', { name: /5-hour/i })).toBeNull();
    expect(screen.getByRole('meter', { name: /weekly/i })).toBeTruthy();
  });

  it('shows where each window is heading', () => {
    const runningOut = {
      ...week,
      projection: { ratePerHour: 2.13, atReset: 100, eta: '2026-11-22T14:00:00Z', runsOutBeforeReset: true },
    };
    render(<AccountCard account={accountOf({ sevenDay: runningOut })} now={signal(now)} />);
    expect(screen.getByText('+2.1%/h · runs out Sun 14:00 · resets Mon 09:00')).toBeTruthy();
  });

  it("shows an API account's spend against its daily budget, as an estimate", () => {
    render(
      <AccountCard
        account={accountOf({
          label: 'API',
          auth: 'api_key',
          plan: '',
          email: '',
          spendTodayUSD: 8.6,
          spendWeekUSD: 47.6,
          spendMonthUSD: 63.2,
          budgetUSD: 25,
        })}
        now={signal(now)}
      />,
    );

    const today = screen.getByRole('meter', { name: /today/i });
    expect(today.getAttribute('aria-valuetext')).toBe('$8.60 of $25.00');
    const card = screen.getByRole('article', { name: 'API' });
    for (const text of ['$47.60', '$63.20', 'estimated at list price']) {
      expect(card.textContent).toContain(text);
    }
  });

  it("shows the account's model split for today", () => {
    render(
      <AccountCard
        account={accountOf({
          sevenDay: week,
          models: [modelOf({ model: 'claude-opus-5-5', costUSD: 3 }), modelOf({ model: 'claude-haiku-5-5', costUSD: 1 })],
        })}
        now={signal(now)}
      />,
    );
    const card = screen.getByRole('article', { name: 'Main' });
    expect(card.textContent).toContain('Opus 5.5');
    expect(card.textContent).toContain('75%');
    expect(card.textContent).not.toContain('claude-');
  });

  it('says the limits reset once every window has passed its reset time', () => {
    render(<AccountCard account={accountOf({ fiveHour, sevenDay: null })} now={signal(new Date('2026-11-21T20:10:00Z'))} />);
    expect(screen.queryByRole('meter')).toBeNull();
    expect(screen.getByText('Limits reset; waiting for new figures')).toBeTruthy();
  });

  it('says so when no limits have been reported', () => {
    render(<AccountCard account={accountOf({ fiveHour: null, sevenDay: null })} now={signal(now)} />);
    expect(screen.getByText('No rate limits reported yet.')).toBeTruthy();
  });
});

describe('ModelsBar', () => {
  it('switches between today and the last 7 days', async () => {
    const user = userEvent.setup();
    render(
      <ModelsBar
        today={[modelOf({ model: 'Opus 5.5', costUSD: 3 }), modelOf({ model: 'Haiku 5.5', costUSD: 1 })]}
        week={[modelOf({ model: 'Sonnet 5.5', costUSD: 6 }), modelOf({ model: 'Opus 5.5', costUSD: 3 })]}
      />,
    );
    const today = screen.getByRole('button', { name: 'Today' });
    const week = screen.getByRole('button', { name: '7 days' });
    const legend = () => screen.getAllByRole('listitem').map((item) => item.textContent);

    expect(today.getAttribute('aria-pressed')).toBe('true');
    expect(week.getAttribute('aria-pressed')).toBe('false');
    expect(legend()).toEqual([expect.stringMatching(/Opus 5\.5.*75%/), expect.stringMatching(/Haiku 5\.5.*25%/)]);

    await user.click(week);
    expect(today.getAttribute('aria-pressed')).toBe('false');
    expect(week.getAttribute('aria-pressed')).toBe('true');
    expect(legend()).toEqual([expect.stringMatching(/Sonnet 5\.5.*67%/), expect.stringMatching(/Opus 5\.5.*33%/)]);
  });

  it('names models from their IDs', () => {
    render(
      <ModelsBar
        today={[modelOf({ model: 'claude-opus-5-5', costUSD: 3 }), modelOf({ model: 'claude-haiku-5-5-20261001', costUSD: 1 })]}
        week={[]}
      />,
    );
    const legend = screen.getAllByRole('listitem').map((item) => item.textContent);
    expect(legend).toEqual([expect.stringMatching(/^Opus 5\.5 75%/), expect.stringMatching(/^Haiku 5\.5 25%/)]);
  });

  it('says when there is no usage yet', () => {
    render(<ModelsBar today={[]} week={[]} />);
    expect(screen.getByText(/no model usage today/i)).toBeTruthy();
  });
});

describe('createNotifier', () => {
  const needsYou = alertOf();
  const runsOut = alertOf({ kind: 'runs_out', session: '', message: 'Main is on pace to run out' });
  const weekly = alertOf({ kind: 'weekly_threshold', session: '', message: 'Main is at 93%' });

  it('notifies once for each new needs-you or runs-out alert', async () => {
    const notifications = fakeNotifications('granted');
    const notifier = createNotifier(notifications.api, memoryOptIn().store);
    await notifier.toggle();
    expect(notifier.state.value).toBe('on');

    // The page's first view, with nothing showing yet.
    notifier.update([]);
    notifier.update([needsYou, weekly]);
    notifier.update([needsYou, weekly]);
    notifier.update([needsYou, runsOut, weekly]);
    notifier.update([]);
    notifier.update([alertOf({ since: '2026-11-21T18:05:00Z' })]);
    // Answered and asked again between two views: a new wait, so a new alert.
    notifier.update([alertOf({ since: '2026-11-21T18:09:00Z' })]);

    expect(notifications.shown).toEqual([needsYou.message, runsOut.message, needsYou.message, needsYou.message]);
    expect(notifications.requestPermission).not.toHaveBeenCalled();
  });

  it('stays quiet while off, and about alerts it already knew of when turned on', async () => {
    const notifications = fakeNotifications('granted');
    const notifier = createNotifier(notifications.api, memoryOptIn().store);
    notifier.update([needsYou]);
    await notifier.toggle();
    notifier.update([needsYou]);
    expect(notifications.shown).toEqual([]);

    await notifier.toggle();
    expect(notifier.state.value).toBe('off');
    notifier.update([needsYou, runsOut]);
    expect(notifications.shown).toEqual([]);
  });

  it('reports blocked when permission is refused', async () => {
    const notifications = fakeNotifications('default', 'denied');
    const notifier = createNotifier(notifications.api, memoryOptIn().store);
    await notifier.toggle();
    expect(notifier.state.value).toBe('blocked');
    expect(createNotifier(fakeNotifications('denied').api, memoryOptIn(true).store).state.value).toBe('blocked');
  });

  it('never notifies of the alerts in the first view, even when turned on before it', async () => {
    const notifications = fakeNotifications('granted');
    const notifier = createNotifier(notifications.api, memoryOptIn().store);
    await notifier.toggle();

    notifier.update([needsYou, runsOut]);
    expect(notifications.shown).toEqual([]);
  });

  it('stays quiet about the alerts showing when it comes back on, and notifies of later ones', () => {
    const notifications = fakeNotifications('granted');
    const notifier = createNotifier(notifications.api, memoryOptIn(true).store);
    expect(notifier.state.value).toBe('on');

    notifier.update([needsYou]);
    expect(notifications.shown).toEqual([]);
    notifier.update([needsYou, runsOut]);
    expect(notifications.shown).toEqual([runsOut.message]);
  });

  it('comes back on when the user turned it on before and the browser still allows it', () => {
    const notifications = fakeNotifications('granted');
    const notifier = createNotifier(notifications.api, memoryOptIn(true).store);
    expect(notifier.state.value).toBe('on');
    expect(notifications.requestPermission).not.toHaveBeenCalled();
  });

  it('stays off after an earlier opt-in when the browser no longer allows it', () => {
    const notifications = fakeNotifications('default');
    expect(createNotifier(notifications.api, memoryOptIn(true).store).state.value).toBe('off');
    expect(notifications.requestPermission).not.toHaveBeenCalled();
  });

  it('remembers each choice', async () => {
    const { store, saved } = memoryOptIn();
    const notifier = createNotifier(fakeNotifications('granted').api, store);
    await notifier.toggle();
    await notifier.toggle();
    expect(saved).toEqual([true, false]);
  });
});

describe('browserOptIn', () => {
  it('keeps the choice in local storage', () => {
    browserOptIn(() => localStorage).save(true);
    expect(browserOptIn(() => localStorage).load()).toBe(true);
    browserOptIn(() => localStorage).save(false);
    expect(browserOptIn(() => localStorage).load()).toBe(false);
  });

  it('works where storage is refused', () => {
    const refused = browserOptIn(() => {
      throw new DOMException('storage is disabled', 'SecurityError');
    });
    expect(refused.load()).toBe(false);
    expect(() => refused.save(true)).not.toThrow();
  });

  it('works where storage is full', () => {
    const full: Storage = {
      length: 0,
      clear: () => {},
      key: () => null,
      getItem: () => null,
      removeItem: () => {},
      setItem: () => {
        throw new DOMException('quota exceeded', 'QuotaExceededError');
      },
    };
    expect(() => browserOptIn(() => full).save(true)).not.toThrow();
  });
});
