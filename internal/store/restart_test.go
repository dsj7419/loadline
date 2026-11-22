package store_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/store"
	"github.com/dsj7419/loadline/internal/tracker"
)

const days = 86400

// manualClock is a clock the test sets.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// heldTicker never runs what it is given: the tests take each view they read.
type heldTicker struct{}

func (heldTicker) AfterFunc(time.Duration, func()) func() bool { return func() bool { return true } }

// creditsRun is one stretch of an account's usage, as a tracker sees it.
type creditsRun struct {
	grants []domain.CreditGrant
	// play drives the tracker, from the account's sign-in to just before
	// the restart.
	play func(c *manualClock, tr *tracker.Tracker)
	// check holds the tracker that never restarted, under a thirty-day
	// retention, to what the scenario says it reports, so that two trackers
	// wrong alike cannot pass.
	check func(t *testing.T, credits *tracker.CreditsView)
}

// newCreditsTracker is a tracker as the server sets one up for a history
// that keeps retentionDays.
func newCreditsTracker(t *testing.T, c *manualClock, grants []domain.CreditGrant, retentionDays int, rec tracker.Recorder) *tracker.Tracker {
	t.Helper()
	tr := tracker.New(c.Now, tracker.Config{
		Credits:    map[domain.ConfigDir][]domain.CreditGrant{dirA: grants},
		CreditDays: retentionDays,
		Alerts:     domain.AlertConfig{WeeklyPct: 90, ContextPct: 85, NeedsYouAfter: 20 * time.Second},
		Location:   time.UTC,
		Ticker:     heldTicker{},
	}, rec)
	t.Cleanup(tr.Close)
	return tr
}

// signIn starts the scenario at sec with dirA signed in to a subscription,
// the only kind of account that has usage credits.
func signIn(c *manualClock, tr *tracker.Tracker, sec int) {
	c.Set(at(sec))
	tr.SetAccount(domain.Account{Dir: dirA, Auth: domain.AuthSubscription})
}

func limitsLine(sec int, sevenDay *domain.Window) domain.Status {
	return domain.Status{At: at(sec), Dir: dirA, Session: s1, Limits: domain.Limits{SevenDay: sevenDay, ObservedAt: at(sec)}}
}

func callAt(sec int, costUSD float64) domain.APIRequest {
	return domain.APIRequest{At: at(sec), Session: s1, Model: opus, Input: 100, Output: 50, CostUSD: costUSD}
}

// creditsState is what the page shows of dirA's credits: the account's
// credits and the credit alerts.
func creditsState(t *testing.T, v tracker.View) (credits *tracker.CreditsView, page string) {
	t.Helper()
	var alerts []tracker.AlertView
	for _, a := range v.Alerts {
		if a.Kind == domain.AlertCreditsActive || a.Kind == domain.AlertCreditsLow {
			alerts = append(alerts, a)
		}
	}
	for _, a := range v.Accounts {
		if a.Dir == dirA {
			credits = a.Credits
		}
	}
	data, err := json.Marshal(struct {
		Credits *tracker.CreditsView `json:"credits"`
		Alerts  []tracker.AlertView  `json:"alerts"`
	}{credits, alerts})
	if err != nil {
		t.Fatalf("encode the credits: %v", err)
	}
	return credits, string(data)
}

// A span on credits that opened before the days the history keeps, in the
// reviewer's case, and a long stretch at a limit whose reset is unknown.
var creditsRuns = map[string]creditsRun{
	"a span opened before the days kept holds a call made within them": {
		grants: []domain.CreditGrant{{USD: 20, AddedAt: at(-40 * days)}},
		play: func(c *manualClock, tr *tracker.Tracker) {
			signIn(c, tr, -31*days)
			tr.Apply(limitsLine(-31*days, &domain.Window{UsedPct: 100, ResetsAt: at(-24 * days)}))
			c.Set(at(-29 * days))
			tr.RecordAPI(callAt(-29*days, 5))
			tr.Snapshot()
			c.Set(at(-23 * days))
			tr.Apply(limitsLine(-23*days, &domain.Window{UsedPct: 40, ResetsAt: at(-17 * days)}))
			tr.Snapshot()
		},
		check: func(t *testing.T, credits *tracker.CreditsView) {
			t.Helper()
			if credits == nil || credits.SpentUSD != 5 || credits.RemainingUSD != 15 {
				t.Errorf("credits %+v, want the $5 spent 29 days ago still counted", credits)
			}
		},
	},
	"a long stretch at a limit with no known reset keeps its start": {
		grants: []domain.CreditGrant{{USD: 100, AddedAt: at(-40 * days)}},
		play: func(c *manualClock, tr *tracker.Tracker) {
			signIn(c, tr, -31*days)
			for d := -31; d <= 0; d++ {
				c.Set(at(d * days))
				tr.Apply(limitsLine(d*days, &domain.Window{UsedPct: 100}))
				tr.RecordAPI(callAt(d*days+10, 1))
				tr.Snapshot()
			}
		},
		check: func(t *testing.T, credits *tracker.CreditsView) {
			t.Helper()
			if credits == nil || !credits.Active || !time.Time(credits.OnCreditsSince).Equal(at(-31*days)) || credits.SpentUSD != 30 {
				t.Errorf("credits %+v, want $30 spent over the 30 days kept, on credits since %v", credits, at(-31*days))
			}
		},
	},
}

