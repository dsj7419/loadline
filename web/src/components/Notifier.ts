import { signal, type ReadonlySignal } from '@preact/signals';
import type { AlertKind, AlertView } from '../api/types';

/** The parts of the browser's Notification API the dashboard uses. */
export type NotificationApi = {
  readonly permission: () => NotificationPermission;
  readonly requestPermission: () => Promise<NotificationPermission>;
  readonly show: (title: string, body: string, tag: string) => void;
};

/** The browser's notifications, or null in a browser without them. */
export function browserNotifications(): NotificationApi | null {
  if (typeof Notification === 'undefined') {
    return null;
  }
  return {
    permission: () => Notification.permission,
    requestPermission: () => Notification.requestPermission(),
    show: (title, body, tag) => {
      try {
        const notification = new Notification(title, { body, tag });
        notification.onclick = () => {
          window.focus();
          notification.close();
        };
      } catch {
        // Some browsers only notify from a service worker. Notifications
        // are extra: the banner, title and icon still show the alert.
      }
    },
  };
}

/** Remembers whether the user turned notifications on. */
export type OptInStore = {
  readonly load: () => boolean;
  readonly save: (on: boolean) => void;
};

const optInKey = 'loadline.notifications';

/**
 * Keeps the opt-in in local storage. Storage can be disabled, full or
 * refused outright; then the choice lasts only as long as the page.
 */
export function browserOptIn(storage: () => Storage = () => localStorage): OptInStore {
  return {
    load: () => {
      try {
        return storage().getItem(optInKey) === 'on';
      } catch {
        return false;
      }
    },
    save: (on) => {
      try {
        storage().setItem(optInKey, on ? 'on' : 'off');
      } catch {
        // Without storage the choice is not remembered, which is all.
      }
    },
  };
}

/** Whether the notifier shows notifications: off until the user turns it on. */
export type NotifierState = 'off' | 'on' | 'blocked';

export type Notifier = {
  readonly state: ReadonlySignal<NotifierState>;
  /**
   * Turns notifications on or off. Turning them on asks the browser for
   * permission when it has none yet, so it must only run on the user's click.
   */
  readonly toggle: () => Promise<void>;
  /** Hears each view's alerts, and notifies of those that are new while on. */
  readonly update: (alerts: readonly AlertView[]) => void;
};

// The alerts worth interrupting for: they want something done now.
const notifyingKinds: ReadonlySet<AlertKind> = new Set<AlertKind>(['needs_you', 'runs_out']);

/**
 * Notifies once for each needs-you or runs-out alert that is new since the
 * previous update. An alert that persists is not new; one that clears and
 * comes back is, because it comes back with a new since. The first update is
 * the baseline: alerts already showing when the page loads, or when
 * notifications are turned on, never notify.
 *
 * It starts on only when the user turned it on before and the browser still
 * allows notifications; it never asks the browser by itself.
 */
export function createNotifier(api: NotificationApi, optIn: OptInStore): Notifier {
  const state = signal<NotifierState>(initialState(api.permission(), optIn.load()));
  // Null until the first update, which only records what is showing.
  let known: ReadonlySet<string> | null = null;

  return {
    state,
    toggle: async () => {
      if (state.value === 'on') {
        state.value = 'off';
        optIn.save(false);
        return;
      }
      if (state.value === 'blocked') {
        return;
      }
      const permission = api.permission() === 'granted' ? 'granted' : await askPermission(api);
      state.value = stateFor(permission);
      optIn.save(state.value === 'on');
    },
    update: (alerts) => {
      const current = new Set<string>();
      for (const alert of alerts) {
        if (!notifyingKinds.has(alert.kind)) {
          continue;
        }
        const key = keyOf(alert);
        current.add(key);
        if (state.value === 'on' && known !== null && !known.has(key)) {
          api.show('Loadline', alert.message, key);
        }
      }
      known = current;
    },
  };
}

function initialState(permission: NotificationPermission, optedIn: boolean): NotifierState {
  if (permission === 'denied') {
    return 'blocked';
  }
  return optedIn && permission === 'granted' ? 'on' : 'off';
}

async function askPermission(api: NotificationApi): Promise<NotificationPermission> {
  try {
    return await api.requestPermission();
  } catch {
    // Treated as dismissed: the user can try again.
    return 'default';
  }
}

function stateFor(permission: NotificationPermission): NotifierState {
  switch (permission) {
    case 'granted':
      return 'on';
    case 'denied':
      return 'blocked';
    case 'default':
      return 'off';
  }
}

function keyOf(alert: AlertView): string {
  return [alert.kind, alert.dir, alert.session, alert.since].join('\n');
}
