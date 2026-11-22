import { formatPct, formatTokens } from '../model/format';
import type { Tone } from './Gauge';

// The context alert fires at 85% by default.
function toneOf(pct: number): Tone {
  if (pct >= 85) {
    return 'bad';
  }
  return pct >= 70 ? 'warn' : 'ok';
}

type ContextBarProps = {
  readonly pct: number;
  /** Tokens left in the window; 0 when its size is unknown. */
  readonly left: number;
};

/** How full a session's context window is: a short bar, the percentage and the tokens left. */
export function ContextBar({ pct, left }: ContextBarProps) {
  const filled = Number.isFinite(pct) ? Math.min(100, Math.max(0, pct)) : 0;
  return (
    <span class={`ctx tone-${toneOf(pct)}`}>
      <span class="ctx-label" aria-hidden="true">
        ctx
      </span>
      <span class="visually-hidden">Context</span>
      <span class="ctx-track" aria-hidden="true">
        <span class="ctx-fill" style={{ width: `${filled}%` }} />
      </span>
      <span class="ctx-text num">
        {formatPct(pct)}
        {left > 0 && <span class="ctx-left"> · {formatTokens(left)} left</span>}
      </span>
    </span>
  );
}
