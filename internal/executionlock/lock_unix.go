//go:build darwin || linux

package executionlock

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

var errBusy = errors.New("execution lock busy")

// FileIdentity detects replacement at the same canonical path across restart.
func FileIdentity(path string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func tryLock(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errBusy
	}
	return err
}
