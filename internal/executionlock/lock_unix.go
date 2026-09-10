//go:build darwin || linux

package executionlock

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

var errBusy = errors.New("execution lock busy")

func tryLock(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errBusy
	}
	return err
}
