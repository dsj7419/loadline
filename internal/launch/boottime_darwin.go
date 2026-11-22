package launch

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// BootTime returns when the machine started, as the kernel recorded it.
func BootTime() (time.Time, error) {
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, fmt.Errorf("read kern.boottime: %w", err)
	}
	return time.Unix(boot.Unix()), nil
}
