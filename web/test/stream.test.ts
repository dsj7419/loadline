import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { View } from '../src/api/types';
import { createStream, type StreamStatus } from '../src/model/stream';
import { sessionOf, viewOf } from './fixtures';

const url = '/api/events';

type Listener = ((this: EventSource, event: MessageEvent) => unknown) | EventListenerObject;

// Stands in for the browser's EventSource. Each test plays the server and
// the network through open, send and fail. Every event it fires is a
// MessageEvent, which serves listeners for plain events too.
function fakeEventSources() {
  const opened: FakeEventSource[] = [];

  class FakeEventSource implements EventSource {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSED = 2;
    readonly url: string;
    readonly withCredentials = false;
    readyState: number = FakeEventSource.CONNECTING;
    onerror = null;
    onmessage = null;
    onopen = null;
    readonly #listeners = new Map<string, readonly Listener[]>();

    constructor(url: string | URL) {
      this.url = String(url);
      opened.push(this);
    }

    readonly addEventListener: EventSource['addEventListener'] = (type: string, listener: Listener | null) => {
      if (listener !== null) {
        this.#listeners.set(type, [...this.#listenersFor(type), listener]);
      }
    };

    readonly removeEventListener: EventSource['removeEventListener'] = (type: string, listener: Listener | null) => {
      this.#listeners.set(type, this.#listenersFor(type).filter((added) => added !== listener));
    };

    dispatchEvent(event: Event): boolean {
      const message = event instanceof MessageEvent ? event : new MessageEvent(event.type);
      for (const listener of this.#listenersFor(message.type)) {
        if (typeof listener === 'function') {
          listener.call(this, message);
        } else {
          listener.handleEvent(message);
        }
      }
      return true;
    }

    close(): void {
      this.readyState = FakeEventSource.CLOSED;
    }

    open(): void {
      this.readyState = FakeEventSource.OPEN;
      this.dispatchEvent(new MessageEvent('open'));
    }

    // Delivers an `event: view` message.
    send(data: unknown): void {
      this.dispatchEvent(new MessageEvent('view', { data }));
    }

    // Drops the connection, which the browser would then retry by itself.
    fail(): void {
      this.readyState = FakeEventSource.CONNECTING;
      this.dispatchEvent(new MessageEvent('error'));
    }

    #listenersFor(type: string): readonly Listener[] {
      return this.#listeners.get(type) ?? [];
    }
  }

  return { EventSource: FakeEventSource, opened };
}

function startStream(probe?: () => Promise<number>) {
  const sources = fakeEventSources();
  const views: View[] = [];
  const statuses: StreamStatus[] = [];
  const close = createStream(
    url,
    (view) => views.push(view),
    (status) => statuses.push(status),
    probe === undefined ? { eventSource: sources.EventSource } : { eventSource: sources.EventSource, probe },
  );
  const latest = () => {
    const source = sources.opened.at(-1);
    if (source === undefined) {
      throw new Error('the stream opened no connection');
    }
    return source;
  };
  return { views, statuses, close, opened: sources.opened, latest };
}

// Lets the answers to probes arrive.
async function settled(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
  }
}

// Runs the pending reconnect and gives how long it waited.
function waitForReconnect(): number {
  const before = Date.now();
  vi.advanceTimersToNextTimer();
  return Date.now() - before;
}

const view = viewOf({ sessions: [sessionOf({ id: 'a' })] });
const laterView = viewOf({ generatedAt: '2026-11-21T18:00:05Z', sessions: [sessionOf({ id: 'b' })] });

