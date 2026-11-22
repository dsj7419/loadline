import { isView } from '../src/api/guard';
import type { AccountView, AlertView, ModelTotal, SessionView, View } from '../src/api/types';

// Fixtures start as plain JSON and pass through isView, the guard the page
// applies to every payload, so a test only ever holds values the server could
// send. Fields override the defaults by name.
type Fields<T> = { readonly [K in keyof T]?: unknown };

const viewDefaults = {
  generatedAt: '2026-11-21T18:00:00Z',
  accounts: [],
  sessions: [],
  models: [],
  modelsWeek: [],
  alerts: [],
};

const sessionDefaults = {
  id: 'session-1',
  dir: 'C:/Users/example/.claude',
  account: 'Main',
  kind: 'interactive',
  state: 'working',
  waitReason: '',
  name: 'harbor-api',
  project: 'C:/Users/example/projects/harbor-api',
  pid: 4242,
  startedAt: '2026-11-21T17:00:00Z',
  stateAt: '2026-11-21T17:58:00Z',
  lastSeenAt: '2026-11-21T18:00:00Z',
  subagents: 0,
  model: 'Opus 5.5',
  effort: 'high',
  contextPct: 31,
  contextLeft: 138_000,
  costUSD: 1.25,
  cacheHitRatio: 0.9,
  linesAdded: 12,
  linesRemoved: 3,
  share: 0.41,
  lost: false,
  replaces: '',
  models: [],
  timeline: [],
};

const accountDefaults = {
  dir: 'C:/Users/example/.claude',
  label: 'Main',
  email: 'dev@example.com',
  plan: 'Ultra',
  auth: 'subscription',
  fiveHour: null,
  sevenDay: null,
  spendTodayUSD: 0,
  spendWeekUSD: 0,
  spendMonthUSD: 0,
  budgetUSD: 0,
  models: [],
  modelsWeek: [],
};

const alertDefaults = {
  kind: 'needs_you',
  dir: 'C:/Users/example/.claude',
  session: 'session-1',
  since: '2026-11-21T17:58:48Z',
  message: 'harbor-api needs you: permission prompt',
};

const modelDefaults = {
  model: 'Opus 5.5',
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  costUSD: 0,
  requests: 0,
};

export function viewOf(fields: Fields<View> = {}): View {
  const view: unknown = { ...viewDefaults, ...fields };
  if (!isView(view)) {
    throw new Error(`not a view the server could send: ${JSON.stringify(fields)}`);
  }
  return view;
}

export function sessionOf(fields: Fields<SessionView> = {}): SessionView {
  return only(viewOf({ sessions: [{ ...sessionDefaults, ...fields }] }).sessions);
}

export function accountOf(fields: Fields<AccountView> = {}): AccountView {
  return only(viewOf({ accounts: [{ ...accountDefaults, ...fields }] }).accounts);
}

export function alertOf(fields: Fields<AlertView> = {}): AlertView {
  return only(viewOf({ alerts: [{ ...alertDefaults, ...fields }] }).alerts);
}

export function modelOf(fields: Fields<ModelTotal> = {}): ModelTotal {
  return only(viewOf({ models: [{ ...modelDefaults, ...fields }] }).models);
}

function only<T>(items: readonly T[]): T {
  const [item] = items;
  if (item === undefined || items.length !== 1) {
    throw new Error(`want one item, got ${items.length}`);
  }
  return item;
}
