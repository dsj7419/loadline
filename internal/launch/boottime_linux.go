package launch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

const procStat = "/proc/stat"

// BootTime returns when the machine started, as the kernel recorded it.
func BootTime() (time.Time, error) {
	stat, err := os.ReadFile(procStat)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the boot time: %w", err)
	}
	boot, err := parseBootTime(stat)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the boot time from %s: %w", procStat, err)
	}
	return boot, nil
}

// parseBootTime reads the btime line of /proc/stat: the boot time in
// seconds since the Unix epoch.
func parseBootTime(stat []byte) (time.Time, error) {
	for line := range bytes.Lines(stat) {
		value, found := bytes.CutPrefix(line, []byte("btime "))
		if !found {
			continue
		}
		seconds, err := strconv.ParseInt(string(bytes.TrimSpace(value)), 10, 64)
		if err != nil || seconds <= 0 {
			return time.Time{}, fmt.Errorf("btime %q is not a time", bytes.TrimSpace(value))
		}
		return time.Unix(seconds, 0), nil
	}
	return time.Time{}, errors.New("no btime line")
}