// restartAt is when the server restarts in these tests.
var restartAt = at(60)

// restart plays run on a tracker that records to a new history keeping
// retentionDays, lets prune have the history as the downtime left it, and
// restarts as the server does: it restores a new tracker from what the
// history loads at restartAt. It returns the credits the tracker that ran on
// shows at restartAt, and what the page shows of them from both trackers.
func restart(t *testing.T, run creditsRun, retentionDays int, prune func(t *testing.T, s *store.SQLite, since time.Time)) (ranOn *tracker.CreditsView, ranOnPage, restartedPage string) {
	t.Helper()
	s, _ := openStore(t)
	rec, _ := newRecorder(t, s)
	clock := &manualClock{}
	live := newCreditsTracker(t, clock, run.grants, retentionDays, rec)
	run.play(clock, live)
	closeRecorder(t, rec)
	if rec.Dropped() != 0 || rec.Errors() != 0 {
		t.Fatalf("the history missed %d changes and lost %d", rec.Dropped(), rec.Errors())
	}
	clock.Set(restartAt)
	ranOn, ranOnPage = creditsState(t, live.Snapshot())

	since := restartAt.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	prune(t, s, since)
	restarted := newCreditsTracker(t, clock, run.grants, retentionDays, nil)
	restarted.Restore(load(t, s, since))
	_, restartedPage = creditsState(t, restarted.Snapshot())
	return ranOn, ranOnPage, restartedPage
}

// A tracker restored from the history after a restart holds the spans, the
// spend and the time on credits that the one that ran on holds, whether the
// history was pruned at the restart, a day before it, or not since it was
// last written.
func TestRestart_RebuildsTheCreditsTheTrackerHeld(t *testing.T) {
	histories := map[string]func(t *testing.T, s *store.SQLite, since time.Time){
		"pruned at the restart": func(t *testing.T, s *store.SQLite, since time.Time) {
			t.Helper()
			must(t, s.Prune(t.Context(), since))
		},
		"pruned a day before": func(t *testing.T, s *store.SQLite, since time.Time) {
			t.Helper()
			must(t, s.Prune(t.Context(), since.Add(-24*time.Hour)))
		},
		"not pruned since": func(*testing.T, *store.SQLite, time.Time) {},
	}
	for name, run := range creditsRuns {
		for history, prune := range histories {
			t.Run(name+", "+history, func(t *testing.T) {
				ranOn, want, got := restart(t, run, 30, prune)

				run.check(t, ranOn)
				if got != want {
					t.Errorf("after the restart\n got %s\nwant %s, as the tracker that ran on has it", got, want)
				}
			})
		}
	}
}

// A history kept for fewer than thirty days holds fewer days of spend, and
// the tracker counts no more than it, so a restart changes nothing.
func TestRestart_CreditsCountTheDaysTheHistoryKeeps(t *testing.T) {
	run := creditsRuns["a long stretch at a limit with no known reset keeps its start"]

	ranOn, want, got := restart(t, run, 7, func(t *testing.T, s *store.SQLite, since time.Time) {
		t.Helper()
		must(t, s.Prune(t.Context(), since))
	})

	if ranOn == nil || ranOn.SpentUSD != 7 || ranOn.Days != 7 || !time.Time(ranOn.OnCreditsSince).Equal(at(-31*days)) {
		t.Errorf("credits %+v, want $7 spent over the 7 days kept, on credits since %v", ranOn, at(-31*days))
	}
	if got != want {
		t.Errorf("after the restart\n got %s\nwant %s, as the tracker that ran on has it", got, want)
	}
}
