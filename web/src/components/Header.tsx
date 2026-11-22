import type { StreamStatus } from '../model/stream';
import { BellIcon, LoadLineMark, WarningIcon } from './Icons';
import type { Notifier } from './Notifier';

/** Where the page's link to the server stands: connecting until the first view. */
export type LinkStatus = 'connecting' | StreamStatus;

const linkText: Readonly<Record<LinkStatus, string>> = {
  connecting: 'Connecting',
  live: 'Live',
  reconnecting: 'Reconnecting',
  signed_out: 'Signed out',
};

type HeaderProps = {
  readonly status: LinkStatus;
  readonly alertCount: number;
  /** Null in a browser without notifications, which then shows no toggle. */
  readonly notifier: Notifier | null;
};

/** The wordmark, whether the page is live, the alert count and the notifications toggle. */
export function Header({ status, alertCount, notifier }: HeaderProps) {
  return (
    <header class="masthead">
      <h1 class="wordmark">
        <LoadLineMark />
        Loadline
      </h1>
      <div class="masthead-side">
        <p class={`link link-${status}`} role="status">
          <span class="link-dot" aria-hidden="true" />
          {linkText[status]}
        </p>
        <p class={alertCount > 0 ? 'alert-count has-alerts' : 'alert-count'} aria-live="polite">
          {alertCount > 0 && <WarningIcon />}
          {alertCountText(alertCount)}
        </p>
        {notifier !== null && <NotifyToggle notifier={notifier} />}
      </div>
    </header>
  );
}

function alertCountText(count: number): string {
  if (count === 0) {
    return 'No alerts';
  }
  return count === 1 ? '1 alert' : `${count} alerts`;
}

// Reads the notifier's state, so it alone re-renders when that changes.
function NotifyToggle({ notifier }: { readonly notifier: Notifier }) {
  const state = notifier.state.value;
  if (state === 'blocked') {
    return (
      <p class="notify notify-blocked">
        <BellIcon off />
        Notifications blocked
      </p>
    );
  }
  const on = state === 'on';
  return (
    <button type="button" class="notify" aria-pressed={on} onClick={() => void notifier.toggle()}>
      <BellIcon off={!on} />
      Notifications
      <span class="notify-state" aria-hidden="true">
        {on ? 'On' : 'Off'}
      </span>
    </button>
  );
}
