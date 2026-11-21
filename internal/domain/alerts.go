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
	// AlertRunsOut is an account whose usage is on pace to hit a limit before
	// the window resets.
	AlertRunsOut AlertKind = "runs_out"
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
	AlertBudget:          2,
	AlertWeeklyThreshold: 3,
	AlertContextHigh:     4,
}

// Alert is something the user should look at. Account alerts leave Session
// empty.
type Alert struct {
	Kind    AlertKind
	Dir     ConfigDir
	Session SessionID
	// Since is when the condition began, as far as Evaluate can tell: when the
	// session entered needs_you, and otherwise the evaluation time. The
	// tracker keeps the first Since it saw for an alert that persists.
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
}

// Evaluate returns the alerts that hold at now, most urgent first: by kind
// (needs_you, runs_out, budget, weekly_threshold, context_high), then the
// longest-standing first, then by account and session so that the order never
// depends on how the input was listed. Ended and parked sessions raise no
// alerts. It is pure: Since is the evaluation time for every alert but
// needs_you, which carries the moment the session began waiting.
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

	if window, ok := earliestRunOut(in.Projections[account.Dir]); ok {
		alerts = append(alerts, alert(AlertRunsOut,
			fmt.Sprintf("%s is on pace to run out of its %s limit before it resets", label, window)))
	}
	if budget := in.Budgets[account.Dir]; budget > 0 && in.SpendToday[account.Dir] >= budget {
		alerts = append(alerts, alert(AlertBudget,
			fmt.Sprintf("%s has spent $%.2f today, reaching its $%.2f daily budget", label, in.SpendToday[account.Dir], budget)))
	}
	if weekly := account.Limits.SevenDay; weekly != nil && weekly.UsedPct >= cfg.WeeklyPct {
		alerts = append(alerts, alert(AlertWeeklyThreshold,
			fmt.Sprintf("%s is at %.0f%% of its weekly limit", label, weekly.UsedPct)))
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

// earliestRunOut names the window of the projection that runs out soonest. A
// tie goes to the seven-day window.
func earliestRunOut(p WindowProjections) (window string, ok bool) {
	sevenDay := p.SevenDay.Known && p.SevenDay.RunsOutBeforeReset
	fiveHour := p.FiveHour.Known && p.FiveHour.RunsOutBeforeReset
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
