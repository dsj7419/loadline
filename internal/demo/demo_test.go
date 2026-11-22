package demo_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/demo"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// now is a Saturday evening.
var now = time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)

var alertConfig = domain.AlertConfig{WeeklyPct: 90, ContextPct: 85, NeedsYouAfter: 20 * time.Second}

// seeded feeds the scenario as of at through a real tracker, the way
// `serve --demo` does, and returns the view the page would get.
func seeded(t *testing.T, at time.Time) tracker.View {
	t.Helper()
	accounts, events, api := demo.Scenario(at)
	tr := tracker.New(func() time.Time { return at }, tracker.Config{
		Budgets:  demo.Budgets(),
		Credits:  demo.Credits(at),
		Alerts:   alertConfig,
		Location: at.Location(),
	}, nil)
	t.Cleanup(tr.Close)
	for _, a := range accounts {
		tr.SetAccount(a)
	}
	for _, e := range events {
		tr.Apply(e)
	}
	for _, r := range api {
		tr.RecordAPI(r)
	}
	return tr.Snapshot()
}

func accountByLabel(t *testing.T, v tracker.View, label string) tracker.AccountView {
	t.Helper()
	for _, a := range v.Accounts {
		if a.Label == label {
			return a
		}
	}
	t.Fatalf("no account labeled %q in %+v", label, v.Accounts)
	return tracker.AccountView{}
}

func sessionInState(t *testing.T, v tracker.View, state domain.State) tracker.SessionView {
	t.Helper()
	for _, s := range v.Sessions {
		if s.State == state {
			return s
		}
	}
	t.Fatalf("no %s session in the view", state)
	return tracker.SessionView{}
}

func modelNames(totals []tracker.ModelTotal) []string {
	names := make([]string, 0, len(totals))
	for _, m := range totals {
		names = append(names, m.Model)
	}
	slices.Sort(names)
	return names
}

func TestScenario_Deterministic(t *testing.T) {
	accounts1, events1, api1 := demo.Scenario(now)
	accounts2, events2, api2 := demo.Scenario(now)

	if len(accounts1) == 0 || len(events1) == 0 || len(api1) == 0 {
		t.Fatalf("Scenario gave %d accounts, %d events and %d requests; want some of each", len(accounts1), len(events1), len(api1))
	}
	if !reflect.DeepEqual(accounts1, accounts2) {
		t.Errorf("accounts differ between calls:\n%+v\n%+v", accounts1, accounts2)
	}
	if !reflect.DeepEqual(events1, events2) {
		t.Errorf("events differ between calls")
	}
	if !reflect.DeepEqual(api1, api2) {
		t.Errorf("requests differ between calls")
	}
	if !reflect.DeepEqual(seeded(t, now), seeded(t, now)) {
		t.Errorf("the seeded views differ")
	}
}

func TestScenario_ProducesAllStates(t *testing.T) {
	v := seeded(t, now)

	states := make(map[domain.State]bool)
	for _, s := range v.Sessions {
		states[s.State] = true
	}
	for _, want := range []domain.State{
		domain.StateNeedsYou, domain.StateWorking, domain.StateCompacting, domain.StateIdle, domain.StateParked,
	} {
		if !states[want] {
			t.Errorf("no %s session; states %v", want, states)
		}
	}
	if len(v.Sessions) != 5 {
		t.Errorf("view lists %d sessions, want 5", len(v.Sessions))
	}
}

func TestScenario_MainReachedItsWeeklyLimitAndWorksOnCredits(t *testing.T) {
	main := accountByLabel(t, seeded(t, now), "Main")

	if main.Auth != domain.AuthSubscription || main.Plan != "Ultra" {
		t.Errorf("Main is %s on plan %q, want a subscription on Ultra", main.Auth, main.Plan)
	}
	if main.FiveHour != nil {
		t.Errorf("Main has a 5-hour window %+v, want none", main.FiveHour)
	}
	week := main.SevenDay
	if week == nil || week.UsedPct != 100 {
		t.Fatalf("Main's weekly window is %+v, want 100%% used", week)
	}
	if week.Projection == nil || !week.Projection.RunsOutBeforeReset || week.Projection.RatePerHour <= 0 {
		t.Errorf("Main's weekly projection is %+v, want a rising rate that ran out before the reset", week.Projection)
	}
	credits := main.Credits
	if credits == nil || !credits.Active || credits.Source != domain.CreditSourceEstimate || credits.BalanceUSD != 20 {
		t.Fatalf("Main's credits are %+v, want an estimate of a $20 balance it is spending now", credits)
	}
	if since := time.Time(credits.OnCreditsSince); !since.Equal(now.Add(-18 * time.Minute)) {
		t.Errorf("Main has been on credits since %v, want since its weekly window filled 18 minutes ago", since)
	}
	// About $6.20 of calls since.
	if credits.SpentUSD < 6 || credits.SpentUSD > 6.5 || credits.RemainingUSD != credits.BalanceUSD-credits.SpentUSD {
		t.Errorf("Main has spent $%v and has $%v left, want about $6.20 of $20 spent", credits.SpentUSD, credits.RemainingUSD)
	}
	for _, a := range []string{"B", "API"} {
		if c := accountByLabel(t, seeded(t, now), a).Credits; c != nil {
			t.Errorf("%s has credits %+v, want none", a, c)
		}
	}
}

