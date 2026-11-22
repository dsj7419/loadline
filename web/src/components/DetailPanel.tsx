import type { ReadonlySignal } from '@preact/signals';
import type { ComponentChildren } from 'preact';
import { useId, useLayoutEffect, useRef } from 'preact/hooks';
import type { SessionView } from '../api/types';
import { sessionTitle, timeInState } from '../model/describe';
import { formatPct, formatRelative, formatTokens, formatUSD, formatWhen } from '../model/format';
import { modelLabel } from '../model/models';
import { StateTag } from './StateTag';

type DetailPanelProps = {
  readonly session: SessionView;
  readonly now: ReadonlySignal<Date>;
  readonly onClose: () => void;
};

// The elements a keyboard can reach. The panel holds no links or fields.
const focusable = 'button:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * Everything known about one session, in a modal dialog. Opening moves focus
 * to its close button; Tab and Shift+Tab stay inside it; Escape, the close
 * button and a click outside close it. Whoever opened it puts focus back.
 */
export function DetailPanel({ session, now, onClose }: DetailPanelProps) {
  const titleId = useId();
  const dialog = useRef<HTMLDivElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);

  useLayoutEffect(() => {
    closeButton.current?.focus();
    const root = document.documentElement;
    root.classList.add('has-dialog');
    return () => root.classList.remove('has-dialog');
  }, []);

  // Keys are handled on the dialog itself, which holds focus from the moment
  // it opens, so none is missed while effects wait for the next paint.
  const onKeyDown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') {
      event.preventDefault();
      onClose();
    } else if (event.key === 'Tab' && dialog.current !== null) {
      keepFocusIn(dialog.current, event);
    }
  };

  const at = now.value;
  return (
    <div class="overlay">
      <div class="backdrop" onClick={onClose} />
      {/* tabindex -1: a click on the panel's text keeps focus in the panel. */}
      <div
        ref={dialog}
        class="sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabindex={-1}
        onKeyDown={onKeyDown}
      >
        <header class="sheet-head">
          <div class="sheet-heading">
            <StateTag state={session.state} />
            <h2 id={titleId} class="sheet-title">
              {sessionTitle(session)}
            </h2>
            <p class="sheet-sub">{timeInState(session, at)}</p>
          </div>
          <button ref={closeButton} type="button" class="close" onClick={onClose}>
            Close <kbd>Esc</kbd>
          </button>
        </header>

        <dl class="facts">
          <Fact term="Account">{session.account}</Fact>
          <Fact term="Model">
            {session.model !== '' ? modelLabel(session.model) : '—'}
            {session.effort !== '' && ` · ${session.effort}`}
          </Fact>
          <Fact term="Context">
            {formatPct(session.contextPct)}
            {session.contextLeft > 0 && ` · ${formatTokens(session.contextLeft)} left`}
          </Fact>
          <Fact term="Cost">{formatUSD(session.costUSD)}</Fact>
          <Fact term="Cache hit ratio">{formatPct(session.cacheHitRatio * 100)}</Fact>
          <Fact term="Lines">
            <span class="added">+{session.linesAdded}</span> <span class="removed">−{session.linesRemoved}</span>
          </Fact>
          <Fact term="Started">
            <time dateTime={session.startedAt}>{formatWhen(session.startedAt, at)}</time>
            <span class="fact-aside"> · {formatRelative(session.startedAt, at)}</span>
          </Fact>
          <Fact term="Last activity">
            <time dateTime={session.lastSeenAt}>{formatWhen(session.lastSeenAt, at)}</time>
            <span class="fact-aside"> · {formatRelative(session.lastSeenAt, at)}</span>
          </Fact>
          <Fact term="Kind">{session.kind === 'background' ? 'Background' : 'Interactive'}</Fact>
          <Fact term="Process">{session.pid > 0 ? String(session.pid) : '—'}</Fact>
          <Fact term="Session ID" wide>
            <code>{session.id}</code>
          </Fact>
          <Fact term="Project" wide>
            <code>{session.project !== '' ? session.project : '—'}</code>
          </Fact>
          {session.replaces !== '' && (
            <Fact term="Continues" wide>
              <code>{session.replaces}</code>
            </Fact>
          )}
        </dl>

        <ModelTable session={session} />
        <Timeline session={session} now={at} />
      </div>
    </div>
  );
}

// Moves focus round to the other end of the dialog when Tab would leave it.
function keepFocusIn(container: HTMLElement, event: KeyboardEvent): void {
  const items = [...container.querySelectorAll<HTMLElement>(focusable)];
  const first = items[0];
  const last = items.at(-1);
  if (first === undefined || last === undefined) {
    return;
  }
  const active = document.activeElement;
  // Focus on the panel itself, after a click on its text, counts as outside
  // the items, so Tab goes to the first and Shift+Tab to the last.
  const outside = !(active instanceof Node) || !container.contains(active) || active === container;
  if (event.shiftKey && (active === first || outside)) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (active === last || outside)) {
    event.preventDefault();
    first.focus();
  }
}

type FactProps = {
  readonly term: string;
  /** A long value, such as a path, takes the whole width. */
  readonly wide?: boolean;
  readonly children: ComponentChildren;
};

function Fact({ term, wide = false, children }: FactProps) {
  return (
    <div class={wide ? 'fact fact-wide' : 'fact'}>
      <dt>{term}</dt>
      <dd>{children}</dd>
    </div>
  );
}

// The panel's sections are plain sections, not landmarks: the dialog is the
// one landmark a screen reader needs here.
function ModelTable({ session }: { readonly session: SessionView }) {
  const headingId = useId();
  return (
    <section class="sheet-section">
      <h3 id={headingId}>Usage by model</h3>
      {session.models.length === 0 ? (
        <p class="empty">No model calls recorded.</p>
      ) : (
        // The table can be wider than a phone, so it scrolls on its own, and
        // a keyboard can reach it to scroll it.
        <div class="table-scroll" role="region" aria-labelledby={headingId} tabindex={0}>
          <table class="usage">
            <thead>
              <tr>
                <th scope="col">Model</th>
                <th scope="col">Input</th>
                <th scope="col">Output</th>
                <th scope="col">Cache read</th>
                <th scope="col">Cache write</th>
                <th scope="col">Cost</th>
                <th scope="col">Requests</th>
              </tr>
            </thead>
            <tbody>
              {session.models.map((m) => (
                <tr key={m.model}>
                  <th scope="row">{modelLabel(m.model)}</th>
                  <td>{formatTokens(m.input)}</td>
                  <td>{formatTokens(m.output)}</td>
                  <td>{formatTokens(m.cacheRead)}</td>
                  <td>{formatTokens(m.cacheWrite)}</td>
                  <td>{formatUSD(m.costUSD)}</td>
                  <td>{m.requests}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Timeline({ session, now }: { readonly session: SessionView; readonly now: Date }) {
  // The server lists changes oldest first; the newest matters most here.
  const changes = [...session.timeline].reverse();
  return (
    <section class="sheet-section">
      <h3>Timeline</h3>
      <ol class="timeline">
        {changes.map((change) => (
          <li key={`${change.at}-${change.state}`} class="change">
            <StateTag state={change.state} />
            <span class="change-when num">{formatRelative(change.at, now)}</span>
            {change.reason !== '' && <span class="change-reason">{change.reason}</span>}
          </li>
        ))}
      </ol>
    </section>
  );
}
