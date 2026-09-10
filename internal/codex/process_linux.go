package codex

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func processSnapshot() (map[int]processIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	table := make(map[int]processIdentity)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		identity, err := lookupProcess(pid)
		if err == syscall.ESRCH {
			continue
		}
		if err != nil {
			return nil, err
		}
		table[pid] = identity
	}
	return table, nil
}

func lookupProcess(pid int) (processIdentity, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if os.IsNotExist(err) {
		return processIdentity{}, syscall.ESRCH
	}
	if err != nil {
		return processIdentity{}, err
	}
	// comm may contain spaces and parentheses; fields after its final ')' start
	// with state (field 3). Starttime is field 22 and survives exec and setsid.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return processIdentity{}, fmt.Errorf("invalid process stat for %d", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return processIdentity{}, fmt.Errorf("truncated process stat for %d", pid)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return processIdentity{}, err
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return processIdentity{}, err
	}
	started, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processIdentity{}, err
	}
	return processIdentity{pid: pid, parent: parent, group: group, started: started, stopped: fields[0] == "T" || fields[0] == "t", zombie: fields[0] == "Z"}, nil
}
