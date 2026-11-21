// Package tracker owns Loadline's live state. It folds hook, poll, status
// line and model-call events into sessions and accounts, derives usage,
// projections, shares and alerts from them, and publishes the result as
// Views.
package tracker

import (
	"cmp"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	// defaultShareWindow is how far back session shares look unless Config
	// says otherwise.
	defaultShareWindow = 60 * time.Minute
	// finishedKeep is how long a parked or ended session stays after it
	// finished.
	finishedKeep = 24 * time.Hour
	// maxTimeline is how many state changes each session keeps.
	maxTimeline = 50
	// clearLinkWindow is how close the start of a session must come to the
	// /clear that ended its predecessor in the same process.
	clearLinkWindow = 10 * time.Second
	endReasonClear  = "clear"
)

// Clock returns the current time.
type Clock func() time.Time

// Config holds what the tracker needs to know beyond the events.
type Config struct {
	// Labels and Budgets are keyed by normalized config dir. A budget is in
	// US dollars a day.
	Labels  map[domain.ConfigDir]string
	Budgets map[domain.ConfigDir]float64
	Alerts  domain.AlertConfig
	// ShareWindow is how far back a session's share of its account's spend
	// looks. Zero means 60 minutes.
	ShareWindow time.Duration
	// Location is the time zone whose calendar days usage is totaled by. Nil
	// means the local time zone.
	Location *time.Location
	// Ticker schedules the tracker's deferred work. Nil means wall-clock
	// timers.
	Ticker Ticker
}

// Restored is the state a store kept from an earlier run.
type Restored struct {
	Accounts  []domain.Account
	Sessions  []domain.Session
	Samples   map[domain.ConfigDir]WindowSamples
	Usage     []UsageRow
	Timelines map[domain.SessionID][]domain.Transition
}

// sessionEntry is a session and what the tracker keeps alongside it.
type sessionEntry struct {
	session domain.Session
	// firstSeen is when the tracker first learned of the session. Unlike
	// StartedAt, which a poll moves back to the start of the process, it
	// dates a session that /clear began inside a running process.
	firstSeen time.Time
	timeline  []domain.Transition
	// replaces and replacedBy link the sessions /clear splits one terminal's
	// conversation into.
	replaces, replacedBy domain.SessionID
}

// Tracker holds the live state. It is safe for concurrent use: one lock
// guards everything, and the Recorder and subscribers are reached without
// ever blocking under it.
type Tracker struct {
	clock  Clock
	cfg    Config
	rec    Recorder
	ticker Ticker

	mu       sync.Mutex
	sessions map[domain.SessionID]*sessionEntry
	accounts map[domain.ConfigDir]*accountEntry
	usage    usageBook
	pending  pendingRequests
	alerts   alertDates
	pub      publisher
}

