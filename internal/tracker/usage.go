package tracker

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	// keepDays is how many local days of usage the tracker holds, today
	// included; weekDays is how many the week's figures cover.
	keepDays = 30
	weekDays = 7

	// pendingFor is how long a model call waits for its session to become
	// known, and maxPending how many calls may wait at once.
	pendingFor = 10 * time.Minute
	maxPending = 10000

	secondsPerDay = 24 * 60 * 60
)

// UsageRow is the usage of one session and model within one minute.
type UsageRow struct {
	Minute                               time.Time
	Dir                                  domain.ConfigDir
	Session                              domain.SessionID
	Model                                string
	Input, Output, CacheRead, CacheWrite int64
	CostUSD                              float64
	Requests                             int
}

// civilDay numbers a calendar day, counting from 1 January 1970.
type civilDay int64

// dayOf is the calendar day t falls on in loc. Counting calendar dates rather
// than 24-hour spans keeps days whole across daylight-saving changes.
func dayOf(t time.Time, loc *time.Location) civilDay {
	year, month, day := t.In(loc).Date()
	return civilDay(time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Unix() / secondsPerDay)
}

// usage is the token counts and cost of some number of model calls.
type usage struct {
	input, output, cacheRead, cacheWrite int64
	costUSD                              float64
	requests                             int
}

func (u usage) plus(o usage) usage {
	return usage{
		input:      addCount(u.input, o.input),
		output:     addCount(u.output, o.output),
		cacheRead:  addCount(u.cacheRead, o.cacheRead),
		cacheWrite: addCount(u.cacheWrite, o.cacheWrite),
		costUSD:    addCost(u.costUSD, o.costUSD),
		requests:   addRequests(u.requests, o.requests),
	}
}

// Sums of usage saturate rather than overflow. A token count that wrapped
// around would show as negative, and a cost that reached infinity could not
// be encoded, so the view could not be served at all.

// addCount adds token counts, stopping at the largest or smallest an int64
// holds.
func addCount(a, b int64) int64 {
	switch {
	case b > 0 && a > math.MaxInt64-b:
		return math.MaxInt64
	case b < 0 && a < math.MinInt64-b:
		return math.MinInt64
	}
	return a + b
}

// addRequests adds request counts, stopping at the largest or smallest an
// int holds.
func addRequests(a, b int) int {
	switch {
	case b > 0 && a > math.MaxInt-b:
		return math.MaxInt
	case b < 0 && a < math.MinInt-b:
		return math.MinInt
	}
	return a + b
}

// addCost adds costs, stopping at the largest finite cost either way.
func addCost(a, b float64) float64 {
	sum := a + b
	if math.IsInf(sum, 0) {
		return math.Copysign(math.MaxFloat64, sum)
	}
	return sum
}

func usageOf(r domain.APIRequest) usage {
	return usage{input: r.Input, output: r.Output, cacheRead: r.CacheRead, cacheWrite: r.CacheWrite, costUSD: r.CostUSD, requests: 1}
}

func usageOfRow(r UsageRow) usage {
	return usage{input: r.Input, output: r.Output, cacheRead: r.CacheRead, cacheWrite: r.CacheWrite, costUSD: r.CostUSD, requests: r.Requests}
}

type usageKey struct {
	day     civilDay
	dir     domain.ConfigDir
	session domain.SessionID
	model   string
}

type usageRow struct {
	key   usageKey
	usage usage
}

// usageBook holds usage by local day, account, session and model, and the
// spend of the recent calls that session shares are worked out from.
//
// Rows stay in the order they were first written, and every total is summed
// in that order: floating-point addition depends on order, and a view must
// not change from one build to the next when nothing else has.
type usageBook struct {
	loc   *time.Location
	rows  []usageRow
	index map[usageKey]int
	spend []domain.Spend
}

func newUsageBook(loc *time.Location) usageBook {
	return usageBook{loc: loc, index: make(map[usageKey]int)}
}

// add records usage for a session's model on dir, at the time at.
func (b *usageBook) add(dir domain.ConfigDir, session domain.SessionID, model string, at time.Time, u usage) {
	key := usageKey{day: dayOf(at, b.loc), dir: dir, session: session, model: model}
	if i, ok := b.index[key]; ok {
		b.rows[i].usage = b.rows[i].usage.plus(u)
	} else {
		b.index[key] = len(b.rows)
		b.rows = append(b.rows, usageRow{key: key, usage: u})
	}
	b.spend = append(b.spend, domain.Spend{At: at, Session: session, Dir: dir, CostUSD: u.costUSD})
}

// prune forgets the days before firstDay and the spend before oldestSpend.
func (b *usageBook) prune(firstDay civilDay, oldestSpend time.Time) {
	b.spend = slices.DeleteFunc(b.spend, func(s domain.Spend) bool { return s.At.Before(oldestSpend) })
	kept := slices.DeleteFunc(b.rows, func(r usageRow) bool { return r.key.day < firstDay })
	if len(kept) == len(b.rows) {
		return
	}
	b.rows = kept
	clear(b.index)
	for i, r := range b.rows {
		b.index[r.key] = i
	}
}

