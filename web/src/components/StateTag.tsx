import type { JSX } from 'preact';
import type { SessionState } from '../api/types';
import { stateLabels } from '../model/describe';

// Each state's icon differs in shape as well as colour, and always sits beside
// the state's name, so a state never rests on colour alone.
const shapes: Readonly<Record<SessionState, () => JSX.Element>> = {
  needs_you: () => (
    <>
      <circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" stroke-width="1.6" />
      <path d="M6 1.4a4.6 4.6 0 0 0 0 9.2z" fill="currentColor" />
    </>
  ),
  working: () => <circle cx="6" cy="6" r="5" fill="currentColor" />,
  compacting: () => (
    <>
      <circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" stroke-width="1.6" />
      <circle cx="6" cy="6" r="1.9" fill="currentColor" />
    </>
  ),
  idle: () => <circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" stroke-width="1.6" />,
  starting: () => (
    <circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-dasharray="2.4 1.8" />
  ),
  error: () => <path d="M6 1.2 11.2 10.4H.8z" fill="currentColor" />,
  parked: () => (
    <>
      <rect x="2.4" y="1.8" width="2.6" height="8.4" rx=".6" fill="currentColor" />
      <rect x="7" y="1.8" width="2.6" height="8.4" rx=".6" fill="currentColor" />
    </>
  ),
  ended: () => <rect x="2" y="2" width="8" height="8" rx="1.4" fill="none" stroke="currentColor" stroke-width="1.6" />,
};

/** The icon of a session state, for use beside its name. */
export function StateIcon({ state }: { readonly state: SessionState }) {
  return (
    <svg class="state-icon" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
      {shapes[state]()}
    </svg>
  );
}

/** A session state as an icon and its name. */
export function StateTag({ state }: { readonly state: SessionState }) {
  return (
    <span class={`state state-${state}`}>
      <StateIcon state={state} />
      <span class="state-label">{stateLabels[state]}</span>
    </span>
  );
}