// New returns an empty tracker that reads the time from clock and reports
// each change to rec, which must not block (see Recorder).
func New(clock Clock, cfg Config, rec Recorder) *Tracker {
	cfg.Labels, cfg.Budgets = maps.Clone(cfg.Labels), maps.Clone(cfg.Budgets)
	if cfg.ShareWindow <= 0 {
		cfg.ShareWindow = defaultShareWindow
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	ticker := cfg.Ticker
	if ticker == nil {
		ticker = wallTicker{}
	}
	return &Tracker{
		clock:    clock,
		cfg:      cfg,
		rec:      rec,
		ticker:   ticker,
		sessions: make(map[domain.SessionID]*sessionEntry),
		accounts: make(map[domain.ConfigDir]*accountEntry),
		usage:    newUsageBook(cfg.Location),
		pending:  newPendingRequests(),
		alerts:   make(alertDates),
		pub:      publisher{subscribers: make(map[chan View]struct{})},
	}
}

// Apply folds one event into the state. A Status also reports its account's
// rate limits: the newest observation wins.
func (t *Tracker) Apply(e domain.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.apply(e)
	t.changed()
}

// ApplyPoll folds in one poll of dir, taken at at: each entry, and then an
// absence for each session of dir that the poll no longer lists.
func (t *Tracker) ApplyPoll(dir domain.ConfigDir, at time.Time, polls []domain.Poll) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.account(dir)
	listed := make(map[domain.SessionID]bool, len(polls))
	for _, p := range onePerSession(polls) {
		listed[p.Session] = true
		t.apply(p)
	}
	var missing []domain.SessionID
	for id, e := range t.sessions {
		if e.session.Dir == dir && e.session.State != domain.StateEnded && !listed[id] {
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	for _, id := range missing {
		t.apply(domain.Absent{At: at, Dir: dir, Session: id})
	}
	t.changed()
}

// onePerSession keeps one entry for each session. After an in-place resume,
// `claude agents --all` lists a background session both as the record of its
// earlier run and as its live process; the live entry wins.
func onePerSession(polls []domain.Poll) []domain.Poll {
	at := make(map[domain.SessionID]int, len(polls))
	kept := make([]domain.Poll, 0, len(polls))
	for _, p := range polls {
		i, seen := at[p.Session]
		if !seen {
			at[p.Session] = len(kept)
			kept = append(kept, p)
			continue
		}
		if kept[i].PID == 0 && p.PID > 0 {
			kept[i] = p
		}
	}
	return kept
}

// RecordAPI attributes a model call to its session's account. A call for a
// session not yet known waits up to ten minutes for it to appear.
func (t *Tracker) RecordAPI(r domain.APIRequest) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.sessions[r.Session]; ok {
		t.attach(e.session.Dir, r)
	} else {
		t.pending.hold(r, t.clock())
	}
	t.changed()
}

// SetAccount records who an account belongs to. Its rate limits are left as
// they are, since status lines report them, and so is its email when a has
// none.
func (t *Tracker) SetAccount(a domain.Account) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, isNew := t.accountEntry(a.Dir)
	before := entry.account
	entry.account.Label, entry.account.Plan, entry.account.Org = a.Label, a.Plan, a.Org
	entry.account.Auth, entry.account.ProbedAt = a.Auth, a.ProbedAt
	if a.Email != "" {
		entry.account.Email = a.Email
	}
	if isNew || entry.account != before {
		t.rec.AccountChanged(entry.account)
	}
	t.changed()
}

// Restore installs the state a store kept from an earlier run. It is meant
// for startup: an account or session the tracker already knows is newer than
// the store's copy and stays as it is, and usage is added to what the
// tracker holds. Nothing restored is reported to the Recorder, which kept it.
func (t *Tracker) Restore(s Restored) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, a := range s.Accounts {
		if _, known := t.accounts[a.Dir]; known {
			continue
		}
		a.Limits.FiveHour, a.Limits.SevenDay = cloneWindow(a.Limits.FiveHour), cloneWindow(a.Limits.SevenDay)
		t.accounts[a.Dir] = &accountEntry{account: a}
	}
	for dir, samples := range s.Samples {
		entry, _ := t.accountEntry(dir)
		entry.restore(samples)
	}
	for _, row := range s.Usage {
		t.usage.add(row.Dir, row.Session, row.Model, row.Minute, usageOfRow(row))
	}

	var restored []*sessionEntry
	for _, session := range s.Sessions {
		if _, known := t.sessions[session.ID]; known {
			continue
		}
		t.accountEntry(session.Dir)
		timeline := s.Timelines[session.ID]
		e := &sessionEntry{
			session:   session,
			firstSeen: session.StartedAt,
			timeline:  slices.Clone(timeline[max(0, len(timeline)-maxTimeline):]),
		}
		t.sessions[session.ID] = e
		restored = append(restored, e)
		for _, r := range t.pending.take(session.ID, t.clock().Add(-pendingFor)) {
			t.attach(session.Dir, r)
		}
	}
	for _, e := range restored {
		t.link(e)
	}
	t.changed()
}

