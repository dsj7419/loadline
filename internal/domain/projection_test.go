package domain_test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// The projection tests run at t0. ago and later place a sample or a reset
// relative to it.
func ago(minutes int) time.Time { return t0.Add(-time.Duration(minutes) * time.Minute) }

func later(hours int) time.Time { return t0.Add(time.Duration(hours) * time.Hour) }

func sampleAgo(minutes int, usedPct float64) domain.Sample {
	return domain.Sample{At: ago(minutes), UsedPct: usedPct}
}

// near reports whether got is within 0.01 of want.
func near(got, want float64) bool { return math.Abs(got-want) <= 0.01 }

// nearTime reports whether got is within a second of want.
func nearTime(got, want time.Time) bool { return got.Sub(want).Abs() <= time.Second }

func TestProject_TooFewSamplesUnknown(t *testing.T) {
	for name, samples := range map[string][]domain.Sample{
		"no samples": nil,
		"two samples over an hour": {
			sampleAgo(60, 50), sampleAgo(0, 60),
		},
		"three samples over under ten minutes": {
			sampleAgo(9, 50), sampleAgo(5, 52), sampleAgo(0, 54),
		},
		"three samples at one instant": {
			sampleAgo(30, 50), sampleAgo(30, 55), sampleAgo(30, 60),
		},
		"enough samples but all older than the window": {
			sampleAgo(180, 50), sampleAgo(150, 55), sampleAgo(130, 60),
		},
		"only two samples left after a reset": {
			sampleAgo(40, 90), sampleAgo(30, 95), sampleAgo(20, 2), sampleAgo(0, 4),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := domain.Project(samples, later(3), t0); got != (domain.Projection{}) {
				t.Errorf("Project() = %+v, want unknown", got)
			}
		})
	}
}

func TestProject_ThreeSamplesOverTenMinutesKnown(t *testing.T) {
	got := domain.Project([]domain.Sample{sampleAgo(10, 50), sampleAgo(5, 51), sampleAgo(0, 52)}, later(3), t0)

	if !got.Known {
		t.Fatalf("Project() = %+v, want known: three samples over exactly ten minutes are enough", got)
	}
	if !near(got.RatePerHour, 12) {
		t.Errorf("RatePerHour = %v, want 12", got.RatePerHour)
	}
}

func TestProject_SteadyBurnRunsOutBeforeReset(t *testing.T) {
	samples := []domain.Sample{sampleAgo(60, 80), sampleAgo(30, 85), sampleAgo(0, 90)}

	got := domain.Project(samples, later(3), t0)

	if !got.Known {
		t.Fatalf("Project() = %+v, want known", got)
	}
	if !near(got.RatePerHour, 10) {
		t.Errorf("RatePerHour = %v, want 10", got.RatePerHour)
	}
	if !nearTime(got.ETA, later(1)) {
		t.Errorf("ETA = %v, want an hour from now (%v)", got.ETA, later(1))
	}
	if !got.RunsOutBeforeReset {
		t.Error("RunsOutBeforeReset = false, want true: the limit is hit in an hour, the reset is in three")
	}
	if got.AtReset != 100 {
		t.Errorf("AtReset = %v, want it capped at 100", got.AtReset)
	}
}

func TestProject_SlowBurnOnPace(t *testing.T) {
	samples := []domain.Sample{sampleAgo(60, 50), sampleAgo(30, 51), sampleAgo(0, 52)}

	got := domain.Project(samples, later(10), t0)

	if !got.Known {
		t.Fatalf("Project() = %+v, want known", got)
	}
	if !near(got.RatePerHour, 2) {
		t.Errorf("RatePerHour = %v, want 2", got.RatePerHour)
	}
	if !near(got.AtReset, 72) {
		t.Errorf("AtReset = %v, want 72", got.AtReset)
	}
	if !nearTime(got.ETA, later(24)) {
		t.Errorf("ETA = %v, want 24 hours from now", got.ETA)
	}
	if got.RunsOutBeforeReset {
		t.Error("RunsOutBeforeReset = true, want false: the limit would be hit a day from now, the reset is in ten hours")
	}
}

