// Small icons drawn in the text colour around them. Each sits beside text
// that says the same thing, so all of them are hidden from screen readers.

/** A warning triangle. */
export function WarningIcon() {
  return (
    <svg class="icon" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
      <path d="M6 1.2 11.2 10.4H.8z" fill="currentColor" />
      <rect x="5.3" y="4.2" width="1.4" height="3.4" rx=".5" class="icon-cutout" />
      <rect x="5.3" y="8.2" width="1.4" height="1.4" rx=".5" class="icon-cutout" />
    </svg>
  );
}

/** A bell, struck through when off. */
export function BellIcon({ off }: { readonly off: boolean }) {
  return (
    <svg class="icon" viewBox="0 0 14 14" width="14" height="14" aria-hidden="true">
      <path
        d="M7 1.6a3.6 3.6 0 0 0-3.6 3.6v2.6L2.2 9.6v.6h9.6v-.6l-1.2-1.8V5.2A3.6 3.6 0 0 0 7 1.6zM5.6 11.2a1.4 1.4 0 0 0 2.8 0z"
        fill="currentColor"
      />
      {off && <path d="M1.6 1.6 12.4 12.4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />}
    </svg>
  );
}

/**
 * The Loadline mark: a ship's load line, the ringed bar painted on a hull to
 * show how deep it may safely sit.
 */
export function LoadLineMark() {
  return (
    <svg class="mark" viewBox="0 0 24 24" width="22" height="22" aria-hidden="true">
      <circle cx="12" cy="12" r="7.25" fill="none" stroke="currentColor" stroke-width="2.25" />
      <rect x="1.5" y="10.9" width="21" height="2.2" rx="1.1" fill="currentColor" />
    </svg>
  );
}
