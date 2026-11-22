// The dashboard's API: the JSON form of the server's tracker.View, field for
// field. Every list is an array, never null, and every time is UTC.

export const sessionStates = [
  'starting',
  'working',
  'needs_you',
  'idle',
  'compacting',
  'error',
  'parked',
  'ended',
] as const;
export type SessionState = (typeof sessionStates)[number];

export const sessionKinds = ['interactive', 'background'] as const;
export type SessionKind = (typeof sessionKinds)[number];

export const authKinds = ['unknown', 'subscription', 'api_key', 'third_party'] as const;
export type AuthKind = (typeof authKinds)[number];

export const alertKinds = ['needs_you', 'runs_out', 'budget', 'weekly_threshold', 'context_high'] as const;
export type AlertKind = (typeof alertKinds)[number];

declare const brand: unique symbol;

/**
 * An RFC 3339 time. The brand keeps plain strings out, so only a value that
 * isView has checked can be a Timestamp.
 */
export type Timestamp = string & { readonly [brand]: 'Timestamp' };

/** A whole number: a count, a token total or a pid. Branded like Timestamp. */
export type Int = number & { readonly [brand]: 'Int' };

/** Everything the dashboard shows, as of generatedAt. */
export type View = {
  readonly generatedAt: Timestamp;
  readonly accounts: readonly AccountView[];
  readonly sessions: readonly SessionView[];
  /** Today's usage across all accounts. */
  readonly models: readonly ModelTotal[];
  /** Usage over the last seven local days, today included. */
  readonly modelsWeek: readonly ModelTotal[];
  readonly alerts: readonly AlertView[];
};

/** One account, its rate limits and its spend. */
export type AccountView = {
  readonly dir: string;
  readonly label: string;
  readonly email: string;
  readonly plan: string;
  readonly auth: AuthKind;
  /** Null when the account reports no such window, or it has reset since. */
  readonly fiveHour: WindowView | null;
  readonly sevenDay: WindowView | null;
  /** Spend today and over the last 7 and 30 local days. */
  readonly spendTodayUSD: number;
  readonly spendWeekUSD: number;
  readonly spendMonthUSD: number;
  /** The daily budget, or 0 for none. */
  readonly budgetUSD: number;
  readonly models: readonly ModelTotal[];
  readonly modelsWeek: readonly ModelTotal[];
};

/** One rate-limit window. */
export type WindowView = {
  readonly usedPct: number;
  /** Null when the reset time is unknown. */
  readonly resetsAt: Timestamp | null;
  readonly observedAt: Timestamp | null;
  /** Null while the trend is unknown. */
  readonly projection: ProjectionView | null;
};

/** Where a window's usage is heading. */
export type ProjectionView = {
  readonly ratePerHour: number;
  readonly atReset: number;
  /** When usage reaches 100% at the current rate; null when it is not rising. */
  readonly eta: Timestamp | null;
  readonly runsOutBeforeReset: boolean;
};

/** One session. */
export type SessionView = {
  readonly id: string;
  /** The session's account directory; account is its label. */
  readonly dir: string;
  readonly account: string;
  readonly kind: SessionKind;
  readonly state: SessionState;
  readonly waitReason: string;
  readonly name: string;
  readonly project: string;
  readonly pid: Int;
  readonly startedAt: Timestamp;
  readonly stateAt: Timestamp;
  readonly lastSeenAt: Timestamp;
  readonly subagents: Int;
  readonly model: string;
  readonly effort: string;
  readonly contextPct: number;
  /** Tokens left in the context window; 0 when its size is unknown. */
  readonly contextLeft: Int;
  readonly costUSD: number;
  readonly cacheHitRatio: number;
  readonly linesAdded: Int;
  readonly linesRemoved: Int;
  /** The session's estimated fraction of its account's recent spend. */
  readonly share: number;
  readonly lost: boolean;
  /** The session that /clear ended to start this one, or empty. */
  readonly replaces: string;
  /** Usage over the session's whole life. */
  readonly models: readonly ModelTotal[];
  readonly timeline: readonly TransitionView[];
};

/** A session entering a state. */
export type TransitionView = {
  readonly at: Timestamp;
  readonly state: SessionState;
  readonly reason: string;
};

/** The usage of one model. */
export type ModelTotal = {
  readonly model: string;
  readonly input: Int;
  readonly output: Int;
  readonly cacheRead: Int;
  readonly cacheWrite: Int;
  readonly costUSD: number;
  readonly requests: Int;
};

/** Something the user should look at. Account alerts have an empty session. */
export type AlertView = {
  readonly kind: AlertKind;
  readonly dir: string;
  readonly session: string;
  readonly since: Timestamp;
  readonly message: string;
};
