package webui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// OpenDefaultBrowser invokes only the operating system's documented URL
// launcher. Failure is returned to the foreground server as a non-fatal
// warning.
func OpenDefaultBrowser(targetURL string) error {
	name, arguments, err := browserCommand(runtime.GOOS, targetURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, name, arguments...).Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("browser launcher timed out: %w", ctx.Err())
		}
		return err
	}
	return nil
}

func browserCommand(goos, targetURL string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{targetURL}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", targetURL}, nil
	case "linux":
		return "xdg-open", []string{targetURL}, nil
	default:
		return "", nil, fmt.Errorf("browser opening is not supported on %s", goos)
	}
}
