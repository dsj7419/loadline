package domain

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"time"
)

// AlertKind says what an alert is about.
type AlertKind string

const (
	// AlertNeedsYou is a session that has waited on the user too long.
	AlertNeedsYou AlertKind = "needs_you"
	// AlertRunsOut is an account that has reached a limit no other alert
	// reports, or whose usage is on pace to hit one before the window resets.
	AlertRunsOut AlertKind = "runs_out"
	// AlertCreditsActive is an account at a plan limit that keeps working on
	// usage credits.
	AlertCreditsActive AlertKind = "credits_active"
	// AlertCreditsLow is an account that has spent most of its recorded or
	// reported usage credits.
	AlertCreditsLow AlertKind = "credits_low"
	// AlertBudget is an account that has spent its daily budget.
	AlertBudget AlertKind = "budget"
	// AlertWeeklyThreshold is an account whose weekly usage is at or over the
	// configured percentage.
	AlertWeeklyThreshold AlertKind = "weekly_threshold"
	// AlertContextHigh is a session whose context window is nearly full.
	AlertContextHigh AlertKind = "context_high"
)

// alertPriority orders alerts by how soon they need attention.
var alertPriority = map[AlertKind]int{
	AlertNeedsYou:        0,
	AlertRunsOut:         1,
	AlertCreditsActive:   2,
	AlertCreditsLow:      3,
	AlertBudget:          4,
	AlertWeeklyThreshold: 5,
	AlertContextHigh:     6,
}

// Alert is something the user should look at. Account alerts leave Session
// empty.
type Alert struct {
	Kind    AlertKind
	Dir     ConfigDir
	Session SessionID
	// Since is when the condition began, as far as Evaluate can tell: when the
	// session entered needs_you, when the account went on credits, and
	// otherwise the evaluation time. The tracker keeps the first Since it saw
	// for an alert of the other kinds that persists.
	Since   time.Time
	Message string
}

// AlertConfig holds the thresholds that raise alerts.
type AlertConfig struct {
	// WeeklyPct is the seven-day usage, in percent, at which an account alerts.
	WeeklyPct float64
	// ContextPct is the context usage, in percent, at which a session alerts.
	ContextPct float64
	// NeedsYouAfter is how long a session may wait on the user before it alerts.
	NeedsYouAfter time.Duration
}

// AlertInput is everything Evaluate looks at.
type AlertInput struct {
	Accounts []Account
	Sessions []Session
	// Projections holds each account's burn-rate projections.
	Projections map[ConfigDir]WindowProjections
	// SpendToday and Budgets are in US dollars, by account. A budget of zero
	// or less means no budget.
	SpendToday, Budgets map[ConfigDir]float64
	// Credits holds each account's usage credits.
	Credits map[ConfigDir]CreditEstimate
}

// Evaluate returns the alerts that hold at now, most urgent first: by kind
// (needs_you, runs_out, credits_active, credits_low, budget,
// weekly_threshold, context_high), then the longest-standing first, then by
// account and session so that the order never depends on how the input was
// listed. Ended and parked sessions raise no alerts. It is pure: Since is the
// evaluation time for every alert but needs_you, which carries the moment
// the session began waiting, and credits_active, which carries the moment
// the account went on credits.
func Evaluate(in AlertInput, cfg AlertConfig, now time.Time) []Alert {
	var alerts []Alert
	for _, account := range in.Accounts {
		alerts = appendAccountAlerts(alerts, account, in, cfg, now)
	}
	for _, session := range in.Sessions {
		alerts = appendSessionAlerts(alerts, session, cfg, now)
	}
	slices.SortStableFunc(alerts, compareAlerts)
	return alerts
}

func compareAlerts(a, b Alert) int {
	return cmp.Or(
		cmp.Compare(alertPriority[a.Kind], alertPriority[b.Kind]),
		a.Since.Compare(b.Since),
		cmp.Compare(a.Dir, b.Dir),
		cmp.Compare(a.Session, b.Session),
	)
}

func appendAccountAlerts(alerts []Alert, account Account, in AlertInput, cfg AlertConfig, now time.Time) []Alert {
	label := accountLabel(account)
	alert := func(kind AlertKind, message string) Alert {
		return Alert{Kind: kind, Dir: account.Dir, Since: now, Message: message}
	}

	if message, ok := runOutMessage(account, label, in, cfg, now); ok {
		alerts = append(alerts, alert(AlertRunsOut, message))
	}
	alerts = appendCreditAlerts(alerts, account.Dir, label, in.Credits[account.Dir], now)
	if budget := in.Budgets[account.Dir]; budget > 0 && in.SpendToday[account.Dir] >= budget {
		alerts = append(alerts, alert(AlertBudget,
			fmt.Sprintf("%s has spent $%.2f today, reaching its $%.2f daily budget", label, in.SpendToday[account.Dir], budget)))
	}
	if weekly := account.Limits.SevenDay; weeklyThresholdReached(weekly, cfg) {
		alerts = append(alerts, alert(AlertWeeklyThreshold,
			fmt.Sprintf("%s is at %.0f%% of its weekly limit", label, weekly.UsedPct)))
	}
	return alerts
}

func weeklyThresholdReached(weekly *Window, cfg AlertConfig) bool {
	return weekly != nil && weekly.UsedPct >= cfg.WeeklyPct
}