// KnownDirs returns the config dir of every account seen, in order.
func (t *Tracker) KnownDirs() []domain.ConfigDir {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Sorted(maps.Keys(t.accounts))
}

// HasLive reports whether any session is live: neither parked nor ended.
func (t *Tracker) HasLive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.sessions {
		if state := e.session.State; state != domain.StateParked && state != domain.StateEnded {
			return true
		}
	}
	return false
}

// Snapshot returns the view as of now.
func (t *Tracker) Snapshot() View {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buildView(t.clock())
}

// apply folds one event into its session, creating the session when the
// event is the first sign of it.
func (t *Tracker) apply(e domain.Event) {
	id, dir, at, sighting := subjectOf(e)
	if sighting {
		t.account(dir)
	}
	if st, ok := e.(domain.Status); ok {
		t.observeLimits(st.Dir, st.Limits)
	}
	if id == "" {
		return
	}

	entry, known := t.sessions[id]
	if !known {
		if !opensSession(e) {
			return
		}
		entry = &sessionEntry{session: domain.NewSession(id, dir, at), firstSeen: at}
		t.sessions[id] = entry
	}
	before := entry.session
	after := domain.Apply(before, e)
	entry.session = after
	if !known || after.State != before.State {
		entry.timeline = appendTransition(entry.timeline, domain.Transition{
			At: after.StateAt, State: after.State, Reason: cmp.Or(after.WaitReason, after.EndReason),
		})
	}
	if !known || after != before {
		t.rec.SessionChanged(after)
	}
	if !known {
		for _, r := range t.pending.take(id, t.clock().Add(-pendingFor)) {
			t.attach(after.Dir, r)
		}
	}
	t.link(entry)
}

// subjectOf returns the session, account and time an event is about, and
// whether it is a sighting of the session rather than of its absence.
func subjectOf(e domain.Event) (id domain.SessionID, dir domain.ConfigDir, at time.Time, sighting bool) {
	switch e := e.(type) {
	case domain.Hook:
		return e.Session, e.Dir, e.At, true
	case domain.Poll:
		return e.Session, e.Dir, e.At, true
	case domain.Status:
		return e.Session, e.Dir, e.At, true
	case domain.Absent:
		return e.Session, e.Dir, e.At, false
	default:
		return "", "", time.Time{}, false
	}
}

// opensSession reports whether e makes a session known that was not. An
// absence never does, and neither does the record `claude agents --all`
// keeps of a background session that finished long ago: it lists those
// forever, and each would come back every time the old one expired.
func opensSession(e domain.Event) bool {
	switch e := e.(type) {
	case domain.Hook, domain.Status:
		return true
	case domain.Poll:
		return !finishedLongAgo(e)
	default:
		return false
	}
}

func finishedLongAgo(p domain.Poll) bool {
	if p.PID != 0 || p.StartedAt.IsZero() {
		return false
	}
	switch p.BgState {
	case "done", "stopped", "failed":
		return p.At.Sub(p.StartedAt) > finishedKeep
	default:
		return false
	}
}

func appendTransition(timeline []domain.Transition, tr domain.Transition) []domain.Transition {
	if len(timeline) < maxTimeline {
		return append(timeline, tr)
	}
	copy(timeline, timeline[1:])
	timeline[len(timeline)-1] = tr
	return timeline
}

// observeLimits applies a status line's rate limits to the account it came
// from.
func (t *Tracker) observeLimits(dir domain.ConfigDir, l domain.Limits) {
	entry := t.account(dir)
	changed, sampled := entry.observe(l)
	if !changed {
		return
	}
	t.rec.AccountChanged(entry.account)
	if sampled.FiveHour != nil || sampled.SevenDay != nil {
		t.rec.LimitSampled(dir, sampled)
	}
}

