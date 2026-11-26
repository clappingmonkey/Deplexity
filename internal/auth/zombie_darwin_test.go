package auth

import "golang.org/x/sys/unix"

// sZomb is SZOMB from <sys/proc.h>.
const sZomb = 5

func isZombie(pid int) bool {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && p.Proc.P_stat == sZomb
}
