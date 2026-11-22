import type { ReadonlySignal } from '@preact/signals';
import type { SessionView } from '../api/types';
import { sessionTitle, shareOfAccount, timeInState } from '../model/describe';
import { formatUSD } from '../model/format';
import { ContextBar } from './ContextBar';
import { StateTag } from './StateTag';

type SessionRowProps = {
  readonly session: SessionView;
  readonly now: ReadonlySignal<Date>;
  /** Opens the session's details. */
  readonly onOpen: (id: string) => void;
};

/**
 * One session as a row that opens its details, by click, Enter or Space. The
 * row reads the page's clock, so its time in state counts up between views.
 */
export function SessionRow({ session, now, onOpen }: SessionRowProps) {
  const share = shareOfAccount(session);
  return (
    <li class="session">
      <button
        type="button"
        class={`row is-${session.state}`}
        aria-haspopup="dialog"
        data-session={session.id}
        onClick={() => onOpen(session.id)}
      >
        <span class="row-state">
          <StateTag state={session.state} />
        </span>
        <span class="row-name">
          <span class="name">{sessionTitle(session)}</span>
          <span class="chip chip-account">{session.account}</span>
        </span>
        <span class="row-model">
          {session.model !== '' ? session.model : '—'}
          {session.effort !== '' && <span class="effort"> · {session.effort}</span>}
        </span>
        <span class="row-context">
          <ContextBar pct={session.contextPct} left={session.contextLeft} />
        </span>
        <span class="row-cost num">{formatUSD(session.costUSD)}</span>
        <span class="row-meta">
          <span class="meta-item meta-time">{timeInState(session, now.value)}</span>
          {share !== null && <span class="meta-item">{share}</span>}
          {session.subagents > 0 && (
            <span class="meta-item">
              {session.subagents} {session.subagents === 1 ? 'subagent' : 'subagents'}
            </span>
          )}
          {session.kind === 'background' && <span class="meta-item">background</span>}
          {session.lost && <span class="meta-item meta-lost">lost</span>}
        </span>
      </button>
    </li>
  );
}
