package auth

import (
	"bytes"
	"os"
	"strconv"
)

// isZombie reads the state field of /proc/<pid>/stat, which follows the
// parenthesized command name.
func isZombie(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