func TestProject_IgnoresSamplesBeforeResetDrop(t *testing.T) {
	for name, tc := range map[string]struct {
		samples  []domain.Sample
		wantRate float64
	}{
		"one reset": {
			samples: []domain.Sample{
				sampleAgo(40, 93), sampleAgo(35, 94), sampleAgo(30, 1), sampleAgo(15, 2), sampleAgo(0, 3),
			},
			wantRate: 4,
		},
		"the last of two resets wins": {
			samples: []domain.Sample{
				sampleAgo(90, 80), sampleAgo(80, 10), sampleAgo(70, 30), sampleAgo(30, 5), sampleAgo(15, 6), sampleAgo(0, 7),
			},
			wantRate: 4,
		},
		"a fall of exactly five points is a reset": {
			samples: []domain.Sample{
				sampleAgo(40, 60), sampleAgo(30, 55), sampleAgo(15, 56), sampleAgo(0, 57),
			},
			wantRate: 4,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := domain.Project(tc.samples, later(3), t0)

			if !got.Known || !near(got.RatePerHour, tc.wantRate) {
				t.Errorf("Project() = %+v, want known at %v%%/h", got, tc.wantRate)
			}
		})
	}
}

func TestProject_SmallFallIsNotAReset(t *testing.T) {
	// A dip of 4.9 points keeps the earlier samples, which makes the trend fall.
	samples := []domain.Sample{sampleAgo(40, 60), sampleAgo(30, 55.1), sampleAgo(15, 56), sampleAgo(0, 57)}

	got := domain.Project(samples, later(3), t0)

	want := domain.Projection{Known: true, RatePerHour: 0, AtReset: 57}
	if got != want {
		t.Errorf("Project() = %+v, want %+v", got, want)
	}
}

