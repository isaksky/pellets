package testcodex

import (
	"golang.org/x/sys/windows"
	"os/exec"
)

func Detach(cmd *exec.Cmd) {}

func Alive(pid int) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
