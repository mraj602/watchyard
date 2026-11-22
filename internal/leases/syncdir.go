package leases

import (
	"os"
	"runtime"
)

// syncDir makes a new or removed name in dir durable. Best effort; not supported on Windows.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
