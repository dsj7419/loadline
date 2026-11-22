import type { RefObject } from 'preact';
import { useLayoutEffect, useRef } from 'preact/hooks';

// Where keyboard focus goes as session rows come and go. A row is found by
// its session's id each time, because a row that changes group is a new
// element in another list.

function rowOf(id: string): HTMLElement | undefined {
  const rows = document.querySelectorAll<HTMLElement>('[data-session]');
  return [...rows].find((row) => row.dataset['session'] === id);
}

// The session whose row holds focus, if one does.
function focusedSession(): string | null {
  const active = document.activeElement;
  if (!(active instanceof HTMLElement)) {
    return null;
  }
  return active.closest<HTMLElement>('[data-session]')?.dataset['session'] ?? null;
}

function focusFell(): boolean {
  return document.activeElement === null || document.activeElement === document.body;
}

/**
 * Keeps focus on a session's row across renders. A browser drops focus to
 * the body when the focused element moves or is replaced, which a row does
 * when its session changes group. Call it in the component that renders the
 * rows: it notes the focused row as the render begins, and if focus fell to
 * the body by the time the render is drawn, puts it back on that session's
 * row, or on fallback when the session has gone, without scrolling. Focus the
 * user moved off the rows stays where it is.
 */
export function useKeepRowFocus(fallback: RefObject<HTMLElement>): void {
  const before = focusedSession();
  useLayoutEffect(() => {
    if (before === null || !focusFell()) {
      return;
    }
    (rowOf(before) ?? fallback.current)?.focus({ preventScroll: true });
  });
}

/**
 * Once the details close, puts focus back on the row they were opened from,
 * or on fallback when the session has left the view.
 */
export function useReturnFocus(openId: string | null, fallback: RefObject<HTMLElement>): void {
  const opened = useRef<string | null>(null);
  useLayoutEffect(() => {
    if (openId !== null) {
      opened.current = openId;
      return;
    }
    const id = opened.current;
    opened.current = null;
    if (id === null) {
      return;
    }
    (rowOf(id) ?? fallback.current)?.focus();
  }, [openId]);
}
