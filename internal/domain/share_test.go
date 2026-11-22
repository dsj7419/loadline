package domain_test

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const shareWindow = 60 * time.Minute

func spendAgo(minutes int, dir domain.ConfigDir, session domain.SessionID, costUSD float64) domain.Spend {
	return domain.Spend{At: ago(minutes), Session: session, Dir: dir, CostUSD: costUSD}
}

func TestShares_FractionOfAccountWithinWindow(t *testing.T) {
	spend := []domain.Spend{
		spendAgo(10, dirA, "A1", 3),
		spendAgo(20, dirA, "A2", 1),
		spendAgo(5, dirB, "B1", 5),
		// Older than the window: neither counted in the account's total nor
		// able to give A3 a share.
		spendAgo(90, dirA, "A1", 100),
		spendAgo(61, dirA, "A3", 50),
	}

	got := domain.Shares(spend, t0, shareWindow)

	want := map[domain.SessionID]float64{"A1": 0.75, "A2": 0.25, "B1": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shares() = %v, want %v", got, want)
	}
}

func TestShares_AddsUpASessionsSpend(t *testing.T) {
	spend := []domain.Spend{
		spendAgo(50, dirA, "A1", 1),
		spendAgo(30, dirA, "A1", 2),
		spendAgo(10, dirA, "A2", 1),
	}

	got := domain.Shares(spend, t0, shareWindow)

	want := map[domain.SessionID]float64{"A1": 0.75, "A2": 0.25}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shares() = %v, want %v", got, want)
	}
}

func TestShares_WindowEdge(t *testing.T) {
	spend := []domain.Spend{
		spendAgo(60, dirA, "inside", 1),
		spendAgo(61, dirA, "outside", 1),
	}

	got := domain.Shares(spend, t0, shareWindow)

	want := map[domain.SessionID]float64{"inside": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shares() = %v, want %v: spend exactly one window old counts, older does not", got, want)
	}
}

func TestShares_ZeroSpendOmitted(t *testing.T) {
	spend := []domain.Spend{
		spendAgo(10, dirA, "idle", 0),
		spendAgo(10, dirB, "free", 0),
		spendAgo(10, dirB, "also-free", 0),
		spendAgo(10, dirA, "busy", 2),
	}

	got := domain.Shares(spend, t0, shareWindow)

	want := map[domain.SessionID]float64{"busy": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shares() = %v, want %v: a session that spent nothing has no share, and B has no total to divide by", got, want)
	}
}

func TestShares_UnusableCostsAreSkipped(t *testing.T) {
	spend := []domain.Spend{
		spendAgo(10, dirA, "A1", 1),
		spendAgo(10, dirA, "negative", -4),
		spendAgo(10, dirA, "nan", math.NaN()),
		spendAgo(10, dirA, "inf", math.Inf(1)),
	}

	got := domain.Shares(spend, t0, shareWindow)

	want := map[domain.SessionID]float64{"A1": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shares() = %v, want %v", got, want)
	}
}

func TestShares_SessionMovedBetweenAccountsReportsLargerShare(t *testing.T) {
	// Ten sessions each spent $1 of account A's $20, then $3 of B's $30. Map
	// iteration order is random, so one session could pass by luck; ten
	// cannot.
	spend := []domain.Spend{spendAgo(30, dirA, "stayer", 10)}
	want := map[domain.SessionID]float64{"stayer": 0.5}
	for i := range 10 {
		mover := domain.SessionID(fmt.Sprintf("mover-%d", i))
		spend = append(spend, spendAgo(30, dirA, mover, 1), spendAgo(10, dirB, mover, 3))
		want[mover] = 0.1
	}

	got := domain.Shares(spend, t0, shareWindow)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shares() = %v, want %v", got, want)
	}
}

func TestShares_NothingToShareIsAnEmptyMap(t *testing.T) {
	got := domain.Shares(nil, t0, shareWindow)

	if got == nil || len(got) != 0 {
		t.Errorf("Shares(nil) = %#v, want an empty, non-nil map", got)
	}
}

// Costs too large to add up must not make a share infinite or NaN: the view
// that carries it could not be encoded. The account's total stops at the
// largest finite cost.
func TestShares_StayFiniteWhenCostsOverflow(t *testing.T) {
	nearMax := 0.9 * math.MaxFloat64
	spend := []domain.Spend{
		spendAgo(10, dirA, "A1", nearMax),
		spendAgo(20, dirA, "A1", nearMax),
		spendAgo(30, dirA, "A2", nearMax),
		spendAgo(10, dirB, "B1", nearMax),
		spendAgo(20, dirB, "B1", nearMax),
	}

	got := domain.Shares(spend, t0, shareWindow)

	if len(got) != 3 {
		t.Fatalf("Shares() = %v, want a share for A1, A2 and B1", got)
	}
	for session, share := range got {
		if math.IsNaN(share) || share <= 0 || share > 1 {
			t.Errorf("%s's share is %v, want a fraction above 0 and at most 1", session, share)
		}
	}
	if got["B1"] != 1 {
		t.Errorf("B1's share is %v, want 1: it is its account's only session", got["B1"])
	}
}
