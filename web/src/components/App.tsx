import { useSignal, type ReadonlySignal } from '@preact/signals';
import type { Ref } from 'preact';
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'preact/hooks';
import type { AccountView, SessionView, View } from '../api/types';
import { alertTitle } from '../model/alerts';
import { groupSessions } from '../model/group';
import { createStream, type StreamStatus } from '../model/stream';
import { AccountCard } from './AccountCard';
import { AlertBanner } from './AlertBanner';
import { DetailPanel } from './DetailPanel';
import { drawFavicon as drawTabIcon } from './Favicon';
import { Header, type LinkStatus } from './Header';
import { ModelsBar } from './ModelsBar';
import { browserNotifications, browserOptIn, createNotifier, type NotificationApi } from './Notifier';
import { useKeepRowFocus, useReturnFocus } from './rowFocus';
import { SessionGroup } from './SessionGroup';

/** Follows the server's views until the returned function is called. */
export type Connect = (onView: (view: View) => void, onStatus: (status: StreamStatus) => void) => () => void;

// The snapshot needs the same sign-in as the stream, so its status tells a
// signed-out page from a server that is down.
async function snapshotStatus(): Promise<number> {
  const response = await fetch('/api/snapshot', { cache: 'no-store' });
  return response.status;
}

const followServer: Connect = (onView, onStatus) =>
  createStream('/api/events', onView, onStatus, { probe: snapshotStatus });

const tick = 1_000;

type AppProps = {
  readonly connect?: Connect;
  /** The browser's notifications by default; null for none. */
  readonly notifications?: NotificationApi | null;
  readonly drawFavicon?: (alerting: boolean) => void;
};

/**
 * The dashboard. It shows the latest view the server sent and keeps its own
 * clock, ticking each second, so times in state and countdowns move between
 * views: the server publishes only when something visible changes.
 */
export function App({ connect = followServer, notifications, drawFavicon = drawTabIcon }: AppProps) {
  const [view, setView] = useState<View | null>(null);
  const [status, setStatus] = useState<LinkStatus>('connecting');
  const [openId, setOpenId] = useState<string | null>(null);
  const now = useSignal(new Date());
  const notifier = useMemo(() => {
    const api = notifications === undefined ? browserNotifications() : notifications;
    return api === null ? null : createNotifier(api, browserOptIn());
  }, [notifications]);

  useEffect(() => connect(setView, setStatus), [connect]);

  useEffect(() => {
    const timer = setInterval(() => {
      now.value = new Date();
    }, tick);
    return () => clearInterval(timer);
  }, [now]);

  const alertCount = view?.alerts.length ?? 0;
  const alerting = alertCount > 0;
  useEffect(() => {
    document.title = alertTitle(alertCount);
  }, [alertCount]);
  useEffect(() => {
    drawFavicon(alerting);
  }, [alerting, drawFavicon]);
  useEffect(() => {
    if (view !== null) {
      notifier?.update(view.alerts);
    }
  }, [view, notifier]);

  const open = openId === null ? null : (view?.sessions.find(({ id }) => id === openId) ?? null);
  const closeDetail = useCallback(() => setOpenId(null), []);
  const sessionsHeading = useRef<HTMLHeadingElement>(null);
  useReturnFocus(openId, sessionsHeading);
  useKeepRowFocus(sessionsHeading);
  useEffect(() => {
    // A session that leaves the view takes its details with it.
    if (openId !== null && view !== null && open === null) {
      setOpenId(null);
    }
  }, [openId, view, open]);

  return (
    <>
      <div class="page" inert={open !== null}>
        <Header status={status} alertCount={alertCount} notifier={notifier} />
        <main class={status === 'reconnecting' || status === 'signed_out' ? 'main is-stale' : 'main'}>
          {status === 'signed_out' && (
            <p class="signed-out" role="alert">
              Signed out — run <code>loadline open</code> to sign in again.
            </p>
          )}
          {view === null ? (
            status !== 'signed_out' && <p class="waiting">Waiting for the Loadline server…</p>
          ) : (
            <>
              <AlertBanner alerts={view.alerts} />
              <Accounts accounts={view.accounts} now={now} />
              <Sessions sessions={view.sessions} now={now} onOpen={setOpenId} heading={sessionsHeading} />
              <ModelsBar today={view.models} week={view.modelsWeek} />
            </>
          )}
        </main>
      </div>
      {open !== null && <DetailPanel session={open} now={now} onClose={closeDetail} />}
    </>
  );
}

type AccountsProps = {
  readonly accounts: readonly AccountView[];
  readonly now: ReadonlySignal<Date>;
};

function Accounts({ accounts, now }: AccountsProps) {
  const headingId = useId();
  return (
    <section class="accounts" aria-labelledby={headingId}>
      <div class="section-head">
        <h2 id={headingId}>Accounts</h2>
      </div>
      {accounts.length === 0 ? (
        <p class="empty">No accounts yet. They appear once a Claude Code session reports in.</p>
      ) : (
        <div class="account-grid">
          {accounts.map((account) => (
            <AccountCard key={account.dir} account={account} now={now} />
          ))}
        </div>
      )}
    </section>
  );
}

type SessionsProps = {
  readonly sessions: readonly SessionView[];
  readonly now: ReadonlySignal<Date>;
  readonly onOpen: (id: string) => void;
  /** The list's heading, which takes focus when nothing better can. */
  readonly heading: Ref<HTMLHeadingElement>;
};

function Sessions({ sessions, now, onOpen, heading }: SessionsProps) {
  const headingId = useId();
  return (
    <section class="sessions" aria-labelledby={headingId}>
      <div class="section-head">
        <h2 id={headingId} ref={heading} tabindex={-1}>
          Sessions
        </h2>
        <span class="section-aside num">{sessions.length}</span>
      </div>
      {sessions.length === 0 ? (
        <p class="empty">No sessions right now. Start Claude Code and its sessions appear here.</p>
      ) : (
        groupSessions(sessions).map((group) => (
          <SessionGroup key={group.key} group={group} now={now} onOpen={onOpen} />
        ))
      )}
    </section>
  );
}
