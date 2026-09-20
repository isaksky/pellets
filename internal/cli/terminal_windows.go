package cli

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func terminalWidth(w io.Writer) int {
	file, ok := w.(*os.File)
	if !ok {
		return 0
	}
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(file.Fd()), &info) != nil {
		return 0
	}
	return int(info.Window.Right - info.Window.Left + 1)
}
