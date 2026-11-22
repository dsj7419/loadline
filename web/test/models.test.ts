import { describe, expect, it } from 'vitest';
import { modelLabel, modelShares, modelTone } from '../src/model/models';
import { modelOf } from './fixtures';

describe('modelShares', () => {
  it('shares the cost between models, largest first', () => {
    const shares = modelShares([
      modelOf({ model: 'Haiku 5.5', costUSD: 0.25, input: 3_000 }),
      modelOf({ model: 'Opus 5.5', costUSD: 3.75, input: 1_000 }),
    ]);
    expect(shares).toEqual([
      { model: 'Opus 5.5', costUSD: 3.75, share: 0.9375 },
      { model: 'Haiku 5.5', costUSD: 0.25, share: 0.0625 },
    ]);
  });

  it('gives shares that sum to one', () => {
    const shares = modelShares([
      modelOf({ model: 'Opus 5.5', costUSD: 1 }),
      modelOf({ model: 'Sonnet 5.5', costUSD: 1 }),
      modelOf({ model: 'Haiku 5.5', costUSD: 1 }),
    ]);
    expect(shares.reduce((sum, { share }) => sum + share, 0)).toBeCloseTo(1, 12);
  });

  it('shares every token the models processed when none has a cost', () => {
    const shares = modelShares([
      modelOf({ model: 'Opus 5.5', input: 300, output: 100 }),
      modelOf({ model: 'Sonnet 5.5', cacheRead: 1_200, cacheWrite: 400 }),
    ]);
    expect(shares).toEqual([
      { model: 'Sonnet 5.5', costUSD: 0, share: 0.8 },
      { model: 'Opus 5.5', costUSD: 0, share: 0.2 },
    ]);
  });

  it('gives a model with no cost no share while another model has one', () => {
    const shares = modelShares([
      modelOf({ model: 'Haiku 5.5', costUSD: 0, input: 50_000 }),
      modelOf({ model: 'Opus 5.5', costUSD: 2, input: 10 }),
    ]);
    expect(shares).toEqual([
      { model: 'Opus 5.5', costUSD: 2, share: 1 },
      { model: 'Haiku 5.5', costUSD: 0, share: 0 },
    ]);
  });

  it('keeps the server order between equal shares', () => {
    const shares = modelShares([modelOf({ model: 'B', costUSD: 1 }), modelOf({ model: 'A', costUSD: 1 })]);
    expect(shares.map(({ model }) => model)).toEqual(['B', 'A']);
  });

  it('counts an amount that is negative or not a number as no usage', () => {
    const shares = modelShares([
      modelOf({ model: 'Opus 5.5', costUSD: 3 }),
      modelOf({ model: 'Refund', costUSD: -1 }),
      { ...modelOf({ model: 'Unpriced' }), costUSD: Number.NaN },
    ]);
    expect(shares).toEqual([
      { model: 'Opus 5.5', costUSD: 3, share: 1 },
      { model: 'Refund', costUSD: -1, share: 0 },
      { model: 'Unpriced', costUSD: Number.NaN, share: 0 },
    ]);
  });

  it('gives no shares when there is no usage', () => {
    expect(modelShares([])).toEqual([]);
    expect(modelShares([modelOf({ model: 'Opus 5.5' }), modelOf({ model: 'Haiku 5.5' })])).toEqual([]);
  });
});

describe('modelTone', () => {
  it.each<[string, string]>([
    ['Opus 5.5', 'opus'],
    ['claude-opus-5-5', 'opus'],
    ['Sonnet 5.5', 'sonnet'],
    ['SONNET 4', 'sonnet'],
    ['Haiku 5.5', 'haiku'],
  ])('gives %s the %s tone', (model, want) => {
    expect(modelTone(model)).toBe(want);
  });

  it('gives any other model one of the spare tones, the same every time', () => {
    const names = ['gpt-oss-120b', 'llama-4', 'mistral-large', 'deepseek-v4', 'qwen-3', ''];
    for (const name of names) {
      expect(['other1', 'other2', 'other3']).toContain(modelTone(name));
      expect(modelTone(name)).toBe(modelTone(name));
    }
    expect(new Set(names.map(modelTone)).size).toBeGreaterThan(1);
  });
});

describe('modelLabel', () => {
  it.each<[string, string]>([
    ['claude-opus-5-5', 'Opus 5.5'],
    ['claude-haiku-5-5-20261001', 'Haiku 5.5'],
    ['claude-sonnet-5-5', 'Sonnet 5.5'],
    ['claude-fable-5-2', 'Fable 5.2'],
    ['claude-opus-10-12', 'Opus 10.12'],
    ['Opus 5.5', 'Opus 5.5'],
    ['claude-3-5-sonnet-20240620', 'claude-3-5-sonnet-20240620'],
    ['claude-opus-5', 'Opus 5'],
    ['claude-sonnet-4-20250514', 'Sonnet 4'],
    ['claude-opus-4-20250514', 'Opus 4'],
    ['claude-opus-4-1-20250805', 'Opus 4.1'],
    ['claude-opus-123-1', 'claude-opus-123-1'],
    ['claude-opus-5-123', 'claude-opus-5-123'],
    ['claude-sonnet-4-2025051', 'claude-sonnet-4-2025051'],
    ['claude-opus-5-5-5', 'claude-opus-5-5-5'],
    ['claude-opus-5-5-2026', 'claude-opus-5-5-2026'],
    ['claude-opus-5-5[1m]', 'claude-opus-5-5[1m]'],
    ['xclaude-opus-5-5', 'xclaude-opus-5-5'],
    ['gpt-oss-120b', 'gpt-oss-120b'],
    ['', ''],
  ])('labels %j as %j', (id, want) => {
    expect(modelLabel(id)).toBe(want);
  });
});
