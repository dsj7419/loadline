package launch

import (
	"testing"
	"time"
)

func TestParseBootTime_ReadsTheBtimeLine(t *testing.T) {
	stat := "cpu  1 2 3 4\ncpu0 1 2 3 4\nintr 5\nctxt 6\nbtime 1795000000\nprocesses 7\n"

	got, err := parseBootTime([]byte(stat))

	if err != nil || !got.Equal(time.Unix(1795000000, 0)) {
		t.Errorf("parseBootTime = %v, %v; want %v", got, err, time.Unix(1795000000, 0))
	}
}

func TestParseBootTime_RefusesAStatWithoutOne(t *testing.T) {
	for name, stat := range map[string]string{
		"missing":   "cpu  1 2 3 4\nprocesses 7\n",
		"malformed": "btime soon\n",
		"zero":      "btime 0\n",
		"prefix":    "btimex 1795000000\n",
	} {
		if got, err := parseBootTime([]byte(stat)); err == nil {
			t.Errorf("%s: parseBootTime = %v, want an error", name, got)
		}
	}
}
