package tracker_test

import (
	"testing"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// The scenarios here are those whose views must stay cheap however long they
// run: the tracker holds its lock while it builds a view, so a slow view
// stalls ingest. The tests that bound a view's cost and the benchmarks that
// measure it share them.

// aThousandDollars is a grant, added the day before t0, that a day of calls
// at a cent each leaves far from spent.
var aThousandDollars = map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 1000, AddedAt: at(-86400)}}}

// aBillionDollars is a grant, added before the credit history begins, that no
// month of calls spends.
var aBillionDollars = map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 1e9, AddedAt: at(-40 * 86400)}}}

// disagreeFor feeds the tracker hours of two sessions of one account reading
// its weekly window differently, as in
// TestTracker_AnIdleSessionsOlderReadingKeepsTheStretchOnCredits: s1 works on
// credits, its status line reporting the window at 100% every 5 s, while s2
// sits idle and goes on reporting the 97% its own last response carried. The
// tracker is viewed once an hour meanwhile, and the clock left a second past
// the end.
func disagreeFor(tr *tracker.Tracker, clock *fakeClock, hours int) {
	for sec := 0; sec < hours*3600; sec += 5 {
		clock.Set(at(sec))
		tr.Apply(withWindows(statusLine(sec, s1), nil, window(100, weekResets)))
		tr.RecordAPI(request(at(sec+1), s1, "Opus 5.5", 0.01))
		clock.Set(at(sec + 2))
		tr.Apply(withWindows(statusLine(sec+2, s2), nil, window(97, weekResets)))
		tr.RecordAPI(request(at(sec+3), s1, "Opus 5.5", 0.01))
		if sec%3600 == 0 {
			tr.Snapshot()
		}
	}
	clock.Set(at(hours*3600 + 1))
}

// restoreStretchesOnCredits restores days of stretches on credits up to t0,
// half a minute long and one every minute, each with a one-dollar call in
// it, and returns how many stretches there are.
func restoreStretchesOnCredits(tr *tracker.Tracker, days int) int {
	var samples []domain.Sample
	var usage []tracker.UsageRow
	for sec := -days*86400 + 60; sec < 0; sec += 60 {
		samples = append(samples,
			domain.Sample{At: at(sec), UsedPct: 100, ResetsAt: weekResets},
			domain.Sample{At: at(sec + 30), UsedPct: 0, ResetsAt: weekResets})
		usage = append(usage, tracker.UsageRow{Minute: at(sec), Dir: dirA, Session: s1, Model: "Opus 5.5", CostUSD: 1, Requests: 1})
	}
	tr.Restore(tracker.Restored{Samples: map[domain.ConfigDir]domain.WindowSamples{dirA: {SevenDay: samples}}, Usage: usage})
	return len(usage)
}

// viewing returns a view of tr, as an operation for costtest to time.
func viewing(tr *tracker.Tracker) func() {
	return func() { tr.Snapshot() }
}

// newBenchTracker returns the tracker newSubscribedHarness does, for a
// benchmark.
func newBenchTracker(b *testing.B, credits map[domain.ConfigDir][]domain.CreditGrant) (*tracker.Tracker, *fakeClock) {
	b.Helper()
	clock := &fakeClock{now: t0}
	cfg := tracker.Config{Credits: credits, Alerts: defaultAlerts, Location: testZone, Ticker: &fakeTicker{}}
	tr := tracker.New(clock.Now, cfg, &fakeRecorder{})
	b.Cleanup(tr.Close)
	tr.SetAccount(domain.Account{Dir: dirA, Auth: domain.AuthSubscription})
	return tr, clock
}

// BenchmarkTracker_ViewAfterADayOfIdleReadings measures a view after a day of
// an idle session's readings disagreeing with a busy one's.
func BenchmarkTracker_ViewAfterADayOfIdleReadings(b *testing.B) {
	tr, clock := newBenchTracker(b, aThousandDollars)
	disagreeFor(tr, clock, 24)
	b.ReportAllocs()

	for b.Loop() {
		tr.Snapshot()
	}
}

// BenchmarkTracker_ViewOverManySpansAndCalls measures a view over a month of
// stretches on credits, each with a call in it.
func BenchmarkTracker_ViewOverManySpansAndCalls(b *testing.B) {
	tr, _ := newBenchTracker(b, aBillionDollars)
	restoreStretchesOnCredits(tr, 30)
	tr.Snapshot()
	b.ReportAllocs()

	for b.Loop() {
		tr.Snapshot()
	}
}