// Just after midnight the scenario dates the calls of the hours before at the
// start of the day, and must not count them as spent on credits.
func TestScenario_CreditsStayCoherentJustAfterMidnight(t *testing.T) {
	tokyo := time.FixedZone("Tokyo", 9*3600)
	at := time.Date(2026, 11, 22, 0, 5, 0, 0, tokyo)

	credits := accountByLabel(t, seeded(t, at), "Main").Credits

	if credits == nil || !credits.Active || credits.SpentUSD <= 0 || credits.SpentUSD > 6.5 {
		t.Errorf("Main's credits at %v are %+v, want on credits with no more than the last minutes' calls spent", at, credits)
	}
}

func TestScenario_BShowsBothWindowsOnPace(t *testing.T) {
	b := accountByLabel(t, seeded(t, now), "B")

	if b.Auth != domain.AuthSubscription {
		t.Errorf("B is %s, want a subscription", b.Auth)
	}
	if b.SevenDay == nil || b.SevenDay.UsedPct != 4 {
		t.Errorf("B's weekly window is %+v, want 4%% used", b.SevenDay)
	}
	if b.FiveHour == nil {
		t.Fatal("B has no 5-hour window")
	}
	for name, w := range map[string]*tracker.WindowView{"weekly": b.SevenDay, "5-hour": b.FiveHour} {
		if w == nil || w.Projection == nil || w.Projection.RunsOutBeforeReset {
			t.Errorf("B's %s window is %+v, want a projection that stays under the limit", name, w)
		}
	}
}

// The page colors a pace by band, and the demo shows a window in each,
// whenever in the hour, and whatever the day, it starts.
func TestScenario_ShowsAPaceInEachBand(t *testing.T) {
	tokyo := time.FixedZone("Tokyo", 9*3600)
	for _, at := range []time.Time{now, now.Add(9 * time.Minute), now.Add(59 * time.Minute), time.Date(2026, 11, 22, 0, 5, 0, 0, tokyo)} {
		bands := make(map[string][]float64)
		for _, a := range seeded(t, at).Accounts {
			for _, w := range []*tracker.WindowView{a.FiveHour, a.SevenDay} {
				switch {
				case w == nil || w.Pace == nil:
				case *w.Pace < 0.9:
					bands["below 0.9"] = append(bands["below 0.9"], *w.Pace)
				case *w.Pace <= 1.2:
					bands["0.9 to 1.2"] = append(bands["0.9 to 1.2"], *w.Pace)
				default:
					bands["above 1.2"] = append(bands["above 1.2"], *w.Pace)
				}
			}
		}
		for _, band := range []string{"below 0.9", "0.9 to 1.2", "above 1.2"} {
			if len(bands[band]) == 0 {
				t.Errorf("at %v no window has a pace %s; the paces are %v", at, band, bands)
			}
		}
	}
}

func TestScenario_APIAccountSpendsAcrossDays(t *testing.T) {
	api := accountByLabel(t, seeded(t, now), "API")

	if api.Auth != domain.AuthAPIKey {
		t.Errorf("API is %s, want an API key", api.Auth)
	}
	if api.BudgetUSD <= 0 {
		t.Errorf("API has no daily budget")
	}
	if !(0 < api.SpendTodayUSD && api.SpendTodayUSD < api.SpendWeekUSD && api.SpendWeekUSD < api.SpendMonthUSD) {
		t.Errorf("API spend today %v, week %v, month %v; want each period to add to the one before",
			api.SpendTodayUSD, api.SpendWeekUSD, api.SpendMonthUSD)
	}
	if api.SpendTodayUSD >= api.BudgetUSD {
		t.Errorf("API spent %v of its %v budget today; want it under budget", api.SpendTodayUSD, api.BudgetUSD)
	}
}

