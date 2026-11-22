// Package demo is the made-up machine that `loadline serve --demo` shows:
// three accounts and five sessions that between them cover every state,
// limit, projection and alert the dashboard draws. The scenario is a list of
// the same observations the real feeds produce, so the real tracker turns it
// into the view. Every name, address and path in it is invented.
package demo

import (
	"cmp"
	"slices"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	dirMain domain.ConfigDir = "C:/Users/example/.claude"
	dirB    domain.ConfigDir = "C:/Users/example/.claude-b"
	dirAPI  domain.ConfigDir = "C:/Users/example/.claude-work"

	opus   = "Opus 5.5"
	sonnet = "Sonnet 5.5"
	haiku  = "Haiku 5.5"

	contextSize = 200_000
	projects    = "C:/Users/example/projects/"
)

// Budgets returns the daily budget, in US dollars, of each scenario account
// that has one.
func Budgets() map[domain.ConfigDir]float64 {
	return map[domain.ConfigDir]float64{dirAPI: 25}
}

// Scenario returns the scenario's accounts, session events and model calls as
// of now, each list in time order. It is deterministic: the same now gives the
// same scenario. Seed a tracker with SetAccount for each account, then Apply
// each event, then RecordAPI each call.
//
// Main is an Ultra subscription at 93% of its weekly limit and on pace to run
// out before the week resets; B is a subscription with a 5-hour window as
// well, comfortably on pace; API is billed per token, under its daily budget
// with spend spread over the past fortnight. The sessions are one waiting on
// a permission prompt, one working with two subagents, one compacting, one
// idle and one parked background session.
func Scenario(now time.Time) (accounts []domain.Account, events []domain.Event, api []domain.APIRequest) {
	s := script{now: now, midnight: startOfDay(now)}
	s.copperLantern()
	s.harborAPI()
	s.tidewater()
	s.quietLedger()
	s.dependencyAudit()
	return s.accounts(), s.sortedEvents(), s.sortedCalls()
}

func startOfDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

// script collects the scenario as it is written, each moment given as how
// long before now it happened.
type script struct {
	now, midnight time.Time
	events        []timedEvent
	calls         []domain.APIRequest
}

type timedEvent struct {
	at    time.Time
	event domain.Event
}

// session is what the scenario says about one session beyond its events.
type session struct {
	id    domain.SessionID
	dir   domain.ConfigDir
	pid   int
	name  string
	email string

	model, effort            string
	contextUsed              int64
	linesAdded, linesRemoved int64
	cacheHitRatio            float64
}

func (s *script) ago(d time.Duration) time.Time { return s.now.Add(-d) }

// today is d before now, or the start of today when that is earlier, so that
// the calls of the last hours stay on today however early in the day now is.
func (s *script) today(d time.Duration) time.Time {
	if at := s.ago(d); at.After(s.midnight) {
		return at
	}
	return s.midnight
}

func (s *script) add(at time.Time, e domain.Event) {
	s.events = append(s.events, timedEvent{at: at, event: e})
}

func (s *script) hook(x session, d time.Duration, h domain.Hook) {
	h.At, h.Dir, h.Session, h.PID = s.ago(d), x.dir, x.id, x.pid
	s.add(h.At, h)
}

func (s *script) start(x session, d time.Duration) {
	s.hook(x, d, domain.Hook{Name: "SessionStart", Source: "startup"})
}

func (s *script) prompt(x session, d time.Duration) {
	s.hook(x, d, domain.Hook{Name: "UserPromptSubmit"})
}

func (s *script) stop(x session, d time.Duration) {
	s.hook(x, d, domain.Hook{Name: "Stop"})
}

// status adds a status line for x at d before now, reporting its account's
// rate limits when limits is not nil. Every line carries the session's final
// figures: only the newest line counts.
func (s *script) status(x session, d time.Duration, costUSD float64, limits *domain.Limits) {
	st := domain.Status{
		At:         s.ago(d),
		Dir:        x.dir,
		Session:    x.id,
		Name:       x.name,
		ProjectDir: projects + x.name,
		Metrics: domain.Metrics{
			Model:         x.model,
			Effort:        x.effort,
			ContextPct:    100 * float64(x.contextUsed) / contextSize,
			ContextSize:   contextSize,
			ContextUsed:   x.contextUsed,
			CostUSD:       costUSD,
			DurationMS:    d.Milliseconds(),
			LinesAdded:    x.linesAdded,
			LinesRemoved:  x.linesRemoved,
			CacheWarm:     true,
			CacheHitRatio: x.cacheHitRatio,
		},
	}
	if limits != nil {
		st.Limits = *limits
		st.Limits.ObservedAt = st.At
	}
	s.add(st.At, st)
}

// variation spreads the cost of a run of calls around its typical cost.
var variation = []float64{0.85, 1.1, 0.95, 1.2, 0.9}

// tokensPerUSD sets how many tokens a dollar buys from each model, so the
// cheaper models show more tokens for the same spend.
var tokensPerUSD = map[string]float64{opus: 1, sonnet: 5, haiku: 15}

