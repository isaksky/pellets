//go:build !windows

package cli

import (
	"io"
	"os"
	"strconv"

	"github.com/mattn/go-isatty"
	"golang.org/x/sys/unix"
)

func terminalWidth(w io.Writer) int {
	file, ok := w.(*os.File)
	if !ok || !isatty.IsTerminal(file.Fd()) {
		return 0
	}
	if size, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ); err == nil && size.Col > 0 {
		return int(size.Col)
	}
	if width, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && width > 0 {
		return width
	}
	return 80
}