// runOutMessage is what the runs_out alert says of the account, if anything.
// A limit the account has reached is said as a fact, unless another alert
// says it already: credits_active, while the account works on its usage
// credits, or weekly_threshold, of the weekly window. Otherwise it names the
// limit the account is on pace to reach before it resets. A limit reached
// comes before one ahead, and of two reached, the weekly one, which holds
// longer.
func runOutMessage(account Account, label string, in AlertInput, cfg AlertConfig, now time.Time) (string, bool) {
	limits := account.Limits
	onCredits := in.Credits[account.Dir].Active
	for _, w := range []struct {
		name     string
		window   *Window
		reported bool
	}{
		{"weekly", limits.SevenDay, onCredits || weeklyThresholdReached(limits.SevenDay, cfg)},
		{"5-hour", limits.FiveHour, onCredits},
	} {
		if atLimit(w.window) && !w.reported {
			return fmt.Sprintf("%s has reached its %s limit%s", label, w.name, resetClause(w.window.ResetsAt, now)), true
		}
	}
	if window, ok := earliestRunOut(in.Projections[account.Dir], limits); ok {
		return fmt.Sprintf("%s is on pace to run out of its %s limit before it resets", label, window), true
	}
	return "", false
}

// atLimit reports whether w is reported and at or past its limit.
func atLimit(w *Window) bool {
	return w != nil && w.UsedPct >= limitPct
}

// resetClause says when a window that resets at resetsAt does, by the clock
// of now's location: ", resets 14:00" later the same day, ", resets Mon
// 09:00" within the week and ", resets Nov 28 09:00" further out. It is
// empty when the reset is unknown or past.
func resetClause(resetsAt, now time.Time) string {
	if !resetsAt.After(now) {
		return ""
	}
	at := resetsAt.In(now.Location())
	switch {
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return ", resets " + at.Format("15:04")
	case at.Sub(now) < 7*24*time.Hour:
		return ", resets " + at.Format("Mon 15:04")
	default:
		return ", resets " + at.Format("Jan 2 15:04")
	}
}

func appendCreditAlerts(alerts []Alert, dir ConfigDir, label string, credits CreditEstimate, now time.Time) []Alert {
	if credits.Active {
		alerts = append(alerts, Alert{
			Kind: AlertCreditsActive, Dir: dir, Since: credits.OnCreditsSince,
			Message: fmt.Sprintf("%s is using usage credits", label),
		})
	}
	if creditsLow(credits) {
		about := ""
		if credits.Source == CreditSourceEstimate {
			about = "about "
		}
		alerts = append(alerts, Alert{
			Kind: AlertCreditsLow, Dir: dir, Since: now,
			Message: fmt.Sprintf("%s has used %s$%.2f of its $%.2f in usage credits", label, about, credits.SpentUSD, credits.BalanceUSD),
		})
	}
	return alerts
}

func appendSessionAlerts(alerts []Alert, session Session, cfg AlertConfig, now time.Time) []Alert {
	if session.State == StateEnded || session.State == StateParked {
		return alerts
	}
	label := sessionLabel(session)

	if session.State == StateNeedsYou && now.Sub(session.StateAt) >= cfg.NeedsYouAfter {
		alerts = append(alerts, Alert{
			Kind: AlertNeedsYou, Dir: session.Dir, Session: session.ID, Since: session.StateAt,
			Message: needsYouMessage(label, session.WaitReason),
		})
	}
	if session.Metrics.ContextPct >= cfg.ContextPct {
		alerts = append(alerts, Alert{
			Kind: AlertContextHigh, Dir: session.Dir, Session: session.ID, Since: now,
			Message: fmt.Sprintf("%s is at %.0f%% of its context window", label, session.Metrics.ContextPct),
		})
	}
	return alerts
}

// earliestRunOut names the window that is on pace to run out soonest. A tie
// goes to the seven-day window.
func earliestRunOut(p WindowProjections, limits Limits) (window string, ok bool) {
	sevenDay := onPaceToRunOut(p.SevenDay, limits.SevenDay)
	fiveHour := onPaceToRunOut(p.FiveHour, limits.FiveHour)
	switch {
	case sevenDay && fiveHour && p.FiveHour.ETA.Before(p.SevenDay.ETA):
		return "5-hour", true
	case sevenDay:
		return "weekly", true
	case fiveHour:
		return "5-hour", true
	default:
		return "", false
	}
}

// onPaceToRunOut reports whether a window is projected to reach its limit
// before it resets. One already at its limit has reached it, which is said
// as a fact or by another alert; one that w does not report is judged by its
// projection alone.
func onPaceToRunOut(p Projection, w *Window) bool {
	return p.Known && p.RunsOutBeforeReset && !atLimit(w)
}

func needsYouMessage(label, waitReason string) string {
	if waitReason == "" {
		return fmt.Sprintf("%s needs you", label)
	}
	return fmt.Sprintf("%s needs you: %s", label, waitReason)
}

// accountLabel is the name the user gave the account, or else its directory's
// name.
func accountLabel(a Account) string {
	if a.Label != "" {
		return a.Label
	}
	return filepath.Base(string(a.Dir))
}

// shortIDLength is how much of a session ID names a session that has no name.
const shortIDLength = 8

// sessionLabel is the session's name, or else the start of its ID.
func sessionLabel(s Session) string {
	if s.Name != "" {
		return s.Name
	}
	id := []rune(string(s.ID))
	return string(id[:min(len(id), shortIDLength)])
}
