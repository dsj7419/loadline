package demo_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// readmeCaption is the README's description of its screenshot of the demo.
var readmeCaption = regexp.MustCompile(`showing demo data: (\w+) accounts, (\w+) sessions and (\w+) alerts\. ` +
	`Main is at (\d+)% of its weekly limit, reached at a pace of ×(\d+\.\d), and works on usage credits`)

// The README's hero image is a screenshot of `serve --demo`, and its alt
// text says what the demo shows. Both tell the demo's current story, so the
// counts and Main's figures in the alt text must be the ones the demo serves.
func TestREADME_DescribesTheDemo(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read the README: %v", err)
	}
	m := readmeCaption.FindStringSubmatch(string(readme))
	if m == nil {
		t.Fatalf("the README has no caption matching %s", readmeCaption)
	}
	v := seeded(t, now)
	main := accountByLabel(t, v, "Main")
	if main.SevenDay == nil || main.SevenDay.Pace == nil || main.Credits == nil {
		t.Fatalf("Main shows %+v, want a weekly window with a pace, and credits", main)
	}

	numbers := map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7}
	for _, c := range []struct {
		what, said string
		served     int
	}{
		{"accounts", m[1], len(v.Accounts)},
		{"sessions", m[2], len(v.Sessions)},
		{"alerts", m[3], len(v.Alerts)},
	} {
		if numbers[c.said] != c.served {
			t.Errorf("the README says %s %s; the demo serves %d", c.said, c.what, c.served)
		}
	}
	if want := fmt.Sprintf("%.0f", main.SevenDay.UsedPct); m[4] != want {
		t.Errorf("the README has Main at %s%% of its week; the demo shows %s%%", m[4], want)
	}
	if want := fmt.Sprintf("%.1f", shownPace(*main.SevenDay.Pace)); m[5] != want {
		t.Errorf("the README gives Main a pace of ×%s; the demo shows ×%s", m[5], want)
	}
	if !main.Credits.Active {
		t.Errorf("the README has Main on usage credits; the demo's credits are %+v", main.Credits)
	}
}
