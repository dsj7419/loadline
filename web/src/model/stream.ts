import { isView } from '../api/guard';
import type { View } from '../api/types';

export type StreamStatus = 'live' | 'reconnecting';

// The waits before the first reconnects in a row. Later ones wait the most.
const firstRetryDelaysMs = [1_000, 2_000, 5_000];
const maxRetryDelayMs = 10_000;

/**
 * Follows the server's stream of views at url and passes each valid one to
 * onView; a payload that is not a View is dropped. When the connection
 * fails, it closes it and reconnects after 1 s, 2 s, 5 s and then every
 * 10 s, starting over once a valid view arrives. onStatus hears each change:
 * live once a valid view arrives (an open connection alone proves nothing),
 * reconnecting when the connection fails. The returned function stops it.
 */
export function createStream(
  url: string,
  onView: (view: View) => void,
  onStatus: (status: StreamStatus) => void,
  es: typeof EventSource = EventSource,
): () => void {
  let source: EventSource | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  let failures = 0;
  let status: StreamStatus | null = null;

  const report = (next: StreamStatus): void => {
    if (next !== status) {
      status = next;
      onStatus(next);
    }
  };

  const connect = (): void => {
    retry = null;
    const current = new es(url);
    source = current;

    current.addEventListener('view', (event) => {
      if (current !== source) {
        return;
      }
      const view = parseView(event.data);
      if (view === null) {
        return;
      }
      failures = 0;
      onView(view);
      report('live');
    });

    current.addEventListener('error', () => {
      if (current !== source) {
        return;
      }
      // Left open, the browser would retry on its own schedule alongside ours.
      current.close();
      source = null;
      report('reconnecting');
      retry = setTimeout(connect, firstRetryDelaysMs[failures] ?? maxRetryDelayMs);
      failures += 1;
    });
  };

  connect();

  return () => {
    if (retry !== null) {
      clearTimeout(retry);
      retry = null;
    }
    source?.close();
    source = null;
  };
}

function parseView(data: unknown): View | null {
  if (typeof data !== 'string') {
    return null;
  }
  let payload: unknown;
  try {
    payload = JSON.parse(data);
  } catch {
    // Malformed JSON is dropped like any other payload that is not a View.
    return null;
  }
  return isView(payload) ? payload : null;
}
