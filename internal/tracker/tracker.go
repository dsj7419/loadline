// Package tracker owns Loadline's live state. It folds hook, poll, status
// line and model-call events into sessions and accounts, derives usage,
// projections, shares and alerts from them, and publishes the result as
// Views.
package tracker

import (
	"cmp"
	"log/slog"
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
	// Plans are the plan names the owner gave accounts, keyed by normalized
	// config dir. The view shows one in place of the plan its account
	// reports; everything else, the history included, keeps the reported
	// plan.
	Plans map[domain.ConfigDir]string
	// Credits are the usage credits the owner recorded for each account,
	// keyed by normalized config dir.
	Credits map[domain.ConfigDir][]domain.CreditGrant
	// CreditDays is how many days back credit estimates count spend. The
	// server sets it to the days its history keeps, so a tracker restored
	// from the history counts what one that ran on does. It is at most 30,
	// the days the view's spend covers; zero means 30.
	CreditDays int
	Alerts     domain.AlertConfig
	// ShareWindow is how far back a session's share of its account's spend
	// looks. Zero means 60 minutes.
	ShareWindow time.Duration
	// Location is the time zone whose calendar days usage is totaled by. Nil
	// means the local time zone.
	Location *time.Location
	// Ticker schedules the tracker's deferred work. Nil means wall-clock
	// timers.
	Ticker Ticker
	// Log receives the tracker's warnings. Nil discards them.
	Log *slog.Logger
}

// Restored is the state a store kept from an earlier run. A session needs its
// StartedAt, StateAt and LastSeenAt, and a transition its At: Restore skips a
// session that lacks one and drops such a transition, so the view never shows
// a zero time.
type Restored struct {
	Accounts  []domain.Account
	Sessions  []domain.Session
	Samples   map[domain.ConfigDir]domain.WindowSamples
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
	// responses dates the rate limits the session's status line reports.
	responses responseClock
}

// Tracker holds the live state. It is safe for concurrent use: one lock
// guards everything, and the Recorder and subscribers are reached without
// ever blocking under it.
type Tracker struct {
	clock  Clock
	cfg    Config
	rec    Recorder
	ticker Ticker
	log    *slog.Logger

	mu sync.Mutex
	// newest is the latest reading of the clock, which tells when the clock
	// has been set back.
	newest   time.Time
	sessions map[domain.SessionID]*sessionEntry
	accounts map[domain.ConfigDir]*accountEntry
	usage    usageBook
	pending  pendingRequests
	alerts   alertDates
	pub      publisher
}

