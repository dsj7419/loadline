import { useId } from 'preact/hooks';
import type { ModelTotal } from '../api/types';
import { formatPct, formatUSD } from '../model/format';
import { modelLabel, modelShares, modelTone } from '../model/models';

type SplitProps = {
  readonly models: readonly ModelTotal[];
  /** Names the legend for screen readers. */
  readonly label: string;
  /** A compact split is smaller, and says nothing when there is no usage. */
  readonly compact?: boolean;
  readonly empty?: string;
};

/** How usage divides between models: a stacked bar and a legend. */
export function ModelSplit({ models, label, compact = false, empty = '' }: SplitProps) {
  const shares = modelShares(models);
  if (shares.length === 0) {
    return empty === '' ? null : <p class="empty">{empty}</p>;
  }
  return (
    <div class={compact ? 'split split-compact' : 'split'}>
      <div class="split-bar" aria-hidden="true">
        {shares.map(({ model, share }) => (
          <span key={model} class={`split-seg model-${modelTone(model)}`} style={{ flexGrow: share }} />
        ))}
      </div>
      <ul class="legend" aria-label={label}>
        {shares.map(({ model, share, costUSD }) => (
          <li key={model} class="legend-item">
            <span class={`swatch model-${modelTone(model)}`} aria-hidden="true" />
            <span class="legend-name">{modelLabel(model)}</span> <span class="legend-pct num">{formatPct(share * 100)}</span>
            <span class="legend-cost num"> {formatUSD(costUSD)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The period the page's usage covers: today, or the last 7 days. */
export type Range = 'today' | 'week';

const ranges: readonly Range[] = ['today', 'week'];

const rangeText: Readonly<Record<Range, { readonly label: string; readonly legend: string; readonly empty: string }>> = {
  today: { label: 'Today', legend: 'Models today', empty: 'No model usage today.' },
  week: { label: '7 days', legend: 'Models over the last 7 days', empty: 'No model usage in the last 7 days.' },
};

/** The legend's name for a period's usage, beginning with whose it is. */
export function rangeLegend(range: Range, owner: string): string {
  return `${owner} ${rangeText[range].legend.toLowerCase()}`;
}

type ModelsBarProps = {
  readonly today: readonly ModelTotal[];
  readonly week: readonly ModelTotal[];
  /** The period shown, which the page's account cards follow as well. */
  readonly range: Range;
  readonly onRange: (range: Range) => void;
};

/** The models used across every account, today or over the last 7 days. */
export function ModelsBar({ today, week, range, onRange }: ModelsBarProps) {
  const headingId = useId();
  const models = range === 'today' ? today : week;
  const total = models.reduce((sum, { costUSD }) => sum + (Number.isFinite(costUSD) ? costUSD : 0), 0);
  return (
    <section class="panel models" aria-labelledby={headingId}>
      <div class="section-head">
        <h2 id={headingId}>Models</h2>
        <div class="segmented" role="group" aria-label="Period">
          {ranges.map((key) => (
            <button key={key} type="button" aria-pressed={range === key} onClick={() => onRange(key)}>
              {rangeText[key].label}
            </button>
          ))}
        </div>
        <span class="section-aside num">{formatUSD(total)}</span>
      </div>
      <ModelSplit models={models} label={rangeText[range].legend} empty={rangeText[range].empty} />
    </section>
  );
}
