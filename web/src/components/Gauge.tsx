import type { ComponentChildren } from 'preact';
import { useId } from 'preact/hooks';

/** How a gauge's reading stands: fine, getting close, or at the limit. */
export type Tone = 'ok' | 'warn' | 'bad';

type GaugeProps = {
  readonly label: string;
  /** The id of what the gauge belongs to, such as its account's name, which labels the meter along with label. */
  readonly ownerId: string;
  /** How full the gauge is, in percent. */
  readonly value: number;
  /** The reading as the gauge prints it: 93%, or $8.60. */
  readonly reading: string;
  /** The reading as a screen reader hears it, when it says more than reading. */
  readonly spoken?: string;
  /** Where the value is heading, in percent; the bar shows the way there. */
  readonly heading?: number | null;
  readonly tone: Tone;
  readonly children?: ComponentChildren;
};

/**
 * A horizontal meter with its label and reading above it and a note below.
 * The stretch between the value and where it is heading is hatched: the
 * line the usage is loading up to.
 */
export function Gauge({ label, ownerId, value, reading, spoken, heading = null, tone, children }: GaugeProps) {
  const labelId = useId();
  const filled = percent(value);
  const ahead = heading === null ? 0 : Math.max(0, percent(heading) - filled);
  return (
    <div class={`gauge tone-${tone}`}>
      <div class="gauge-head">
        <span id={labelId} class="gauge-label">
          {label}
        </span>
        <span class="gauge-reading num">{reading}</span>
      </div>
      <div
        class="gauge-track"
        role="meter"
        aria-labelledby={`${ownerId} ${labelId}`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={filled}
        aria-valuetext={spoken ?? reading}
      >
        <span class="gauge-fill" style={{ width: `${filled}%` }} />
        {ahead > 0 && <span class="gauge-ahead" style={{ left: `${filled}%`, width: `${ahead}%` }} />}
      </div>
      {children !== undefined && <div class="gauge-note">{children}</div>}
    </div>
  );
}

// A value the bar can draw: within 0 to 100, and 0 for one that is not a number.
function percent(value: number): number {
  return Number.isFinite(value) ? Math.min(100, Math.max(0, value)) : 0;
}
