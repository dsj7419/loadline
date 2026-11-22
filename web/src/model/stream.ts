import { isView } from '../api/guard';
import type { View } from '../api/types';

/**
 * Where the page's link to the server stands: live once a view arrives,
 * reconnecting while the connection fails, and signed out when the server
 * is up but refuses the page.
 */
export type StreamStatus = 'live' | 'reconnecting' | 'signed_out';

/** What createStream works through, replaceable in tests. */
export type StreamSeams = {
  /** The browser's EventSource by default. */
  readonly eventSource?: typeof EventSource;
  /**
   * Gives the HTTP status of a request that needs the page's sign-in, so a
   * failing stream can tell a signed-out page (401) from a server that is
   * down. Without it, a failure always reads as reconnecting.
   */
  readonly probe?: () => Promise<number>;
};

// The waits before the first reconnects in a row. Later ones wait the most.
const firstRetryDelaysMs = [1_000, 2_000, 5_000];
const maxRetryDelayMs = 10_000;

const unauthorized = 401;

/**
 * Follows the server's stream of views at url and passes each valid one to
 * onView; a payload that is not a View is dropped. When the connection
 * fails, it closes it and reconnects after 1 s, 2 s, 5 s and then every
 * 10 s, starting over once a valid view arrives. onStatus hears each change:
 * live once a valid view arrives (an open connection alone proves nothing),
 * reconnecting when the connection fails. An EventSource cannot see why a
 * connection failed, so the first failure of each run asks the probe; a 401
 * reports signed out, which lasts until a view arrives. The returned
 * function stops it.
 */
export function createStream(
  url: string,
  onView: (view: View) => void,
  onStatus: (status: StreamStatus) => void,
  { eventSource = EventSource, probe }: StreamSeams = {},
): () => void {
  let source: EventSource | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  let failures = 0;
  let status: StreamStatus | null = null;
  let stopped = false;

  const report = (next: StreamStatus): void => {
    if (next !== status) {
      status = next;
      onStatus(next);
    }
  };

  // A view may arrive, or the stream stop, before the answer does; then the
  // answer is out of date.
  const checkSignIn = async (ask: () => Promise<number>): Promise<void> => {
    let answer: number;
    try {
      answer = await ask();
    } catch {
      // Unreachable: the server is down, which reconnecting already says.
      return;
    }
    if (answer === unauthorized && !stopped && status === 'reconnecting') {
      report('signed_out');
    }
  };

  const connect = (): void => {
    retry = null;
    const current = new eventSource(url);
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
      const firstOfRun = status !== 'reconnecting' && status !== 'signed_out';
      if (status !== 'signed_out') {
        report('reconnecting');
      }
      if (firstOfRun && probe !== undefined) {
        void checkSignIn(probe);
      }
      retry = setTimeout(connect, firstRetryDelaysMs[failures] ?? maxRetryDelayMs);
      failures += 1;
    });
  };

  connect();

  return () => {
    stopped = true;
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
