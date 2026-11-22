import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { isView, viewSchema, type Schema } from '../src/api/guard';
import { alertKinds, authKinds, creditSources } from '../src/api/types';

// TestView_GoldenJSON in internal/tracker holds this file to the JSON of a
// View that fills every field, so it is the API exactly as the server sends it.
const goldenURL = new URL('../../internal/tracker/testdata/view.golden.json', import.meta.url);
const golden: unknown = JSON.parse(readFileSync(goldenURL, 'utf8'));

type Path = readonly (string | number)[];

function isObject(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function show(path: Path): string {
  return path.map((step) => (typeof step === 'number' ? `[${step}]` : `.${step}`)).join('');
}

// Every value nested in value, with the path to it.
function* valuesIn(value: unknown, path: Path = []): Generator<{ path: Path; value: unknown }> {
  const children: [string | number, unknown][] = Array.isArray(value)
    ? value.map((item: unknown, index) => [index, item])
    : isObject(value)
      ? Object.entries(value)
      : [];
  for (const [step, child] of children) {
    const childPath = [...path, step];
    yield { path: childPath, value: child };
    yield* valuesIn(child, childPath);
  }
}

const absent = Symbol('absent');

// A copy of value with the value at path replaced, or removed when the
// replacement is absent.
function edit(value: unknown, path: Path, replacement: unknown): unknown {
  const [step, ...rest] = path;
  if (step === undefined) {
    return replacement;
  }
  if (typeof step === 'number' && Array.isArray(value)) {
    return value.map((item: unknown, index) => (index === step ? edit(item, rest, replacement) : item));
  }
  if (typeof step === 'string' && isObject(value)) {
    if (rest.length === 0 && replacement === absent) {
      return Object.fromEntries(Object.entries(value).filter(([key]) => key !== step));
    }
    return { ...value, [step]: edit(value[step], rest, replacement) };
  }
  throw new Error(`no ${String(step)} in ${JSON.stringify(value)}`);
}

// The paths to values in the payload that the schema does not describe.
function undescribed(schema: Schema, value: unknown, path: Path = []): Path[] {
  switch (schema.kind) {
    case 'nullable':
      return value === null ? [] : undescribed(schema.of, value, path);
    case 'array':
      return Array.isArray(value)
        ? value.flatMap((item: unknown, index) => undescribed(schema.of, item, [...path, index]))
        : [];
    case 'object':
      return isObject(value)
        ? Object.entries(value).flatMap(([key, field]) => {
            const fieldSchema = schema.fields[key];
            return fieldSchema === undefined ? [[...path, key]] : undescribed(fieldSchema, field, [...path, key]);
          })
        : [];
    default:
      return [];
  }
}

// The value lists of every enum in schema.
function* enumsIn(schema: Schema): Generator<readonly string[]> {
  switch (schema.kind) {
    case 'enum':
      yield schema.values;
      return;
    case 'nullable':
    case 'array':
      yield* enumsIn(schema.of);
      return;
    case 'object':
      for (const field of Object.values(schema.fields)) {
        yield* enumsIn(field);
      }
      return;
    default:
      return;
  }
}

// A value of another JSON type than the one given.
function wrongTypeFor(value: unknown): unknown {
  return typeof value === 'string' ? 0 : 'wrong';
}

// The sweeps below change these one at a time.
const everyValue = [...valuesIn(golden)];
const everyField = everyValue.filter(({ path }) => typeof path.at(-1) === 'string');

describe('the View contract', () => {
  it('accepts the payload the server sends', () => {
    expect(isView(golden)).toBe(true);
  });

  it('describes every field the server sends', () => {
    expect(undescribed(viewSchema, golden).map(show)).toEqual([]);
  });

  it('would report a field the schema does not describe', () => {
    const extended = edit(golden, ['sessions', 0, 'addedLater'], 1);
    expect(undescribed(viewSchema, extended).map(show)).toEqual(['.sessions[0].addedLater']);
  });

  it('sweeps every value and every field, down to the deepest', () => {
    const deepest = '.sessions[0].timeline[0].reason';
    expect(everyValue.map(({ path }) => show(path))).toContain(deepest);
    expect(everyField.map(({ path }) => show(path))).toContain(deepest);
  });

  it('holds every value of every enum, so the sweeps below try them all', () => {
    const seen = new Map<readonly string[], Set<string>>();
    const collect = (schema: Schema, value: unknown): void => {
      switch (schema.kind) {
        case 'enum':
          if (typeof value === 'string') {
            seen.set(schema.values, (seen.get(schema.values) ?? new Set<string>()).add(value));
          }
          return;
        case 'nullable':
          if (value !== null) {
            collect(schema.of, value);
          }
          return;
        case 'array':
          if (Array.isArray(value)) {
            value.forEach((item: unknown) => {
              collect(schema.of, item);
            });
          }
          return;
        case 'object':
          if (isObject(value)) {
            Object.entries(schema.fields).forEach(([key, field]) => {
              collect(field, value[key]);
            });
          }
          return;
        default:
          return;
      }
    };
    collect(viewSchema, golden);

    const enums = [...enumsIn(viewSchema)];
    expect(enums).toEqual(expect.arrayContaining([authKinds, alertKinds, creditSources]));
    for (const values of enums) {
      expect([...(seen.get(values) ?? [])].sort()).toEqual([...values].sort());
    }
  });

  it('rejects the payload when any one value has the wrong type', () => {
    const accepted = everyValue
      .filter(({ path, value }) => isView(edit(golden, path, wrongTypeFor(value))))
      .map(({ path }) => show(path));
    expect(accepted).toEqual([]);
  });

  it('rejects the payload when any one field is missing', () => {
    const accepted = everyField
      .filter(({ path }) => isView(edit(golden, path, absent)))
      .map(({ path }) => show(path));
    expect(accepted).toEqual([]);
  });

  it.each<[string, Path, unknown]>([
    ['an unknown session state', ['sessions', 0, 'state'], 'asleep'],
    ['an unknown session kind', ['sessions', 0, 'kind'], 'batch'],
    ['an unknown auth kind', ['accounts', 0, 'auth'], 'oauth'],
    ['an unknown alert kind', ['alerts', 0, 'kind'], 'urgent'],
    ['an unknown credit source', ['accounts', 0, 'credits', 'source'], 'guessed'],
    ['a timestamp that is not RFC 3339', ['generatedAt'], '21 Nov 2026 18:00'],
    ['an impossible timestamp', ['sessions', 0, 'startedAt'], '2026-13-45T18:00:00Z'],
    ['a nullable timestamp that is not RFC 3339', ['accounts', 0, 'sevenDay', 'resetsAt'], 'soon'],
    ['null for a timestamp that is always set', ['sessions', 0, 'stateAt'], null],
    ['null for a list', ['sessions'], null],
    ['an infinite amount', ['accounts', 0, 'spendTodayUSD'], Number.POSITIVE_INFINITY],
    ['NaN for an amount', ['sessions', 0, 'contextPct'], Number.NaN],
    ['a fractional count', ['sessions', 0, 'subagents'], 1.5],
    ['a fractional token count', ['models', 0, 'input'], 10.25],
    ['a list element of the wrong type', ['alerts', 0], 'needs_you'],
  ])('rejects %s', (_, path, replacement) => {
    expect(isView(edit(golden, path, replacement))).toBe(false);
  });

  it.each<[string, Path, unknown]>([
    ['a missing rate-limit window', ['accounts', 0, 'sevenDay'], null],
    ['an account without credits', ['accounts', 0, 'credits'], null],
    ['credits not in use', ['accounts', 0, 'credits', 'onCreditsSince'], null],
    ['an unknown observation time', ['accounts', 0, 'fiveHour', 'observedAt'], null],
    ['a timestamp with a fraction and an offset', ['generatedAt'], '2026-11-21T19:00:00.123456789+01:00'],
    ['empty lists', ['sessions'], []],
  ])('accepts %s', (_, path, replacement) => {
    expect(isView(edit(golden, path, replacement))).toBe(true);
  });

  it('accepts fields it does not know, so a page left open across an upgrade keeps working', () => {
    expect(isView(edit(golden, ['sessions', 0, 'addedLater'], 1))).toBe(true);
  });

  // Each row is wrapped because it.each spreads an array row into arguments,
  // which would turn the [] row into a call with no payload at all.
  it.each<[unknown]>([[null], [[]], ['view'], [42]])('rejects %j as a payload', (payload) => {
    expect(isView(payload)).toBe(false);
  });
});