describe('createStream', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('connects to the given URL', () => {
    const stream = startStream();
    expect(stream.opened.map((source) => source.url)).toEqual([url]);
  });

  it('passes on each valid view', () => {
    const stream = startStream();
    stream.latest().send(JSON.stringify(view));
    stream.latest().send(JSON.stringify(laterView));
    expect(stream.views).toEqual([view, laterView]);
  });

  it('reports live once a valid view arrives, not when the connection opens', () => {
    const stream = startStream();
    stream.latest().open();
    expect(stream.statuses).toEqual([]);
    stream.latest().send(JSON.stringify(view));
    expect(stream.statuses).toEqual(['live']);
  });

  it('ignores a payload that is not a valid view', () => {
    const stream = startStream();
    for (const payload of ['not json', '{}', JSON.stringify({ ...view, sessions: null }), 42]) {
      stream.latest().send(payload);
    }
    expect(stream.views).toEqual([]);
    expect(stream.statuses).toEqual([]);

    stream.latest().send(JSON.stringify(view));
    expect(stream.views).toEqual([view]);
  });

  it('closes a failed connection at once and reconnects after 1 s, 2 s, 5 s and then every 10 s', () => {
    const stream = startStream();
    const waits: number[] = [];
    for (let attempt = 0; attempt < 6; attempt++) {
      const failed = stream.latest();
      failed.fail();
      expect(failed.readyState).toBe(failed.CLOSED);
      expect(stream.opened).toHaveLength(attempt + 1);
      waits.push(waitForReconnect());
      expect(stream.opened).toHaveLength(attempt + 2);
    }
    expect(waits).toEqual([1_000, 2_000, 5_000, 10_000, 10_000, 10_000]);
  });

  it('starts the backoff over once a valid view arrives', () => {
    const stream = startStream();
    stream.latest().fail();
    const first = waitForReconnect();
    stream.latest().fail();
    const second = waitForReconnect();
    stream.latest().send(JSON.stringify(view));
    stream.latest().fail();
    expect([first, second, waitForReconnect()]).toEqual([1_000, 2_000, 1_000]);
  });

  it('keeps backing off when a connection opens but sends no valid view', () => {
    const stream = startStream();
    stream.latest().fail();
    const first = waitForReconnect();
    stream.latest().open();
    stream.latest().send('{}');
    stream.latest().fail();
    expect([first, waitForReconnect()]).toEqual([1_000, 2_000]);
  });

  it('reports each change of status once', () => {
    const stream = startStream();
    stream.latest().send(JSON.stringify(view));
    stream.latest().send(JSON.stringify(laterView));
    stream.latest().fail();
    waitForReconnect();
    stream.latest().fail();
    waitForReconnect();
    stream.latest().send(JSON.stringify(view));
    expect(stream.statuses).toEqual(['live', 'reconnecting', 'live']);
  });

  it('ignores a connection it has closed', () => {
    const stream = startStream();
    const failed = stream.latest();
    failed.fail();
    failed.fail();
    failed.send(JSON.stringify(view));
    expect(vi.getTimerCount()).toBe(1);
    expect(stream.views).toEqual([]);
    expect(stream.statuses).toEqual(['reconnecting']);
  });

  it('closes its connection when the stream is closed', () => {
    const stream = startStream();
    stream.close();
    expect(stream.latest().readyState).toBe(stream.latest().CLOSED);
    stream.latest().send(JSON.stringify(view));
    stream.latest().fail();
    expect(stream.views).toEqual([]);
    expect(stream.statuses).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('cancels a pending reconnect when closed', () => {
    const stream = startStream();
    stream.latest().fail();
    stream.close();
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(60_000);
    expect(stream.opened).toHaveLength(1);
  });

  it('asks once per run of failures whether the page is still signed in', async () => {
    const probe = vi.fn(async () => 503);
    const stream = startStream(probe);
    stream.latest().fail();
    waitForReconnect();
    stream.latest().fail();
    await settled();
    expect(probe).toHaveBeenCalledTimes(1);

    waitForReconnect();
    stream.latest().send(JSON.stringify(view));
    stream.latest().fail();
    await settled();
    expect(probe).toHaveBeenCalledTimes(2);
  });

  it('reports signed out when the server refuses the page, and keeps trying until it lets it in', async () => {
    const stream = startStream(async () => 401);
    stream.latest().fail();
    await settled();
    expect(stream.statuses).toEqual(['reconnecting', 'signed_out']);

    waitForReconnect();
    stream.latest().fail();
    await settled();
    expect(stream.statuses).toEqual(['reconnecting', 'signed_out']);
    expect(waitForReconnect()).toBe(2_000);

    stream.latest().send(JSON.stringify(view));
    expect(stream.statuses).toEqual(['reconnecting', 'signed_out', 'live']);
  });

  it.each<[string, () => Promise<number>]>([
    ['answers 200', async () => 200],
    ['answers 503', async () => 503],
    ['cannot be reached', async () => Promise.reject(new TypeError('Failed to fetch'))],
  ])('stays reconnecting when the server %s', async (_, probe) => {
    const stream = startStream(probe);
    stream.latest().fail();
    await settled();
    expect(stream.statuses).toEqual(['reconnecting']);
  });

  it('ignores an answer that comes after a view, or after it stopped', async () => {
    let answer: (status: number) => void = () => undefined;
    const late = () => new Promise<number>((resolve) => (answer = resolve));
    const stream = startStream(late);
    stream.latest().fail();
    waitForReconnect();
    stream.latest().send(JSON.stringify(view));
    answer(401);
    await settled();
    expect(stream.statuses).toEqual(['reconnecting', 'live']);

    stream.latest().fail();
    stream.close();
    answer(401);
    await settled();
    expect(stream.statuses).toEqual(['reconnecting', 'live', 'reconnecting']);
  });

  it('uses the browser EventSource by default', () => {
    const sources = fakeEventSources();
    vi.stubGlobal('EventSource', sources.EventSource);
    const close = createStream(url, () => undefined, () => undefined);
    expect(sources.opened.map((source) => source.url)).toEqual([url]);
    close();
  });
});
