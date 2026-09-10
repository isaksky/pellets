//go:build darwin || linux

package testcodex

import (
	"os/exec"
	"syscall"
)

func Detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
