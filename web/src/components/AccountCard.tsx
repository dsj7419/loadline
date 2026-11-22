import type { ReadonlySignal } from '@preact/signals';
import { useId } from 'preact/hooks';
import type { AccountView, WindowView } from '../api/types';
import { isCurrent, windowNote } from '../model/describe';
import { formatPct, formatUSD } from '../model/format';
import { Gauge, type Tone } from './Gauge';
import { WarningIcon } from './Icons';
import { ModelSplit } from './ModelsBar';

type AccountCardProps = {
  readonly account: AccountView;
  readonly now: ReadonlySignal<Date>;
};

/**
 * One account: who it is, and either its rate-limit windows or, for an
 * account billed per token, its spend. The windows count down against the
 * page's clock and drop out as soon as they reset.
 */
export function AccountCard({ account, now }: AccountCardProps) {
  const titleId = useId();
  const plan = planOf(account);
  return (
    <article class="panel account" aria-labelledby={titleId}>
      <header class="account-head">
        <h3 id={titleId} class="account-label">
          {account.label}
        </h3>
        {plan !== '' && <span class="chip">{plan}</span>}
        {account.email !== '' && <p class="account-email">{account.email}</p>}
      </header>
      {billedPerToken(account) ? (
        <Spend account={account} ownerId={titleId} />
      ) : (
        <Windows account={account} ownerId={titleId} now={now.value} />
      )}
      <div class="account-foot">
        <ModelSplit models={account.models} label={`${account.label} models today`} compact />
      </div>
    </article>
  );
}

function billedPerToken(account: AccountView): boolean {
  return account.auth === 'api_key' || account.auth === 'third_party';
}

function planOf(account: AccountView): string {
  switch (account.auth) {
    case 'api_key':
      return 'API key';
    case 'third_party':
      return account.plan !== '' ? account.plan : 'Cloud provider';
    case 'subscription':
    case 'unknown':
      return account.plan;
  }
}

type WindowsProps = {
  readonly account: AccountView;
  readonly ownerId: string;
  readonly now: Date;
};

function Windows({ account, ownerId, now }: WindowsProps) {
  const windows = [
    { label: '5-hour', window: account.fiveHour },
    { label: 'Weekly', window: account.sevenDay },
  ].filter((w): w is { label: string; window: WindowView } => w.window !== null && isCurrent(w.window, now));
  if (windows.length === 0) {
    return <p class="empty">No rate limits reported yet.</p>;
  }
  return (
    <>
      {windows.map(({ label, window }) => (
        <WindowGauge key={label} label={label} window={window} ownerId={ownerId} now={now} />
      ))}
    </>
  );
}

type WindowGaugeProps = {
  readonly label: string;
  readonly window: WindowView;
  readonly ownerId: string;
  readonly now: Date;
};

function WindowGauge({ label, window, ownerId, now }: WindowGaugeProps) {
  const note = windowNote(window, now);
  const runsOut = note?.runsOut ?? false;
  return (
    <Gauge
      label={label}
      ownerId={ownerId}
      value={window.usedPct}
      reading={formatPct(window.usedPct)}
      spoken={`${formatPct(window.usedPct)} used`}
      heading={window.projection?.atReset ?? null}
      tone={runsOut ? 'bad' : usageTone(window.usedPct)}
    >
      {note !== null && (
        <p class={runsOut ? 'note note-bad' : 'note'}>
          {runsOut && <WarningIcon />}
          {note.text}
        </p>
      )}
    </Gauge>
  );
}

function usageTone(pct: number): Tone {
  if (pct >= 90) {
    return 'bad';
  }
  return pct >= 75 ? 'warn' : 'ok';
}

type SpendProps = {
  readonly account: AccountView;
  readonly ownerId: string;
};

// What an account billed per token has spent, which Claude Code estimates at
// list price.
function Spend({ account, ownerId }: SpendProps) {
  const budget = account.budgetUSD;
  const spent = formatUSD(account.spendTodayUSD);
  return (
    <>
      {budget > 0 ? (
        <Gauge
          label="Spent today"
          ownerId={ownerId}
          value={(100 * account.spendTodayUSD) / budget}
          reading={spent}
          spoken={`${spent} of ${formatUSD(budget)}`}
          tone={usageTone((100 * account.spendTodayUSD) / budget)}
        >
          <p class="note">of {formatUSD(budget)} daily budget</p>
        </Gauge>
      ) : (
        <p class="figure">
          <span class="figure-label">Spent today</span>
          <span class="figure-value num">{spent}</span>
        </p>
      )}
      <dl class="periods">
        <div>
          <dt>7 days</dt>
          <dd class="num">{formatUSD(account.spendWeekUSD)}</dd>
        </div>
        <div>
          <dt>30 days</dt>
          <dd class="num">{formatUSD(account.spendMonthUSD)}</dd>
        </div>
      </dl>
      <p class="fine">All figures estimated at list price</p>
    </>
  );
}
