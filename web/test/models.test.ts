import { describe, expect, it } from 'vitest';
import { modelShares } from '../src/model/models';
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