// New returns an empty tracker that reads the time from clock and reports
// each change to rec, which must not block (see Recorder). A nil rec keeps
// nothing.
func New(clock Clock, cfg Config, rec Recorder) *Tracker {
	if rec == nil {
		rec = NopRecorder{}
	}
	cfg.Labels, cfg.Budgets, cfg.Plans = maps.Clone(cfg.Labels), maps.Clone(cfg.Budgets), maps.Clone(cfg.Plans)
	cfg.Credits = cloneGrants(cfg.Credits)
	if cfg.ShareWindow <= 0 {
		cfg.ShareWindow = defaultShareWindow
	}
	if cfg.CreditDays <= 0 || cfg.CreditDays > keepDays {
		cfg.CreditDays = keepDays
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	ticker := cfg.Ticker
	if ticker == nil {
		ticker = wallTicker{}
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Tracker{
		clock:    clock,
		cfg:      cfg,
		rec:      rec,
		ticker:   ticker,
		log:      log,
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
	t.now()
	t.apply(e)
	t.changed()
}

// ApplyPoll folds in one poll of dir, taken at at: each entry, and then an
// absence for each session of dir that the poll no longer lists. A poll
// taken before the clock was set back and applied after is dated now.
func (t *Tracker) ApplyPoll(dir domain.ConfigDir, at time.Time, polls []domain.Poll) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	at = notAhead(at, now)
	t.account(dir)
	listed := make(map[domain.SessionID]bool, len(polls))
	for _, p := range onePerSession(polls) {
		listed[p.Session] = true
		p.At = notAhead(p.At, now)
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
// session not yet known waits up to ten minutes for it to appear, and shows
// nothing until then.
func (t *Tracker) RecordAPI(r domain.APIRequest) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e, ok := t.sessions[r.Session]
	if !ok {
		t.pending.hold(r, now)
		return
	}
	t.attach(e, r)
	t.changed()
}

// SetAccount records who an account belongs to. Its rate limits are left as
// they are, since status lines report them, and so is its email when a has
// none. A probe that finds the account as it was changes only ProbedAt, and
// is neither recorded nor published.
func (t *Tracker) SetAccount(a domain.Account) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now()
	entry, isNew := t.accountEntry(a.Dir)
	before := entry.account
	entry.account.Label, entry.account.Plan, entry.account.Org = a.Label, a.Plan, a.Org
	entry.account.Auth, entry.account.ProbedAt = a.Auth, a.ProbedAt
	if a.Email != "" {
		entry.account.Email = a.Email
	}
	after := entry.account
	if isNew || !sameIdentity(before, after) {
		t.rec.AccountChanged(after)
	}
	if isNew || t.shownDifferently(before, after) {
		t.changed()
	}
}

// AddAccounts lists the account of each dir from now on, though nothing has
// been seen of it yet: an account the config names is shown before any
// poll, probe or event reaches it, and while its config dir is missing. An
// account already known stays as it is.
func (t *Tracker) AddAccounts(dirs []domain.ConfigDir) {
	t.mu.Lock()
	defer t.mu.Unlock()
	added := false
	for _, dir := range dirs {
		if entry, isNew := t.accountEntry(dir); isNew {
			t.rec.AccountChanged(entry.account)
			added = true
		}
	}
	if added {
		t.changed()
	}
}

// sameIdentity reports whether a and b describe the same owner and sign-in,
// however far apart they were probed.
func sameIdentity(a, b domain.Account) bool {
	return a.Label == b.Label && a.Email == b.Email && a.Plan == b.Plan && a.Org == b.Org && a.Auth == b.Auth
}

// shownDifferently reports whether the view shows who owns a differently
// from b.
func (t *Tracker) shownDifferently(a, b domain.Account) bool {
	return t.label(a) != t.label(b) || a.Email != b.Email || t.plan(a) != t.plan(b) || a.Auth != b.Auth
}

// Restore installs the state a store kept from an earlier run. It is meant
// for startup: an account or session the tracker already knows is newer than
// the store's copy and stays as it is, and usage is added to what the
// tracker holds. Nothing restored is reported to the Recorder, which kept it.
func (t *Tracker) Restore(s Restored) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for _, a := range s.Accounts {
		if _, known := t.accounts[a.Dir]; known {
			continue
		}
		t.accounts[a.Dir] = restoredAccount(a)
	}
	for dir, samples := range s.Samples {
		entry, _ := t.accountEntry(dir)
		entry.restore(samples)
	}
	for _, row := range s.Usage {
		t.usage.add(row.Dir, row.Session, row.Model, row.Minute, usageOfRow(row))
		// Usage is kept by the minute, so a restored call is dated at the
		// start of its minute: one made in the minute an account reached its
		// limit may fall just before the span on credits it was made in, and
		// one made in the minute a span closed, after it closed, just inside
		// that span.
		if entry, ok := t.accounts[row.Dir]; ok {
			entry.credits.recordCall(row.Minute, row.CostUSD)
		}
	}

	var restored []*sessionEntry
	for _, session := range s.Sessions {
		if _, known := t.sessions[session.ID]; known || !isDated(session) {
			continue
		}
		t.accountEntry(session.Dir)
		e := &sessionEntry{
			session:   session,
			firstSeen: session.StartedAt,
			timeline:  datedTimeline(s.Timelines[session.ID]),
		}
		t.sessions[session.ID] = e
		restored = append(restored, e)
		for _, r := range t.pending.take(session.ID, now.Add(-pendingFor)) {
			t.attach(e, r)
		}
	}
	for _, e := range restored {
		t.link(e)
	}
	t.changed()
}

func isDated(s domain.Session) bool {
	return !s.StartedAt.IsZero() && !s.StateAt.IsZero() && !s.LastSeenAt.IsZero()
}

// datedTimeline returns the last maxTimeline of the transitions that carry a
// time, in a new slice.
func datedTimeline(timeline []domain.Transition) []domain.Transition {
	dated := slices.DeleteFunc(slices.Clone(timeline), func(tr domain.Transition) bool { return tr.At.IsZero() })
	return dated[max(0, len(dated)-maxTimeline):]
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
		if isLive(e.session) {
			return true
		}
	}
	return false
}

