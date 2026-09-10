package testcodex

import (
	"os"
	"strconv"
	"strings"
)

func Alive(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return true
	}
	fields := strings.Fields(string(data[end+1:]))
	return len(fields) == 0 || fields[0] != "Z"
}
