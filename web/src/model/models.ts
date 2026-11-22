import type { ModelTotal } from '../api/types';

/** One model's part of the usage. */
export type ModelShare = {
  readonly model: string;
  readonly costUSD: number;
  /** Between 0 and 1; a list's shares sum to 1. */
  readonly share: number;
};

/**
 * Each model's share of the usage, largest first. Shares are of cost, or of
 * every token processed when no model has a cost (a provider that reports no
 * prices). With no usage at all there is nothing to share, so it gives none.
 */
export function modelShares(models: readonly ModelTotal[]): readonly ModelShare[] {
  const byCost = models.some(({ costUSD }) => costUSD > 0);
  const weighed = models.map((m) => ({
    model: m.model,
    costUSD: m.costUSD,
    weight: usage(byCost ? m.costUSD : m.input + m.output + m.cacheRead + m.cacheWrite),
  }));
  const total = weighed.reduce((sum, { weight }) => sum + weight, 0);
  if (total === 0) {
    return [];
  }
  // sort is stable, so equal shares keep the server's order.
  return weighed
    .map(({ model, costUSD, weight }) => ({ model, costUSD, share: weight / total }))
    .sort((a, b) => b.share - a.share);
}

// An amount that cannot be usage counts as none, so shares stay in 0..1.
function usage(amount: number): number {
  return Number.isFinite(amount) && amount > 0 ? amount : 0;
}

/** The colour a model is drawn in, which the stylesheet names model-<tone>. */
export type ModelTone = 'opus' | 'sonnet' | 'haiku' | 'other1' | 'other2' | 'other3';

const families = ['opus', 'sonnet', 'haiku'] as const;
const spareTones = ['other1', 'other2', 'other3'] as const;

/**
 * The colour of a model: one per Claude model family, and one of three spare
 * colours for any other model. It depends on the name alone, so a model keeps
 * its colour whichever usage the page shows.
 */
export function modelTone(model: string): ModelTone {
  const name = model.toLowerCase();
  const family = families.find((f) => name.includes(f));
  if (family !== undefined) {
    return family;
  }
  return spareTones[hash(name) % spareTones.length] ?? 'other1';
}

// A small, stable string hash (FNV-1a), enough to spread names over a few
// colours.
function hash(text: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h;
}