// HasLiveIn reports whether any session of dir's account is live.
func (t *Tracker) HasLiveIn(dir domain.ConfigDir) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.sessions {
		if e.session.Dir == dir && isLive(e.session) {
			return true
		}
	}
	return false
}

func isLive(s domain.Session) bool {
	return s.State != domain.StateParked && s.State != domain.StateEnded
}

// Snapshot returns the view as of now.
func (t *Tracker) Snapshot() View {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buildView(t.now())
}

// apply folds one event into its session, creating the session when the
// event is the first sign of it. A status line's rate limits are judged once
// the session holds what came with the line, its waiting calls included.
func (t *Tracker) apply(e domain.Event) {
	id, dir, at, sighting := subjectOf(e)
	if sighting {
		t.account(dir)
	}
	entry := t.applyToSession(e, id, dir, at)
	if st, ok := e.(domain.Status); ok {
		t.observeStatus(st, entry)
	}
}

// applyToSession folds e into session id, of dir, as of at, creating it when
// e is the first sign of it, and returns the session's entry, or nil when e
// is of no session the tracker keeps.
func (t *Tracker) applyToSession(e domain.Event, id domain.SessionID, dir domain.ConfigDir, at time.Time) *sessionEntry {
	if id == "" {
		return nil
	}
	entry, known := t.sessions[id]
	if !known {
		if !opensSession(e) {
			return nil
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
	if after.State == domain.StateEnded && before.State != domain.StateEnded {
		entry.responses.ended()
	}
	if !known {
		for _, r := range t.pending.take(id, t.now().Add(-pendingFor)) {
			t.attach(entry, r)
		}
	}
	t.link(entry)
	return entry
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
// keeps of a background session that started over a day ago and has no
// process: it lists those forever, and each would come back every time the
// old one expired.
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
	case "done", "blocked", "stopped", "failed":
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

// observeStatus applies what a status line reports of its account: the rate
// limits, as fresh as the newest response its session, nil when it has none,
// is known to have got on that account, and the spend limit.
func (t *Tracker) observeStatus(st domain.Status, session *sessionEntry) {
	var freshAs time.Time
	if session != nil {
		session.responses.line(st.Dir, st.APIDurationMS, st.At)
		freshAs = session.responses.freshAs(st.Dir)
	}
	t.observeLimits(st.Dir, st.Limits, freshAs)
	t.account(st.Dir).credits.observe(st.SpendLimit, st.At)
}

// observeLimits applies a status line's rate limits, as fresh as freshAs, to
// the account it came from.
func (t *Tracker) observeLimits(dir domain.ConfigDir, l domain.Limits, freshAs time.Time) {
	entry := t.account(dir)
	moved, sampled := entry.observe(l, freshAs)
	if moved {
		t.rec.AccountChanged(entry.account)
	}
	if sampled.FiveHour != nil || sampled.SevenDay != nil {
		t.rec.LimitSampled(dir, sampled)
	}
}

// attach records a model call of session s against its account. A request
// carries the account's email, which stands in when the account probe has
// not supplied one.
func (t *Tracker) attach(s *sessionEntry, r domain.APIRequest) {
	// A call stamped before the clock was set back would date its session
	// ahead of every response after the step, so it is dated at the step, as a
	// poll is. That still makes it newer than the responses held from before
	// the step, which is accepted: its session's next reading is taken once,
	// until another session's next response, and a call made at the limit in
	// between may go uncounted on credits.
	s.responses.call(notAhead(r.At, t.newest))
	dir := s.session.Dir
	entry := t.account(dir)
	t.usage.add(dir, r.Session, r.Model, r.At, usageOf(r))
	entry.credits.recordCall(r.At, r.CostUSD)
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
// window, calls that waited too long for their session, and samples and
// calls older than any projection reads, but for those a span on credits
// still needs.
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
	horizon, oldest := now.Add(-sampleHorizon), now.Add(-t.creditHistory())
	for _, a := range t.accounts {
		a.credits.prune(domain.OnCreditsSpans(a.samples, now), horizon, oldest)
		a.pruneSamples(horizon, oldest)
	}
}

// awaitingNeedsYouAlert reports whether a session waits on the user but has
// not yet waited long enough to alert.
func (t *Tracker) awaitingNeedsYouAlert(now time.Time) bool {
	for _, e := range t.sessions {
		if e.session.State == domain.StateNeedsYou && now.Sub(e.session.StateAt) < t.cfg.Alerts.NeedsYouAfter {
			return true
		}
	}
	return false
}
