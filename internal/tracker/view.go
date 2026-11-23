package tracker

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// View is everything the dashboard shows, as of GeneratedAt. Its JSON form is
// the UI's API. Every list encodes as an array, never null, and every time is
// UTC. A View shares nothing with the tracker; subscribers receive the same
// value, so treat it as read-only.
type View struct {
	GeneratedAt time.Time     `json:"generatedAt"`
	Accounts    []AccountView `json:"accounts"`
	Sessions    []SessionView `json:"sessions"`
	// Models is today's usage across all accounts; ModelsWeek covers the last
	// seven local days, today included.
	Models     []ModelTotal `json:"models"`
	ModelsWeek []ModelTotal `json:"modelsWeek"`
	Alerts     []AlertView  `json:"alerts"`
}

// AccountView is one account, its rate limits and its spend.
type AccountView struct {
	Dir   domain.ConfigDir `json:"dir"`
	Label string           `json:"label"`
	Email string           `json:"email"`
	Plan  string           `json:"plan"`
	Auth  domain.AuthKind  `json:"auth"`
	// FiveHour and SevenDay are null when the account reports no such window,
	// or the window has reset since it was last reported.
	FiveHour *WindowView `json:"fiveHour"`
	SevenDay *WindowView `json:"sevenDay"`
	// The spend figures cover today, the last 7 and the last 30 local days.
	SpendTodayUSD float64 `json:"spendTodayUSD"`
	SpendWeekUSD  float64 `json:"spendWeekUSD"`
	SpendMonthUSD float64 `json:"spendMonthUSD"`
	// BudgetUSD is the daily budget, or 0 for none.
	BudgetUSD float64 `json:"budgetUSD"`
	// Credits is null unless the account is a subscription, and the owner
	// recorded usage credits for it or a status line reported its spend limit
	// within the last hour.
	Credits    *CreditsView `json:"credits"`
	Models     []ModelTotal `json:"models"`
	ModelsWeek []ModelTotal `json:"modelsWeek"`
}

// CreditsView is where an account's usage credits stand, in US dollars.
type CreditsView struct {
	BalanceUSD   float64 `json:"balanceUSD"`
	SpentUSD     float64 `json:"spentUSD"`
	RemainingUSD float64 `json:"remainingUSD"`
	// Days is how many days back an estimate counts spend.
	Days int `json:"days"`
	// OnCreditsSince is when the account went on credits; null unless Active.
	OnCreditsSince Timestamp `json:"onCreditsSince"`
	// Active says the account is at a plan limit and working on credits.
	Active bool `json:"active"`
	// Source is "estimate" for figures worked out from the cost of calls, or
	// "reported" for a spend limit's own figures.
	Source domain.CreditSource `json:"source"`
}

// WindowView is one rate-limit window.
type WindowView struct {
	UsedPct float64 `json:"usedPct"`
	// ResetsAt is null when the reset time is unknown.
	ResetsAt   Timestamp `json:"resetsAt"`
	ObservedAt Timestamp `json:"observedAt"`
	// Projection is null while the trend is unknown.
	Projection *ProjectionView `json:"projection"`
	// Pace is how fast the usage has burned against the pace that reaches
	// 100% at reset, as of GeneratedAt (see domain.Pace). It is left out
	// while there is none, and the page rounds it.
	Pace *float64 `json:"pace,omitempty"`
}

// ProjectionView is where a window's usage is heading.
type ProjectionView struct {
	RatePerHour float64 `json:"ratePerHour"`
	AtReset     float64 `json:"atReset"`
	// ETA is when usage reaches 100% at the current rate; null when usage is
	// not rising.
	ETA                Timestamp `json:"eta"`
	RunsOutBeforeReset bool      `json:"runsOutBeforeReset"`
}