// attach records a model call against the account in dir. A request carries
// the account's email, which stands in when the account probe has not
// supplied one.
func (t *Tracker) attach(dir domain.ConfigDir, r domain.APIRequest) {
	entry := t.account(dir)
	t.usage.add(dir, r.Session, r.Model, r.At, usageOf(r))
	t.rec.UsageRecorded(dir, r)
	if entry.account.Email == "" && r.Email != "" {
		entry.account.Email = r.Email
		t.rec.AccountChanged(entry.account)
	}
}

// account returns dir's account, reporting it to the Recorder when it is new.
func (t *Tracker) account(dir domain.ConfigDir) *accountEntry {
	entry, isNew := t.accountEntry(dir)
	if isNew {
		t.rec.AccountChanged(entry.account)
	}
	return entry
}

// accountEntry returns dir's account, creating it when it is new.
func (t *Tracker) accountEntry(dir domain.ConfigDir) (entry *accountEntry, isNew bool) {
	if entry, ok := t.accounts[dir]; ok {
		return entry, false
	}
	entry = &accountEntry{account: domain.Account{Dir: dir}}
	t.accounts[dir] = entry
	return entry, true
}

// link joins e to the session /clear ended to start it, or to the session it
// started by ending with /clear, whichever applies. Either side can be known
// first: the hooks of both fire together and may arrive in either order, and
// the process ID may come only with a later event.
func (t *Tracker) link(e *sessionEntry) {
	if e.session.PID <= 0 {
		return
	}
	if before := t.nearest(func(c *sessionEntry) (time.Duration, bool) { return clearGap(c, e) }); before != nil {
		before.replacedBy, e.replaces = e.session.ID, before.session.ID
	}
	if next := t.nearest(func(c *sessionEntry) (time.Duration, bool) { return clearGap(e, c) }); next != nil {
		e.replacedBy, next.replaces = next.session.ID, e.session.ID
	}
}

// clearGap reports how far from the /clear that ended before the session
// next began, and whether next is what replaced it: neither is linked yet,
// before ended with /clear, and next began in the same process within
// clearLinkWindow of that.
func clearGap(before, next *sessionEntry) (time.Duration, bool) {
	b, n := before.session, next.session
	if before == next || before.replacedBy != "" || next.replaces != "" ||
		b.State != domain.StateEnded || b.EndReason != endReasonClear ||
		b.Dir != n.Dir || b.PID <= 0 || b.PID != n.PID {
		return 0, false
	}
	gap := next.firstSeen.Sub(b.EndedAt).Abs()
	return gap, gap <= clearLinkWindow
}

// nearest returns the session for which gap holds with the smallest gap, the
// lower ID breaking a tie, or nil when there is none.
func (t *Tracker) nearest(gap func(*sessionEntry) (time.Duration, bool)) *sessionEntry {
	var best *sessionEntry
	var bestGap time.Duration
	for _, c := range t.sessions {
		g, ok := gap(c)
		if !ok {
			continue
		}
		if best == nil || g < bestGap || (g == bestGap && c.session.ID < best.session.ID) {
			best, bestGap = c, g
		}
	}
	return best
}

// expire forgets what has aged out by now: sessions parked or ended for a
// day, usage older than the days the view covers, spend older than the share
// window, calls that waited too long for their session, and samples older
// than any projection reads.
func (t *Tracker) expire(now time.Time) {
	finishedBefore := now.Add(-finishedKeep)
	for id, e := range t.sessions {
		state := e.session.State
		if (state == domain.StateParked || state == domain.StateEnded) && !e.session.StateAt.After(finishedBefore) {
			delete(t.sessions, id)
		}
	}
	t.usage.prune(dayOf(now, t.cfg.Location)-keepDays+1, now.Add(-t.cfg.ShareWindow))
	t.pending.prune(now.Add(-pendingFor))
	for _, a := range t.accounts {
		a.pruneSamples(now.Add(-sampleHorizon))
	}
}

// anyNeedsYou reports whether any session is waiting on the user.
func (t *Tracker) anyNeedsYou() bool {
	for _, e := range t.sessions {
		if e.session.State == domain.StateNeedsYou {
			return true
		}
	}
	return false
}
