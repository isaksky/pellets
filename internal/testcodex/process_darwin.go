package testcodex

import "golang.org/x/sys/unix"

func Alive(pid int) bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && info.Proc.P_stat != 5
}
