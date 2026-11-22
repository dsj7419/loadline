import { useId } from 'preact/hooks';
import type { AlertKind, AlertView } from '../api/types';
import { StateIcon } from './StateTag';
import { WarningIcon } from './Icons';

const kindLabels: Readonly<Record<AlertKind, string>> = {
  needs_you: 'Needs you',
  runs_out: 'Limit',
  credits_active: 'On credits',
  credits_low: 'Credits low',
  budget: 'Budget',
  weekly_threshold: 'Weekly limit',
  context_high: 'Context',
};

/** The current alerts, in the server's order: the most urgent first. */
export function AlertBanner({ alerts }: { readonly alerts: readonly AlertView[] }) {
  const headingId = useId();
  if (alerts.length === 0) {
    return null;
  }
  return (
    <section class="alerts" aria-labelledby={headingId}>
      <h2 id={headingId} class="visually-hidden">
        Alerts
      </h2>
      <ul class="alert-list">
        {alerts.map((alert) => (
          <li key={[alert.kind, alert.dir, alert.session].join('\n')} class={`alert alert-${alert.kind}`}>
            <span class="alert-kind">
              {alert.kind === 'needs_you' ? <StateIcon state="needs_you" /> : <WarningIcon />}
              {kindLabels[alert.kind]}
            </span>
            <span class="alert-message">{alert.message}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
