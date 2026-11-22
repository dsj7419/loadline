import type { ReadonlySignal } from '@preact/signals';
import { useId } from 'preact/hooks';
import type { SessionView } from '../api/types';
import { sessionTitle, shareOfAccount, timeInState } from '../model/describe';
import { formatUSD } from '../model/format';
import { modelLabel } from '../model/models';
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
 *
 * The button covers the whole row but holds only the session's name and
 * account, and is named by its state, name and account. It is described by
 * its model when that is known; a bare dash is not worth announcing. The
 * figures beside it are plain text: they tick every second, and a button
 * named by them would be announced afresh each time.
 */
export function SessionRow({ session, now, onOpen }: SessionRowProps) {
  const stateId = useId();
  const nameId = useId();
  const accountId = useId();
  const modelId = useId();
  const share = shareOfAccount(session);
  return (
    <li class={`session row is-${session.state}`}>
      <span class="row-state">
        <StateTag state={session.state} labelId={stateId} />
      </span>
      <button
        type="button"
        class="row-name row-open"
        aria-haspopup="dialog"
        aria-labelledby={`${stateId} ${nameId} ${accountId}`}
        aria-describedby={session.model !== '' ? modelId : undefined}
        data-session={session.id}
        onClick={() => onOpen(session.id)}
      >
        <span id={nameId} class="name">
          {sessionTitle(session)}
        </span>{' '}
        <span id={accountId} class="chip chip-account">
          {session.account}
        </span>
      </button>
      <span id={modelId} class="row-model">
        {session.model !== '' ? modelLabel(session.model) : '—'}
        {session.effort !== '' && (
          <>
            {' '}
            <span class="effort">· {session.effort}</span>
          </>
        )}
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
    </li>
  );
}