// callsAt adds a call from x to model at each of times, of typical cost
// costUSD, and returns what they cost together.
func (s *script) callsAt(x session, model string, costUSD float64, times []time.Time) float64 {
	var total float64
	for i, at := range times {
		cost := costUSD * variation[i%len(variation)]
		tokens := cost * tokensPerUSD[model]
		s.calls = append(s.calls, domain.APIRequest{
			At:         at,
			Session:    x.id,
			Email:      x.email,
			Model:      model,
			Input:      int64(tokens * 3_000),
			Output:     int64(tokens * 1_100),
			CacheRead:  int64(tokens * 52_000),
			CacheWrite: int64(tokens * 4_500),
			CostUSD:    cost,
			DurationMS: int64(2_000 + 700*(i%5)),
		})
		total += cost
	}
	return total
}

// callsToday adds calls from x to model every interval between from and to
// before now.
func (s *script) callsToday(x session, model string, costUSD float64, from, to, every time.Duration) float64 {
	var times []time.Time
	for d := from; d >= to; d -= every {
		times = append(times, s.today(d))
	}
	return s.callsAt(x, model, costUSD, times)
}

// callsOnDay adds count calls from x to model, every interval from the given
// time of day, days before today.
func (s *script) callsOnDay(x session, model string, costUSD float64, days int, from, every time.Duration, count int) float64 {
	start := s.midnight.AddDate(0, 0, -days).Add(from)
	times := make([]time.Time, count)
	for i := range times {
		times[i] = start.Add(time.Duration(i) * every)
	}
	return s.callsAt(x, model, costUSD, times)
}

func (s *script) accounts() []domain.Account {
	probed := s.ago(time.Hour)
	return []domain.Account{
		{Dir: dirMain, Label: "Main", Email: "dev@example.com", Plan: "Ultra", Auth: domain.AuthSubscription, ProbedAt: probed},
		{Dir: dirB, Label: "B", Email: "dev.b@example.com", Plan: "Max 20x", Auth: domain.AuthSubscription, ProbedAt: probed},
		{Dir: dirAPI, Label: "API", Auth: domain.AuthAPIKey, ProbedAt: probed},
	}
}

// copperLantern has waited on a permission prompt for over a minute, after
// an earlier one was approved.
func (s *script) copperLantern() {
	x := session{
		id: "6d1f0c2a-41b7-4e5d-9a83-2f7c1e0b9d41", dir: dirMain, pid: 18244, name: "copper-lantern", email: "dev@example.com",
		model: opus, effort: "xhigh", contextUsed: 62_000, linesAdded: 148, linesRemoved: 37, cacheHitRatio: 0.94,
	}
	cost := s.callsToday(x, opus, 0.62, 44*time.Minute, 2*time.Minute, 3*time.Minute)

	s.start(x, 46*time.Minute)
	s.status(x, 46*time.Minute-5*time.Second, cost, nil)
	s.prompt(x, 45*time.Minute)
	s.hook(x, 21*time.Minute, domain.Hook{Name: "PermissionRequest", ToolName: "Bash"})
	s.hook(x, 20*time.Minute, domain.Hook{Name: "PostToolUse", ToolName: "Bash"})
	s.status(x, 80*time.Second, cost, nil)
	s.hook(x, 72*time.Second, domain.Hook{Name: "PermissionRequest", ToolName: "Edit"})
}

// harborAPI is working with two subagents, and its status lines carry Main's
// weekly usage, climbing about 2% an hour.
func (s *script) harborAPI() {
	x := session{
		id: "a3e94b70-0c5d-4f18-b6a2-8d0e7f3c5b12", dir: dirMain, pid: 20412, name: "harbor-api", email: "dev@example.com",
		model: opus, effort: "high", contextUsed: 124_000, linesAdded: 512, linesRemoved: 96, cacheHitRatio: 0.91,
	}
	cost := s.callsToday(x, opus, 0.45, 109*time.Minute, time.Minute, 4*time.Minute) +
		s.callsToday(x, haiku, 0.04, 7*time.Minute, time.Minute, time.Minute)

	s.start(x, 112*time.Minute)
	s.status(x, 112*time.Minute-5*time.Second, cost, nil)
	s.prompt(x, 110*time.Minute)
	resetsAt := s.now.Add(50 * time.Hour).Truncate(time.Hour)
	for _, sample := range []struct {
		ago  time.Duration
		used float64
	}{
		{100 * time.Minute, 89.5},
		{80 * time.Minute, 90.2},
		{60 * time.Minute, 90.9},
		{40 * time.Minute, 91.6},
		{20 * time.Minute, 92.3},
		{2 * time.Minute, 93},
	} {
		s.status(x, sample.ago, cost, &domain.Limits{SevenDay: &domain.Window{UsedPct: sample.used, ResetsAt: resetsAt}})
	}
	s.stop(x, 41*time.Minute)
	s.prompt(x, 39*time.Minute)
	s.hook(x, 7*time.Minute, domain.Hook{Name: "SubagentStart", AgentID: "agent-explore-1"})
	s.hook(x, 5*time.Minute, domain.Hook{Name: "SubagentStart", AgentID: "agent-review-2"})
}