func TestProject_FlatOrFalling(t *testing.T) {
	for name, tc := range map[string]struct {
		samples []domain.Sample
		last    float64
	}{
		"flat":    {[]domain.Sample{sampleAgo(60, 40), sampleAgo(30, 40), sampleAgo(0, 40)}, 40},
		"falling": {[]domain.Sample{sampleAgo(60, 40), sampleAgo(30, 39), sampleAgo(0, 38)}, 38},
	} {
		t.Run(name, func(t *testing.T) {
			got := domain.Project(tc.samples, later(3), t0)

			want := domain.Projection{Known: true, RatePerHour: 0, AtReset: tc.last}
			if got != want {
				t.Errorf("Project() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestProject_ResetInPastUnknown(t *testing.T) {
	samples := []domain.Sample{sampleAgo(60, 80), sampleAgo(30, 85), sampleAgo(0, 90)}

	for name, resetsAt := range map[string]time.Time{
		"already past": ago(5),
		"right now":    t0,
		"not reported": {},
	} {
		t.Run(name, func(t *testing.T) {
			if got := domain.Project(samples, resetsAt, t0); got != (domain.Projection{}) {
				t.Errorf("Project() = %+v, want unknown", got)
			}
		})
	}
}

func TestProject_WindowBoundary(t *testing.T) {
	t.Run("a sample exactly 120 minutes old is used", func(t *testing.T) {
		got := domain.Project([]domain.Sample{sampleAgo(120, 40), sampleAgo(60, 45), sampleAgo(0, 50)}, later(3), t0)

		if !got.Known || !near(got.RatePerHour, 5) {
			t.Errorf("Project() = %+v, want known at 5%%/h", got)
		}
	})
	t.Run("a sample older than that is not", func(t *testing.T) {
		got := domain.Project([]domain.Sample{sampleAgo(121, 30), sampleAgo(60, 45), sampleAgo(0, 50)}, later(3), t0)

		if got != (domain.Projection{}) {
			t.Errorf("Project() = %+v, want unknown: only two samples are inside the window", got)
		}
	})
	t.Run("an old sample does not tilt the trend", func(t *testing.T) {
		got := domain.Project([]domain.Sample{sampleAgo(200, 10), sampleAgo(60, 40), sampleAgo(30, 45), sampleAgo(0, 50)}, later(3), t0)

		if !got.Known || !near(got.RatePerHour, 10) {
			t.Errorf("Project() = %+v, want known at 10%%/h", got)
		}
	})
}

func TestProject_UnsortedSamplesAreSortedWithoutTouchingTheCaller(t *testing.T) {
	sorted := []domain.Sample{sampleAgo(60, 80), sampleAgo(45, 83), sampleAgo(20, 87), sampleAgo(0, 90)}
	shuffled := []domain.Sample{sorted[2], sorted[0], sorted[3], sorted[1]}
	before := append([]domain.Sample(nil), shuffled...)

	got := domain.Project(shuffled, later(3), t0)

	if want := domain.Project(sorted, later(3), t0); got != want {
		t.Errorf("Project(shuffled) = %+v, want what the sorted samples give: %+v", got, want)
	}
	if !reflect.DeepEqual(shuffled, before) {
		t.Errorf("Project reordered the caller's slice: %v, was %v", shuffled, before)
	}
}

func TestProject_NonFiniteSamplesAreIgnored(t *testing.T) {
	good := []domain.Sample{sampleAgo(60, 80), sampleAgo(30, 85), sampleAgo(0, 90)}
	noisy := append([]domain.Sample{
		sampleAgo(50, math.NaN()), sampleAgo(40, math.Inf(1)), sampleAgo(20, math.Inf(-1)),
	}, good...)

	got := domain.Project(noisy, later(3), t0)

	if want := domain.Project(good, later(3), t0); got != want {
		t.Errorf("Project() = %+v, want %+v, as if the non-finite samples were absent", got, want)
	}
}

func TestProject_OverflowIsUnknownNotNaN(t *testing.T) {
	samples := []domain.Sample{sampleAgo(60, 0), sampleAgo(30, math.MaxFloat64), sampleAgo(0, math.MaxFloat64)}

	got := domain.Project(samples, later(3), t0)

	if got != (domain.Projection{}) {
		t.Errorf("Project() = %+v, want unknown", got)
	}
}

func TestProject_NegligibleSlopeKeepsETAFinite(t *testing.T) {
	// The limit would be a few hundred trillion years away: the ETA must stay
	// after the reset, not wrap around into the past.
	creeping := math.Nextafter(50, 51)
	samples := []domain.Sample{sampleAgo(60, 50), sampleAgo(30, 50), sampleAgo(0, creeping)}

	got := domain.Project(samples, later(3), t0)

	if !got.Known || got.RatePerHour <= 0 {
		t.Fatalf("Project() = %+v, want known with a positive rate", got)
	}
	if got.RunsOutBeforeReset || !got.ETA.After(later(3)) {
		t.Errorf("Project() = %+v, want an ETA after the reset", got)
	}
}

func TestProject_AlreadyAtTheLimit(t *testing.T) {
	for name, samples := range map[string][]domain.Sample{
		"at 100":   {sampleAgo(60, 90), sampleAgo(30, 95), sampleAgo(0, 100)},
		"over 100": {sampleAgo(60, 94), sampleAgo(30, 98.5), sampleAgo(0, 103)},
	} {
		t.Run(name, func(t *testing.T) {
			got := domain.Project(samples, later(3), t0)

			if !got.Known || !got.RunsOutBeforeReset {
				t.Fatalf("Project() = %+v, want known and running out", got)
			}
			if !got.ETA.Equal(t0) {
				t.Errorf("ETA = %v, want now (%v): the limit is already reached", got.ETA, t0)
			}
			if got.AtReset != 100 {
				t.Errorf("AtReset = %v, want 100", got.AtReset)
			}
		})
	}
}
