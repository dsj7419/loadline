import {
  alertKinds,
  authKinds,
  creditSources,
  sessionKinds,
  sessionStates,
  type AccountView,
  type AlertView,
  type CreditsView,
  type Int,
  type ModelTotal,
  type ProjectionView,
  type SessionView,
  type Timestamp,
  type TransitionView,
  type View,
  type WindowView,
} from './types';

/**
 * The shape of one JSON value in the API. isView checks a payload against
 * viewSchema, and the contract test walks viewSchema alongside the server's
 * golden payload to prove that the guard knows every field the server sends.
 */
export type Schema =
  | { readonly kind: 'text' | 'timestamp' | 'finite' | 'integer' | 'flag' }
  | { readonly kind: 'enum'; readonly values: readonly string[] }
  | { readonly kind: 'nullable' | 'optional' | 'array'; readonly of: Schema }
  | { readonly kind: 'object'; readonly fields: Readonly<Record<string, Schema>> };

declare const describes: unique symbol;

// A schema that describes values of type T. The phantom member makes the
// compiler hold each schema below to its type exactly: a field cannot be
// missing, extra, nullable or optional when the type is not, or the other way
// round.
// Because Timestamp and Int are branded, a timestamp field cannot take the
// text schema, nor an integer field the finite one.
type SchemaOf<T> = Schema & { readonly [describes]?: (value: T) => T };

const text: SchemaOf<string> = { kind: 'text' };
const timestamp: SchemaOf<Timestamp> = { kind: 'timestamp' };
const finite: SchemaOf<number> = { kind: 'finite' };
const integer: SchemaOf<Int> = { kind: 'integer' };
const flag: SchemaOf<boolean> = { kind: 'flag' };

function oneOf<const T extends string>(values: readonly T[]): SchemaOf<T> {
  return { kind: 'enum', values };
}

function nullable<T>(of: SchemaOf<T>): SchemaOf<T | null> {
  return { kind: 'nullable', of };
}

// A field the server leaves out, rather than sending null, when it has no
// value for it.
function optional<T>(of: SchemaOf<T>): SchemaOf<T | undefined> {
  return { kind: 'optional', of };
}

function listOf<T>(of: SchemaOf<T>): SchemaOf<readonly T[]> {
  return { kind: 'array', of };
}

function shape<T>(fields: { readonly [K in keyof T]-?: SchemaOf<T[K]> }): SchemaOf<T> {
  return { kind: 'object', fields };
}

const modelTotal = shape<ModelTotal>({
  model: text,
  input: integer,
  output: integer,
  cacheRead: integer,
  cacheWrite: integer,
  costUSD: finite,
  requests: integer,
});

const projectionView = shape<ProjectionView>({
  ratePerHour: finite,
  atReset: finite,
  eta: nullable(timestamp),
  runsOutBeforeReset: flag,
});

const windowView = shape<WindowView>({
  usedPct: finite,
  resetsAt: nullable(timestamp),
  observedAt: nullable(timestamp),
  projection: nullable(projectionView),
  pace: optional(finite),
});

const creditsView = shape<CreditsView>({
  balanceUSD: finite,
  spentUSD: finite,
  remainingUSD: finite,
  days: integer,
  onCreditsSince: nullable(timestamp),
  active: flag,
  source: oneOf(creditSources),
});

const accountView = shape<AccountView>({
  dir: text,
  label: text,
  email: text,
  plan: text,
  auth: oneOf(authKinds),
  fiveHour: nullable(windowView),
  sevenDay: nullable(windowView),
  spendTodayUSD: finite,
  spendWeekUSD: finite,
  spendMonthUSD: finite,
  budgetUSD: finite,
  credits: nullable(creditsView),
  models: listOf(modelTotal),
  modelsWeek: listOf(modelTotal),
});

const transitionView = shape<TransitionView>({
  at: timestamp,
  state: oneOf(sessionStates),
  reason: text,
});

const sessionView = shape<SessionView>({
  id: text,
  dir: text,
  account: text,
  kind: oneOf(sessionKinds),
  state: oneOf(sessionStates),
  waitReason: text,
  name: text,
  project: text,
  pid: integer,
  startedAt: timestamp,
  stateAt: timestamp,
  lastSeenAt: timestamp,
  subagents: integer,
  model: text,
  effort: text,
  contextPct: finite,
  contextLeft: integer,
  costUSD: finite,
  cacheHitRatio: finite,
  linesAdded: integer,
  linesRemoved: integer,
  share: finite,
  lost: flag,
  replaces: text,
  models: listOf(modelTotal),
  timeline: listOf(transitionView),
});

const alertView = shape<AlertView>({
  kind: oneOf(alertKinds),
  dir: text,
  session: text,
  since: timestamp,
  message: text,
});

/** The shape of the whole payload. */
export const viewSchema: Schema = shape<View>({
  generatedAt: timestamp,
  accounts: listOf(accountView),
  sessions: listOf(sessionView),
  models: listOf(modelTotal),
  modelsWeek: listOf(modelTotal),
  alerts: listOf(alertView),
});

/**
 * Reports whether x is a View. Every field the server always sends must be
 * present, and every field present must have the right type; fields it does
 * not know are allowed, so that a page left open across an upgrade keeps
 * working.
 */
export function isView(x: unknown): x is View {
  return matches(viewSchema, x);
}

function matches(schema: Schema, value: unknown): boolean {
  switch (schema.kind) {
    case 'text':
      return typeof value === 'string';
    case 'timestamp':
      return isTimestamp(value);
    case 'finite':
      return Number.isFinite(value);
    case 'integer':
      return Number.isInteger(value);
    case 'flag':
      return typeof value === 'boolean';
    case 'enum':
      return typeof value === 'string' && schema.values.includes(value);
    case 'nullable':
      return value === null || matches(schema.of, value);
    case 'optional':
      return value === undefined || matches(schema.of, value);
    case 'array':
      return Array.isArray(value) && value.every((item: unknown) => matches(schema.of, item));
    case 'object':
      // A missing field reads as undefined, which only an optional schema
      // accepts.
      return isRecord(value) && Object.entries(schema.fields).every(([key, field]) => matches(field, value[key]));
  }
}

// An RFC 3339 date-time, as Go encodes a time.Time.
const rfc3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

/** Reports whether value is an RFC 3339 time that names a real moment. */
export function isTimestamp(value: unknown): value is Timestamp {
  return typeof value === 'string' && rfc3339.test(value) && !Number.isNaN(Date.parse(value));
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
