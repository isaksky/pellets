package executionlock

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

var errBusy = errors.New("execution lock busy")

// Windows cannot prove settlement of the previous unnamed Job Object.
func FileIdentity(path string) (string, error) { return "", nil }

func tryLock(file *os.File) error {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errBusy
	}
	return err
}
