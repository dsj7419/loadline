import type { ReadonlySignal } from '@preact/signals';
import { useId } from 'preact/hooks';
import type { AccountView, CreditsView, WindowView } from '../api/types';
import { isCurrent, windowNote } from '../model/describe';
import { formatPace, formatPct, formatUSD, formatUSDBrief, formatWhen } from '../model/format';
import { Gauge, type Tone } from './Gauge';
import { WarningIcon } from './Icons';
import { ModelSplit, rangeLegend, type Range } from './ModelsBar';

type AccountCardProps = {
  readonly account: AccountView;
  readonly now: ReadonlySignal<Date>;
  /** The period the card's model split covers, as the models bar shows. */
  readonly range: Range;
};

/**
 * One account: who it is, and either its rate-limit windows or, for an
 * account billed per token, its spend, then, for a subscription, its usage
 * credits when it has any. The windows count down against the page's clock
 * and drop out as soon as they reset.
 */
export function AccountCard({ account, now, range }: AccountCardProps) {
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
      {account.auth === 'subscription' && account.credits !== null && (
        <Credits credits={account.credits} now={now.value} />
      )}
      <div class="account-foot">
        <ModelSplit
          models={range === 'today' ? account.models : account.modelsWeek}
          label={rangeLegend(range, account.label)}
          compact
        />
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
  const reported = [
    { label: '5-hour', window: account.fiveHour },
    { label: 'Weekly', window: account.sevenDay },
  ].filter((w): w is { label: string; window: WindowView } => w.window !== null);
  if (reported.length === 0) {
    return <p class="empty">No rate limits reported yet.</p>;
  }
  // The server drops a window once a status line shows it has reset; until
  // then the page knows only that its figures are out of date.
  const windows = reported.filter(({ window }) => isCurrent(window, now));
  if (windows.length === 0) {
    return <p class="empty">Limits reset; waiting for new figures</p>;
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
      {window.pace !== undefined && <Pace pace={window.pace} />}
    </Gauge>
  );
}

// How fast a window is burning, as a multiplier of the pace that reaches 100%
// at reset. A screen reader hears it in words rather than as a × sign, and
// the tooltip, on the figure itself, is not read out a second time.
function Pace({ pace }: { readonly pace: number }) {
  const shown = formatPace(pace);
  return (
    <>
      <span
        class={`pace num tone-${paceTone(pace)}`}
        aria-hidden="true"
        title={`Burning ${shown}× faster than the pace that reaches 100% at reset.`}
      >
        ×{shown}
      </span>
      <span class="visually-hidden">Pace {shown} times the rate that reaches 100% at reset</span>
    </>
  );
}

// A pace of 1 uses the window up just as it resets. Near it is worth
// watching; well past it, the limit comes first.
function paceTone(pace: number): Tone {
  if (pace > 1.2) {
    return 'bad';
  }
  return pace >= 0.9 ? 'warn' : 'ok';
}

function usageTone(pct: number): Tone {
  if (pct >= 90) {
    return 'bad';
  }
  return pct >= 75 ? 'warn' : 'ok';
}

type CreditsProps = {
  readonly credits: CreditsView;
  readonly now: Date;
};

// What a subscription's usage credits come to. Worked-out figures are marked
// as estimates, and say what they are made from; a reported spend limit's are
// shown as they are.
function Credits({ credits, now }: CreditsProps) {
  const estimate = credits.source === 'estimate';
  const days = credits.days === 1 ? '1 day' : `${credits.days} days`;
  return (
    <div class="credits">
      <p
        class="credits-figures"
        title={estimate ? `Estimated from the last ${days} of usage` : 'As the spend limit reports them'}
      >
        Credits {estimate ? '≈ ' : ''}
        <Amount usd={credits.spentUSD} /> of <Amount usd={credits.balanceUSD} /> used ·{' '}
        <Amount usd={credits.remainingUSD} /> left
      </p>
      {credits.active && (
        <p class="credits-state">
          <span class="badge badge-bad">
            <WarningIcon />
            On credits
          </span>
          {credits.onCreditsSince !== null && ` since ${formatWhen(credits.onCreditsSince, now)}`}
        </p>
      )}
    </div>
  );
}

function Amount({ usd }: { readonly usd: number }) {
  return <span class="num">{formatUSDBrief(usd)}</span>;
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
