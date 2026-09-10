package codex

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func darwinIdentity(info unix.KinfoProc) processIdentity {
	return processIdentity{pid: int(info.Proc.P_pid), parent: int(info.Eproc.Ppid), group: int(info.Eproc.Pgid),
		started: uint64(info.Proc.P_starttime.Sec)*1_000_000 + uint64(info.Proc.P_starttime.Usec),
		stopped: info.Proc.P_stat == 4, zombie: info.Proc.P_stat == 5}
}

func processSnapshot() (map[int]processIdentity, error) {
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := make(map[int]processIdentity, len(infos))
	for _, info := range infos {
		identity := darwinIdentity(info)
		table[identity.pid] = identity
	}
	return table, nil
}

func lookupProcess(pid int) (processIdentity, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	// Darwin reports a zero-length result (represented as EIO by x/sys) when
	// the process has disappeared between enumeration and identity validation.
	if errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ESRCH) {
		return processIdentity{}, syscall.ESRCH
	}
	if err != nil {
		return processIdentity{}, err
	}
	return darwinIdentity(*info), nil
}