// SessionView is one session.
type SessionView struct {
	ID domain.SessionID `json:"id"`
	// Dir is the session's account and Account its label.
	Dir        domain.ConfigDir `json:"dir"`
	Account    string           `json:"account"`
	Kind       domain.Kind      `json:"kind"`
	State      domain.State     `json:"state"`
	WaitReason string           `json:"waitReason"`
	Name       string           `json:"name"`
	Project    string           `json:"project"`
	PID        int              `json:"pid"`
	StartedAt  time.Time        `json:"startedAt"`
	StateAt    time.Time        `json:"stateAt"`
	LastSeenAt time.Time        `json:"lastSeenAt"`
	Subagents  int              `json:"subagents"`
	Model      string           `json:"model"`
	Effort     string           `json:"effort"`
	ContextPct float64          `json:"contextPct"`
	// ContextLeft is how many tokens the context window has left; 0 when its
	// size is unknown.
	ContextLeft   int64   `json:"contextLeft"`
	CostUSD       float64 `json:"costUSD"`
	CacheHitRatio float64 `json:"cacheHitRatio"`
	LinesAdded    int64   `json:"linesAdded"`
	LinesRemoved  int64   `json:"linesRemoved"`
	// Share is the session's estimated fraction of its account's recent
	// spend.
	Share float64 `json:"share"`
	Lost  bool    `json:"lost"`
	// Replaces is the session that /clear ended to start this one in the same
	// terminal, or empty.
	Replaces domain.SessionID `json:"replaces"`
	// Models is the session's usage over its whole life.
	Models   []ModelTotal     `json:"models"`
	Timeline []TransitionView `json:"timeline"`
}

// TransitionView is a session entering a state.
type TransitionView struct {
	At     time.Time    `json:"at"`
	State  domain.State `json:"state"`
	Reason string       `json:"reason"`
}