func TestScenario_SessionsShowWhatThePageDraws(t *testing.T) {
	v := seeded(t, now)

	waiting := sessionInState(t, v, domain.StateNeedsYou)
	if waiting.WaitReason != "permission prompt" {
		t.Errorf("the waiting session waits for %q, want a permission prompt", waiting.WaitReason)
	}
	if wait := now.Sub(waiting.StateAt); wait <= alertConfig.NeedsYouAfter {
		t.Errorf("the waiting session has waited %v, want longer than the %v alert threshold", wait, alertConfig.NeedsYouAfter)
	}
	working := sessionInState(t, v, domain.StateWorking)
	if working.Subagents != 2 {
		t.Errorf("the working session runs %d subagents, want 2", working.Subagents)
	}
	if working.Share <= 0 || working.Share >= 1 {
		t.Errorf("the working session's share is %v, want part of its account's spend", working.Share)
	}
	if parked := sessionInState(t, v, domain.StateParked); parked.Kind != domain.KindBackground {
		t.Errorf("the parked session is %s, want background", parked.Kind)
	}

	for _, s := range v.Sessions {
		if s.Name == "" || s.Model == "" || s.ContextLeft <= 0 || s.CostUSD <= 0 || len(s.Models) == 0 {
			t.Errorf("session %s lacks a figure the row shows: %+v", s.ID, s)
		}
		if len(s.Timeline) < 2 {
			t.Errorf("session %s has %d state changes, want a few", s.Name, len(s.Timeline))
		}
		if !strings.HasPrefix(s.Project, "C:/Users/example/projects/") {
			t.Errorf("session %s is in %q, want a made-up project", s.Name, s.Project)
		}
	}
	for _, a := range v.Accounts {
		if a.Email != "" && !strings.HasSuffix(a.Email, "@example.com") {
			t.Errorf("account %s has the address %q, want a made-up one", a.Label, a.Email)
		}
	}
}

func TestScenario_RaisesTheAlertsThatNotify(t *testing.T) {
	v := seeded(t, now)

	kinds := make(map[domain.AlertKind]bool)
	for _, a := range v.Alerts {
		kinds[a.Kind] = true
	}
	for _, want := range []domain.AlertKind{domain.AlertNeedsYou, domain.AlertRunsOut, domain.AlertCreditsActive} {
		if !kinds[want] {
			t.Errorf("no %s alert in %+v", want, v.Alerts)
		}
	}
}

func TestScenario_ModelsDifferBetweenTodayAndTheWeek(t *testing.T) {
	// Just after midnight in Tokyo, most of the scenario's recent hours fall
	// on the day before; today must still have its own usage.
	tokyo := time.FixedZone("Tokyo", 9*3600)
	for _, at := range []time.Time{now, time.Date(2026, 11, 22, 0, 5, 0, 0, tokyo)} {
		v := seeded(t, at)

		want := []string{"claude-haiku-5-5", "claude-opus-5-5", "claude-sonnet-5-5"}
		if got := modelNames(v.Models); !slices.Equal(got, want) {
			t.Errorf("at %v today's models are %v, want %v", at, got, want)
		}
		if got := modelNames(v.ModelsWeek); !slices.Equal(got, want) {
			t.Errorf("at %v the week's models are %v, want %v", at, got, want)
		}
		if reflect.DeepEqual(v.Models, v.ModelsWeek) {
			t.Errorf("at %v today's usage is the week's; want earlier days too", at)
		}
	}
}

// Claude Code names a model by its ID in model calls and by its display name
// in the status line, and the scenario must look the same.
func TestScenario_NamesModelsAsClaudeCodeDoes(t *testing.T) {
	_, events, api := demo.Scenario(now)

	for _, r := range api {
		if !strings.HasPrefix(r.Model, "claude-") {
			t.Fatalf("a model call names its model %q, want an ID", r.Model)
		}
	}
	lines := 0
	for _, e := range events {
		st, ok := e.(domain.Status)
		if !ok {
			continue
		}
		lines++
		if !strings.HasPrefix(st.Metrics.ModelID, "claude-") || strings.HasPrefix(st.Metrics.Model, "claude-") || st.Metrics.Model == "" {
			t.Fatalf("a status line names its model %q with ID %q, want a display name and an ID", st.Metrics.Model, st.Metrics.ModelID)
		}
	}
	if lines == 0 {
		t.Fatal("the scenario has no status lines")
	}
	for _, s := range seeded(t, now).Sessions {
		if strings.HasPrefix(s.Model, "claude-") {
			t.Errorf("session %s shows the model %q, want its display name", s.Name, s.Model)
		}
	}
}
