//go:build !darwin && !linux

package auth

import "time"

// sweepProfileProcesses does nothing here. On Windows the helper's
// kill-on-close job already ends every process the browser started.
func sweepProfileProcesses(string, string, time.Duration) {}
