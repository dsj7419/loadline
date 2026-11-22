import { readFile } from 'node:fs/promises';
import { createServer, type ServerResponse } from 'node:http';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { View } from '../src/api/types';

// The built page, as the binary embeds it.
const dist = fileURLToPath(new URL('../../internal/web/dist/', import.meta.url));

// The real server's policy, so the page runs under the same rules.
const contentSecurityPolicy =
  "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; " +
  "script-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'";

const contentTypes: Readonly<Record<string, string>> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.woff2': 'font/woff2',
  '.txt': 'text/plain; charset=utf-8',
};

/** A server that serves the built page and streams the views a test sends. */
export type ScriptedServer = {
  readonly url: string;
  /** Sends view to every open stream, and to streams that open later. */
  readonly send: (view: View) => void;
  readonly close: () => Promise<void>;
};

/**
 * Starts a server on a free loopback port that serves the built dashboard
 * without sign-in, and an /api/events stream that sends first and then each
 * view the test sends. It lets a test change what the page shows at will,
 * which the demo server, fixed at start-up, cannot.
 */
export async function startScriptedServer(first: View): Promise<ScriptedServer> {
  let latest = first;
  let closing = false;
  const streams = new Set<ServerResponse>();
  const frame = (view: View): string => `event: view\ndata: ${JSON.stringify(view)}\n\n`;

  const server = createServer((request, response) => {
    // A backstop: close() already cuts every connection with
    // closeAllConnections, but a request that still reaches the server once
    // closing has begun is turned away rather than given a stream that would
    // hold the server open.
    if (closing) {
      response.writeHead(503, { connection: 'close' }).end();
      return;
    }
    const path = new URL(request.url ?? '/', 'http://127.0.0.1').pathname;
    if (path === '/api/events') {
      response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store' });
      response.write(frame(latest));
      streams.add(response);
      request.on('close', () => streams.delete(response));
      return;
    }
    const name = normalize(path === '/' ? 'index.html' : path.slice(1));
    if (name.startsWith('..')) {
      response.writeHead(404).end();
      return;
    }
    readFile(join(dist, name)).then(
      (body) => {
        response.writeHead(200, {
          'content-type': contentTypes[extname(name)] ?? 'application/octet-stream',
          'content-security-policy': contentSecurityPolicy,
        });
        response.end(body);
      },
      () => response.writeHead(404).end(),
    );
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (address === null || typeof address === 'string') {
    throw new Error('the scripted server is not listening on a port');
  }

  return {
    url: `http://127.0.0.1:${address.port}/`,
    send: (view) => {
      latest = view;
      for (const stream of streams) {
        stream.write(frame(view));
      }
    },
    close: async () => {
      closing = true;
      for (const stream of streams) {
        stream.end();
      }
      const closed = new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
      // server.close waits for every connection to end, and a browser keeps
      // idle ones open, so they are cut here.
      server.closeAllConnections();
      await closed;
    },
  };
}