// modelUsage is usage by model, in the order the models were first seen.
type modelUsage struct {
	models []string
	byName map[string]usage
}

func (m *modelUsage) add(model string, u usage) {
	if m.byName == nil {
		m.byName = make(map[string]usage)
	}
	current, ok := m.byName[model]
	if !ok {
		m.models = append(m.models, model)
	}
	m.byName[model] = current.plus(u)
}

// totals lists the usage by model, the most costly first and then by name.
func (m modelUsage) totals() []ModelTotal {
	out := make([]ModelTotal, 0, len(m.models))
	for _, model := range m.models {
		u := m.byName[model]
		out = append(out, ModelTotal{
			Model: model, Input: u.input, Output: u.output, CacheRead: u.cacheRead, CacheWrite: u.cacheWrite,
			CostUSD: u.costUSD, Requests: u.requests,
		})
	}
	slices.SortFunc(out, func(a, b ModelTotal) int {
		return cmp.Or(cmp.Compare(b.CostUSD, a.CostUSD), cmp.Compare(a.Model, b.Model))
	})
	return out
}

// accountUsage is one account's usage over the periods the view shows.
type accountUsage struct {
	today, week                                modelUsage
	spendTodayUSD, spendWeekUSD, spendMonthUSD float64
}

// usageSummary is the usage the view shows, as of one day.
type usageSummary struct {
	accounts    map[domain.ConfigDir]*accountUsage
	today, week modelUsage
	sessions    map[domain.SessionID]*modelUsage
}

func (s usageSummary) account(dir domain.ConfigDir) accountUsage {
	if a, ok := s.accounts[dir]; ok {
		return *a
	}
	return accountUsage{}
}

func (s usageSummary) session(id domain.SessionID) []ModelTotal {
	if m, ok := s.sessions[id]; ok {
		return m.totals()
	}
	return []ModelTotal{}
}

// summarize totals the usage as of today. The book must have been pruned to
// the days up to today, so every row it holds falls within the month. A day
// after today, which only a skewed clock can produce, counts as today.
func (b *usageBook) summarize(today civilDay) usageSummary {
	s := usageSummary{
		accounts: make(map[domain.ConfigDir]*accountUsage),
		sessions: make(map[domain.SessionID]*modelUsage),
	}
	for _, r := range b.rows {
		account, ok := s.accounts[r.key.dir]
		if !ok {
			account = &accountUsage{}
			s.accounts[r.key.dir] = account
		}
		session, ok := s.sessions[r.key.session]
		if !ok {
			session = &modelUsage{}
			s.sessions[r.key.session] = session
		}
		session.add(r.key.model, r.usage)

		account.spendMonthUSD = addCost(account.spendMonthUSD, r.usage.costUSD)
		age := today - r.key.day
		if age < weekDays {
			account.spendWeekUSD = addCost(account.spendWeekUSD, r.usage.costUSD)
			account.week.add(r.key.model, r.usage)
			s.week.add(r.key.model, r.usage)
		}
		if age <= 0 {
			account.spendTodayUSD = addCost(account.spendTodayUSD, r.usage.costUSD)
			account.today.add(r.key.model, r.usage)
			s.today.add(r.key.model, r.usage)
		}
	}
	return s
}

// pendingRequest is a model call whose session is not yet known.
type pendingRequest struct {
	request    domain.APIRequest
	receivedAt time.Time
}

// pendingRequests holds model calls until their session appears. Claude Code
// can export a call before the hook or status line that makes its session
// known arrives.
type pendingRequests struct {
	bySession map[domain.SessionID][]pendingRequest
	count     int
}

func newPendingRequests() pendingRequests {
	return pendingRequests{bySession: make(map[domain.SessionID][]pendingRequest)}
}

// hold keeps r until its session appears. When maxPending calls already wait,
// r is dropped.
func (p *pendingRequests) hold(r domain.APIRequest, receivedAt time.Time) {
	if p.count >= maxPending {
		return
	}
	p.bySession[r.Session] = append(p.bySession[r.Session], pendingRequest{request: r, receivedAt: receivedAt})
	p.count++
}

// take removes and returns the calls waiting for session that arrived at or
// after oldest.
func (p *pendingRequests) take(session domain.SessionID, oldest time.Time) []domain.APIRequest {
	waiting := p.bySession[session]
	delete(p.bySession, session)
	p.count -= len(waiting)
	var requests []domain.APIRequest
	for _, w := range waiting {
		if !w.receivedAt.Before(oldest) {
			requests = append(requests, w.request)
		}
	}
	return requests
}

// prune drops the calls that arrived before oldest.
func (p *pendingRequests) prune(oldest time.Time) {
	for session, waiting := range p.bySession {
		kept := slices.DeleteFunc(waiting, func(w pendingRequest) bool { return w.receivedAt.Before(oldest) })
		p.count -= len(waiting) - len(kept)
		if len(kept) == 0 {
			delete(p.bySession, session)
			continue
		}
		p.bySession[session] = kept
	}
}
