package tracker

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

type alertKey struct {
	kind    domain.AlertKind
	dir     domain.ConfigDir
	session domain.SessionID
}

// alertDates remembers when each alert that is firing was first seen.
// domain.Evaluate dates an alert by when the session began waiting, which it
// can tell, or else by the evaluation time, which would make a standing alert
// look new on every view.
type alertDates map[alertKey]time.Time

// date gives each alert the time it was first seen, forgets the alerts that
// no longer fire, and keeps alerts of a kind longest-standing first. alerts
// must be in Evaluate's order, which groups them by kind.
func (d alertDates) date(alerts []domain.Alert) []domain.Alert {
	seen := make(map[alertKey]time.Time, len(alerts))
	for i, a := range alerts {
		key := alertKey{a.Kind, a.Dir, a.Session}
		// A needs-you alert is dated by its session's wait, which starts
		// afresh each time the session needs the user again.
		if since, ok := d[key]; ok && a.Kind != domain.AlertNeedsYou {
			alerts[i].Since = since
		}
		seen[key] = alerts[i].Since
	}
	clear(d)
	maps.Copy(d, seen)

	for start := 0; start < len(alerts); {
		end := start + 1
		for end < len(alerts) && alerts[end].Kind == alerts[start].Kind {
			end++
		}
		slices.SortStableFunc(alerts[start:end], func(a, b domain.Alert) int {
			return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.Dir, b.Dir), cmp.Compare(a.Session, b.Session))
		})
		start = end
	}
	return alerts
}
