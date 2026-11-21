package tracker

import "github.com/dsj7419/loadline/internal/domain"

// Recorder receives each change the tracker makes, so that it can be kept
// across restarts. The tracker calls it with its lock held, in the order the
// changes happen, so an implementation must return at once: it must not
// block, and must not call back into the Tracker. One that persists should
// queue the work for another goroutine and drop what it cannot keep up with.
//
// Values passed in are the recorder's to keep. The tracker never changes a
// rate-limit window after it has handed one out.
type Recorder interface {
	// SessionChanged reports a session as it stands after an event changed
	// it, including when the event first made it known.
	SessionChanged(domain.Session)
	// AccountChanged reports an account when it is first seen and each time
	// who it belongs to or a window's values change. A probe that finds the
	// account as it was, or a status line that reports the same windows
	// again, is not reported: LimitSampled carries how fresh the windows are.
	AccountChanged(domain.Account)
	// LimitSampled reports the rate-limit windows that gained a sample, as of
	// the limits' ObservedAt. A window that gained none is nil.
	LimitSampled(domain.ConfigDir, domain.Limits)
	// UsageRecorded reports a model call billed to the account in dir.
	UsageRecorded(domain.ConfigDir, domain.APIRequest)
}

// NopRecorder is a Recorder that keeps nothing.
type NopRecorder struct{}

// SessionChanged implements Recorder.
func (NopRecorder) SessionChanged(domain.Session) {}

// AccountChanged implements Recorder.
func (NopRecorder) AccountChanged(domain.Account) {}

// LimitSampled implements Recorder.
func (NopRecorder) LimitSampled(domain.ConfigDir, domain.Limits) {}

// UsageRecorded implements Recorder.
func (NopRecorder) UsageRecorded(domain.ConfigDir, domain.APIRequest) {}