// tidewater is compacting a nearly full context, and its status lines carry
// both of B's windows.
func (s *script) tidewater() {
	x := session{
		id: "c7b2d915-6e3a-4a0f-8c47-1b9e5d2f6a83", dir: dirB, pid: 9120, name: "tidewater", email: "dev.b@example.com",
		model: sonnet, effort: "medium", contextUsed: 174_000, linesAdded: 233, linesRemoved: 180, cacheHitRatio: 0.88,
	}
	cost := s.callsToday(x, sonnet, 0.16, 69*time.Minute, time.Minute, 5*time.Minute)

	s.start(x, 71*time.Minute)
	s.status(x, 71*time.Minute-5*time.Second, cost, nil)
	s.prompt(x, 70*time.Minute)
	weekResets := s.now.Add(4 * 24 * time.Hour).Truncate(time.Hour)
	fiveHourResets := s.now.Add(130 * time.Minute).Truncate(10 * time.Minute)
	for _, sample := range []struct {
		ago            time.Duration
		week, fiveHour float64
	}{
		{65 * time.Minute, 3.6, 30},
		{50 * time.Minute, 3.7, 32},
		{35 * time.Minute, 3.8, 34},
		{20 * time.Minute, 3.9, 36},
		{5 * time.Minute, 4, 38},
	} {
		s.status(x, sample.ago, cost, &domain.Limits{
			SevenDay: &domain.Window{UsedPct: sample.week, ResetsAt: weekResets},
			FiveHour: &domain.Window{UsedPct: sample.fiveHour, ResetsAt: fiveHourResets},
		})
	}
	s.stop(x, 30*time.Minute)
	s.prompt(x, 28*time.Minute)
	s.hook(x, 40*time.Second, domain.Hook{Name: "PreCompact"})
}

// quietLedger finished its turn a few minutes ago.
func (s *script) quietLedger() {
	x := session{
		id: "e0f5a8c3-9b21-4d76-a5e8-3c4b7f1d2e94", dir: dirB, pid: 15532, name: "quiet-ledger", email: "dev.b@example.com",
		model: sonnet, contextUsed: 16_000, linesAdded: 12, linesRemoved: 2, cacheHitRatio: 0.97,
	}
	cost := s.callsToday(x, sonnet, 0.09, 29*time.Minute, 5*time.Minute, 4*time.Minute) +
		s.callsToday(x, haiku, 0.02, 28*time.Minute, 20*time.Minute, 4*time.Minute)

	s.start(x, 31*time.Minute)
	s.status(x, 31*time.Minute-5*time.Second, cost, nil)
	s.prompt(x, 30*time.Minute)
	s.status(x, 4*time.Minute+10*time.Second, cost, nil)
	s.stop(x, 4*time.Minute)
}

// dependencyAudit is a background session that runs in the small hours on
// most days, billed to the API key; today's run finished twenty minutes ago.
func (s *script) dependencyAudit() {
	x := session{
		id: "f19c3e6b-2a84-4b0d-97f5-6e8a1c3d5b27", dir: dirAPI, pid: 7311, name: "dependency-audit",
		model: haiku, contextUsed: 22_000, linesAdded: 64, linesRemoved: 58, cacheHitRatio: 0.83,
	}
	var cost float64
	for _, days := range []int{12, 9, 6, 5, 3, 2, 1} {
		cost += s.callsOnDay(x, haiku, 0.08, days, 2*time.Hour, 2*time.Minute, 20) +
			s.callsOnDay(x, sonnet, 0.62, days, 2*time.Hour+time.Minute, 4*time.Minute, 10)
	}
	cost += s.callsToday(x, haiku, 0.08, 51*time.Minute, 22*time.Minute, time.Minute) +
		s.callsToday(x, sonnet, 0.62, 51*time.Minute, 22*time.Minute, 3*time.Minute)

	startedAt := s.midnight.AddDate(0, 0, -12).Add(2 * time.Hour)
	poll := domain.Poll{
		Dir: x.dir, Session: x.id, Kind: domain.KindBackground, Name: x.name, Cwd: projects + x.name, StartedAt: startedAt,
	}
	working, parked := poll, poll
	working.At, working.PID, working.Status, working.BgState = s.ago(52*time.Minute), x.pid, "busy", "working"
	parked.At, parked.BgState = s.ago(21*time.Minute), "done"
	s.add(working.At, working)
	s.status(x, 50*time.Minute, cost, nil)
	s.add(parked.At, parked)
}

// sortedEvents returns the events oldest first; those at the same moment
// keep the order they were written in.
func (s *script) sortedEvents() []domain.Event {
	slices.SortStableFunc(s.events, func(a, b timedEvent) int { return a.at.Compare(b.at) })
	events := make([]domain.Event, len(s.events))
	for i, e := range s.events {
		events[i] = e.event
	}
	return events
}

// sortedCalls returns the model calls oldest first, then by session.
func (s *script) sortedCalls() []domain.APIRequest {
	slices.SortStableFunc(s.calls, func(a, b domain.APIRequest) int {
		return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.Session, b.Session))
	})
	return s.calls
}