// ModelTotal is the usage of one model.
type ModelTotal struct {
	Model      string  `json:"model"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	CostUSD    float64 `json:"costUSD"`
	Requests   int     `json:"requests"`
}

// AlertView is something the user should look at. Account alerts have an
// empty Session.
type AlertView struct {
	Kind    domain.AlertKind `json:"kind"`
	Dir     domain.ConfigDir `json:"dir"`
	Session domain.SessionID `json:"session"`
	Since   time.Time        `json:"since"`
	Message string           `json:"message"`
}

// Timestamp is a moment that may be unknown. It encodes as an RFC 3339 time
// in UTC, or as null for the zero time and for a time outside the years JSON
// can hold, which only a broken clock gives. The view's fields of this type
// may be null; its time.Time fields are always set.
type Timestamp time.Time

// MarshalJSON implements json.Marshaler.
func (ts Timestamp) MarshalJSON() ([]byte, error) {
	t := time.Time(ts)
	if t.IsZero() || !encodable(t) {
		return []byte("null"), nil
	}
	return t.UTC().MarshalJSON()
}

// UnmarshalJSON implements json.Unmarshaler. Null reads as the zero time.
func (ts *Timestamp) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*ts = Timestamp{}
		return nil
	}
	var t time.Time
	if err := t.UnmarshalJSON(data); err != nil {
		return fmt.Errorf("decode timestamp: %w", err)
	}
	*ts = Timestamp(t)
	return nil
}

// earliestEncodable and latestEncodable bound the times JSON can hold:
// time.Time refuses to marshal one whose year, in UTC as the view shows it,
// falls outside 0 to 9999.
var (
	earliestEncodable = time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC)
	latestEncodable   = time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
)

func encodable(t time.Time) bool {
	return !t.Before(earliestEncodable) && !t.After(latestEncodable)
}

// viewTime is t in UTC for a view field that is always set, held within the
// years JSON can hold. Only a broken clock gives a time outside them, and one
// such time must not keep the whole view from encoding.
func viewTime(t time.Time) time.Time {
	switch t = t.UTC(); {
	case t.Before(earliestEncodable):
		return earliestEncodable
	case t.After(latestEncodable):
		return latestEncodable
	default:
		return t
	}
}

// stateOrder is the order sessions are listed in: those that need the user
// first, finished ones last.
var stateOrder = []domain.State{
	domain.StateNeedsYou, domain.StateWorking, domain.StateCompacting, domain.StateError,
	domain.StateStarting, domain.StateIdle, domain.StateParked, domain.StateEnded,
}

// buildView forgets what has aged out by now and returns the view as of
// now. It also dates the alerts, which is why it needs the lock even though
// it only reads otherwise.
func (t *Tracker) buildView(now time.Time) View {
	t.expire(now)
	usage := t.usage.summarize(dayOf(now, t.cfg.Location))

	dirs := slices.Sorted(maps.Keys(t.accounts))
	labels := make(map[domain.ConfigDir]string, len(dirs))
	accounts := make([]AccountView, 0, len(dirs))
	alerting := domain.AlertInput{
		Accounts:    make([]domain.Account, 0, len(dirs)),
		Projections: make(map[domain.ConfigDir]domain.WindowProjections, len(dirs)),
		SpendToday:  make(map[domain.ConfigDir]float64, len(dirs)),
		Budgets:     t.cfg.Budgets,
		Credits:     make(map[domain.ConfigDir]domain.CreditEstimate, len(dirs)),
	}
	for _, dir := range dirs {
		entry := t.accounts[dir]
		account := entry.account
		account.Label = t.label(account)
		account.Limits = entry.currentLimits(now)
		projections := entry.projections(account.Limits, now)
		spend := usage.account(dir)

		labels[dir] = account.Label
		view := t.accountView(account, projections, spend, now)
		// Usage credits are a subscription's; no other account has them.
		if account.Auth == domain.AuthSubscription {
			credits, show := entry.creditEstimate(t.cfg.Credits[dir], now)
			if show {
				view.Credits = creditsView(credits, t.cfg.CreditDays)
			}
			alerting.Credits[dir] = credits
		}
		accounts = append(accounts, view)
		alerting.Accounts = append(alerting.Accounts, account)
		alerting.Projections[dir] = projections
		alerting.SpendToday[dir] = spend.spendTodayUSD
	}

	shares := domain.Shares(t.usage.spend, now, t.cfg.ShareWindow)
	shown := t.shownSessions()
	sessions := make([]SessionView, 0, len(shown))
	alerting.Sessions = make([]domain.Session, 0, len(shown))
	for _, e := range shown {
		sessions = append(sessions, sessionView(e, labels[e.session.Dir], shares[e.session.ID], usage.session(e.session.ID)))
		alerting.Sessions = append(alerting.Sessions, e.session)
	}

	return View{
		GeneratedAt: viewTime(now),
		Accounts:    accounts,
		Sessions:    sessions,
		Models:      usage.today.totals(),
		ModelsWeek:  usage.week.totals(),
		Alerts:      alertViews(t.alerts.date(domain.Evaluate(alerting, t.cfg.Alerts, now))),
	}
}

// label is the account's name: the one configured for it, else the one it
// was given, else its email's local part, else its directory's name.
func (t *Tracker) label(a domain.Account) string {
	local, _, _ := strings.Cut(a.Email, "@")
	return cmp.Or(t.cfg.Labels[a.Dir], a.Label, local, filepath.Base(string(a.Dir)))
}

// plan is the account's plan as shown: the one configured for it, else the
// one it reported.
func (t *Tracker) plan(a domain.Account) string {
	return cmp.Or(t.cfg.Plans[a.Dir], a.Plan)
}

// shownSessions returns the sessions the view lists, in its order: by state,
// then the most recently seen first. A session that /clear replaced is left
// out; its successor continues its row.
func (t *Tracker) shownSessions() []*sessionEntry {
	shown := make([]*sessionEntry, 0, len(t.sessions))
	for _, e := range t.sessions {
		if e.replacedBy != "" && e.session.State == domain.StateEnded {
			continue
		}
		shown = append(shown, e)
	}
	slices.SortFunc(shown, func(a, b *sessionEntry) int {
		return cmp.Or(
			cmp.Compare(slices.Index(stateOrder, a.session.State), slices.Index(stateOrder, b.session.State)),
			b.session.LastSeenAt.Compare(a.session.LastSeenAt),
			cmp.Compare(a.session.ID, b.session.ID),
		)
	})
	return shown
}

func (t *Tracker) accountView(a domain.Account, p domain.WindowProjections, spend accountUsage, now time.Time) AccountView {
	return AccountView{
		Dir:           a.Dir,
		Label:         a.Label,
		Email:         a.Email,
		Plan:          t.plan(a),
		Auth:          a.Auth,
		FiveHour:      windowView(a.Limits.FiveHour, domain.FiveHourLength, a.Limits.ObservedAt, p.FiveHour, now),
		SevenDay:      windowView(a.Limits.SevenDay, domain.SevenDayLength, a.Limits.ObservedAt, p.SevenDay, now),
		SpendTodayUSD: spend.spendTodayUSD,
		SpendWeekUSD:  spend.spendWeekUSD,
		SpendMonthUSD: spend.spendMonthUSD,
		BudgetUSD:     t.cfg.Budgets[a.Dir],
		Models:        spend.today.totals(),
		ModelsWeek:    spend.week.totals(),
	}
}

// windowView shows w, a window of the given length, as of now.
func windowView(w *domain.Window, length time.Duration, observedAt time.Time, p domain.Projection, now time.Time) *WindowView {
	if w == nil {
		return nil
	}
	view := &WindowView{UsedPct: w.UsedPct, ResetsAt: Timestamp(w.ResetsAt), ObservedAt: Timestamp(observedAt)}
	if pace, ok := domain.Pace(w.UsedPct, w.ResetsAt, length, now); ok {
		view.Pace = &pace
	}
	if p.Known {
		view.Projection = &ProjectionView{
			RatePerHour:        p.RatePerHour,
			AtReset:            p.AtReset,
			ETA:                Timestamp(p.ETA),
			RunsOutBeforeReset: p.RunsOutBeforeReset,
		}
	}
	return view
}

func creditsView(e domain.CreditEstimate, days int) *CreditsView {
	return &CreditsView{
		BalanceUSD:     e.BalanceUSD,
		SpentUSD:       e.SpentUSD,
		RemainingUSD:   e.RemainingUSD,
		Days:           days,
		OnCreditsSince: Timestamp(e.OnCreditsSince),
		Active:         e.Active,
		Source:         e.Source,
	}
}

func sessionView(e *sessionEntry, account string, share float64, models []ModelTotal) SessionView {
	s, m := e.session, e.session.Metrics
	var contextLeft int64
	if m.ContextSize > 0 {
		contextLeft = max(0, m.ContextSize-m.ContextUsed)
	}
	timeline := make([]TransitionView, len(e.timeline))
	for i, tr := range e.timeline {
		timeline[i] = TransitionView{At: viewTime(tr.At), State: tr.State, Reason: tr.Reason}
	}
	return SessionView{
		ID:            s.ID,
		Dir:           s.Dir,
		Account:       account,
		Kind:          s.Kind,
		State:         s.State,
		WaitReason:    s.WaitReason,
		Name:          s.Name,
		Project:       s.ProjectDir,
		PID:           s.PID,
		StartedAt:     viewTime(s.StartedAt),
		StateAt:       viewTime(s.StateAt),
		LastSeenAt:    viewTime(s.LastSeenAt),
		Subagents:     s.Subagents,
		Model:         cmp.Or(m.Model, m.ModelID),
		Effort:        m.Effort,
		ContextPct:    m.ContextPct,
		ContextLeft:   contextLeft,
		CostUSD:       m.CostUSD,
		CacheHitRatio: m.CacheHitRatio,
		LinesAdded:    m.LinesAdded,
		LinesRemoved:  m.LinesRemoved,
		Share:         share,
		Lost:          s.Lost,
		Replaces:      e.replaces,
		Models:        models,
		Timeline:      timeline,
	}
}

func alertViews(alerts []domain.Alert) []AlertView {
	views := make([]AlertView, len(alerts))
	for i, a := range alerts {
		views[i] = AlertView{Kind: a.Kind, Dir: a.Dir, Session: a.Session, Since: viewTime(a.Since), Message: a.Message}
	}
	return views
}
